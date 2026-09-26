package ingest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestRetryAfterLimitsChange_F3 is SPEC-1.7/DUR-1.2 (F3): an exact retry of
// a known EventID replays its stored receipt before any limit or policy
// check, so tightening trusted configuration never turns a committed
// event's retry into a failure; new events still meet the new limits.
func TestRetryAfterLimitsChange_F3(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := userEvent("lim", "## Pinned\n- one\n- two\n"+strings.Repeat("x", 100), true)
		first := f.mustIngest(user, e)
		seq := f.lastSeq()
		for name, l := range map[string]domain.Limits{
			"MaxSpanBytes":    {MaxSpanBytes: 8},
			"MaxEventBytes":   {MaxEventBytes: 8},
			"MaxEventItems":   {MaxEventItems: 1},
			"MaxItemsPerSpan": {MaxItemsPerSpan: 1},
		} {
			f.in.Limits = l
			again, err := f.ingest(user, e)
			if err != nil || !reflect.DeepEqual(again, first) || f.lastSeq() != seq {
				t.Errorf("%s: retry = %v (equal %v)", name, err, reflect.DeepEqual(again, first))
			}
			fresh := e
			fresh.EventID = "new-" + name
			if _, err := f.ingest(user, fresh); !errors.Is(err, domain.ErrInvalidRecord) {
				t.Errorf("%s: a new event ignored the limit: %v", name, err)
			}
		}
	})
}

// TestRelationshipLimitOnReplaceAndDuplicate_DUR16: MaxRelationships bounds
// the SUPERSEDES and DUPLICATE_OF edges too, not only DERIVED_FROM.
func TestRelationshipLimitOnReplaceAndDuplicate_DUR16(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("v1", "## Pinned\n- [p] one\n", true))
		f.in.Limits = domain.Limits{MaxRelationships: 1}
		before := f.lastSeq()
		for name, text := range map[string]string{
			"replacement": "## Pinned\n- [p] two\n",
			"duplicate":   "Note.\n## Pinned\n- [p] one\n",
		} {
			if _, err := f.ingest(user, userEvent(name, text, true)); !errors.Is(err, domain.ErrInvalidRecord) || f.lastSeq() != before {
				t.Errorf("%s: err = %v, want the relationship limit", name, err)
			}
		}
	})
}

// TestRecordsAtNarrowerBoundary_F6 is SEC-1.3 (F6): diagnostic and
// lifecycle-command records carry the narrower of the span boundary and the
// boundary of what they describe, so a reader who cannot see the content or
// target cannot read its derived ID, range, or resolution either.
func TestRecordsAtNarrowerBoundary_F6(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u0", "hi", false))

		// A USER span may legally carry SESSION access; its directive is
		// narrowed to the task, and so must its derived-ID notice be.
		wide := textSpan(domain.AuthorityUser, true, "## Pinned\n- acquisition target is Contoso at $40/share\n")
		wide.Access = domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sess}
		f.mustIngest(user, domain.Event{EventID: "s1", Kind: domain.EventUser, Spans: []domain.Span{wide}})
		other := domain.Principal{SessionID: sess, WorkflowID: "wf2", TaskID: "T2", AgentID: "Z", Authority: domain.AuthorityUser}
		f.view(func(tx store.ReadTx) error {
			ds, err := tx.Diagnostics(store.DiagnosticFilter{Viewer: other})
			if err != nil {
				return err
			}
			for _, d := range ds {
				if d.DirectiveID != "" {
					t.Errorf("another task reads derived ID %s", d.DirectiveID)
				}
			}
			return nil
		})

		// Agent A's private pin resolved by an Unpin in a task-wide span:
		// agent B must not read the resolution.
		f.mustIngest(user, userEvent("a1", "## Pinned [plan] scope=AGENT\nPrivate plan.\n", true))
		f.mustIngest(user, userEvent("a2", "## Unpin [plan]\n", true))
		agentB := user
		agentB.AgentID = "B"
		f.view(func(tx store.ReadTx) error {
			cs, err := tx.LifecycleCommands(store.CommandFilter{Viewer: agentB})
			if err != nil {
				return err
			}
			for _, c := range cs {
				if c.ResolvedItemID != "" {
					t.Errorf("agent B reads resolution %s of an agent-A-private target", c.ResolvedItemID)
				}
			}
			return nil
		})
	})
}

// TestApplyFailureAtomic_DUR13: Apply is failure-atomic inside a caller's
// transaction. When it fails after its first write, it poisons the
// transaction, so even a caller that swallows the error and returns nil
// commits nothing, and the EventID is not poisoned: a later retry succeeds.
func TestApplyFailureAtomic_DUR13(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u0", "hi", false))
		before, items := f.lastSeq(), len(f.items())
		e := userEvent("partial", "## Pinned\n- one\n- two\n", true)

		tight := f.in
		tight.Limits = domain.Limits{MaxEventItems: 2} // fails after writing items
		var applyErr error
		updateErr := f.s.Update(ctx, sess, func(tx store.Tx) error {
			_, applyErr = tight.Apply(tx, user, e, "")
			return nil // a careless caller swallows the failure
		})
		if applyErr == nil || updateErr == nil {
			t.Fatalf("apply err %v, update err %v; want both to fail", applyErr, updateErr)
		}
		if f.lastSeq() != before || len(f.items()) != items {
			t.Errorf("a failed Apply committed partial writes")
		}
		if _, err := f.ingest(user, e); err != nil {
			t.Errorf("retry of the EventID after a failed Apply: %v", err)
		}
	})
}

// TestRecordsNeverRevealHiddenVersions_SEC22 completes SEC-1.3: an
// ambiguous lifecycle record and a boundary-conflict diagnostic are caused
// by versions the source actor sees, so they are readable only where those
// versions are too; another agent learns nothing of a private version.
func TestRecordsNeverRevealHiddenVersions_SEC22(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		agentA := principal(domain.AuthorityUser)
		agentB := agentA
		agentB.AgentID = "B"
		f.mustIngest(agentA, userEvent("u0", "hi", false))

		// Ambiguous: A's private [plan], B's task-wide [plan] (B cannot see
		// A's), then A's Unpin sees both.
		f.mustIngest(agentA, userEvent("a1", "## Pinned [plan] scope=AGENT\nA's private plan.\n", true))
		f.mustIngest(agentB, userEvent("b1", "## Pinned [plan]\nB's plan.\n", true))
		r := f.mustIngest(agentA, userEvent("a2", "## Unpin [plan]\n", true))
		if len(r.Lifecycle) != 1 || r.Lifecycle[0].Resolution != domain.TargetAmbiguous {
			t.Fatalf("setup: commands %+v", r.Lifecycle)
		}

		// Boundary conflict: A restates [q] task-wide while its private [q]
		// is current.
		f.mustIngest(agentA, userEvent("a3", "## Pinned [q] scope=AGENT\nA's private q.\n", true))
		r = f.mustIngest(agentA, userEvent("a4", "## Pinned [q]\nTask-wide q.\n", true))
		if !hasDiag(r, domain.ErrMalformedDirective, domain.ReasonBoundaryConflict) {
			t.Fatalf("setup: no boundary conflict: %+v", r.Diagnostics)
		}

		// Hidden versus missing (SEC-3.2): A unpins its private [solo] and
		// an ID that names nothing. Agent B, who can read both transcripts,
		// must see identical records for the two: one per command, with
		// no target-dependent detail, and the same diagnostics.
		f.mustIngest(agentA, userEvent("a5", "## Pinned [solo] scope=AGENT\nA's private solo.\n", true))
		hidden := f.mustIngest(agentA, userEvent("a6", "## Unpin [solo]\n", true))
		missing := f.mustIngest(agentA, userEvent("a7", "## Unpin [nothing]\n", true))
		f.view(func(tx store.ReadTx) error {
			shape := func(occ string) string {
				cs, err := tx.LifecycleCommands(store.CommandFilter{Viewer: agentB, OccurrenceID: occ})
				ds, derr := tx.Diagnostics(store.DiagnosticFilter{Viewer: agentB, OccurrenceID: occ})
				if err != nil || derr != nil {
					t.Fatalf("reads: %v %v", err, derr)
				}
				out := fmt.Sprintf("commands=%d diagnostics=%d", len(cs), len(ds))
				for _, c := range cs {
					out += fmt.Sprintf(" status=%s resolution=%s item=%q v=%d", c.Status, c.Resolution, c.ResolvedItemID, c.ResolvedVersion)
				}
				return out
			}
			if h, m := shape(hidden.OccurrenceID), shape(missing.OccurrenceID); h != m {
				t.Errorf("agent B tells a hidden target from a missing one:\n hidden:  %s\n missing: %s", h, m)
			}
			// The source actor still reads the full resolution.
			cs, err := tx.LifecycleCommands(store.CommandFilter{Viewer: agentA, OccurrenceID: hidden.OccurrenceID})
			if err != nil || len(cs) != 1 || cs[0].Resolution != domain.TargetResolved || cs[0].ResolvedItemID == "" {
				t.Errorf("agent A's own record = %+v, %v", cs, err)
			}
			return nil
		})

		f.view(func(tx store.ReadTx) error {
			cs, err := tx.LifecycleCommands(store.CommandFilter{Viewer: agentB})
			if err != nil {
				return err
			}
			for _, c := range cs {
				if c.Resolution == domain.TargetAmbiguous {
					t.Errorf("agent B reads an AMBIGUOUS record caused by A's private version")
				}
			}
			ds, err := tx.Diagnostics(store.DiagnosticFilter{Viewer: agentB})
			if err != nil {
				return err
			}
			for _, d := range ds {
				if d.Code == domain.ErrAmbiguousDirective || d.Reason == domain.ReasonBoundaryConflict {
					t.Errorf("agent B reads %s/%s revealing A's private version", d.Code, d.Reason)
				}
			}
			// Agent A, who caused and can see both, still reads them.
			ds, err = tx.Diagnostics(store.DiagnosticFilter{Viewer: agentA})
			seen := 0
			for _, d := range ds {
				if d.Code == domain.ErrAmbiguousDirective || d.Reason == domain.ReasonBoundaryConflict {
					seen++
				}
			}
			if seen != 2 {
				t.Errorf("agent A reads %d of its own ambiguity/conflict diagnostics, want 2", seen)
			}
			return err
		})
	})
}

// countingStore counts write transactions.
type countingStore struct {
	store.Store
	updates *int
}

func (s countingStore) Update(ctx context.Context, sessionID string, fn func(store.Tx) error) error {
	*s.updates++
	return s.Store.Update(ctx, sessionID, fn)
}

// TestOverLimitNewEventsStayOutOfWriteTx_SEC21: F3 still replays a known
// EventID whatever the limits, but a NEW event over the configured limits
// is rejected by size alone, before its payload is copied or hashed and
// without ever entering the session write transaction; an input over the
// hard, non-configurable ceiling is rejected before anything else, even as
// a retry.
func TestOverLimitNewEventsStayOutOfWriteTx_SEC21(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		known := userEvent("known", strings.Repeat("k", 2048), false)
		f.mustIngest(user, known)
		updates := 0
		f.s = countingStore{f.s, &updates}
		f.in.Limits = domain.Limits{MaxSpanBytes: 1024, MaxEventBytes: 1024, MaxBlobBytes: 1024}
		big := domain.InputPart{Type: domain.PartImage, MediaType: "image/png", Data: make([]byte, 4096)}
		for name, e := range map[string]domain.Event{
			"anonymous":   {Kind: domain.EventUser, Spans: []domain.Span{{Authority: domain.AuthorityUser, Access: taskAccess(), Parts: []domain.InputPart{big}}}},
			"new EventID": {EventID: "new", Kind: domain.EventUser, Spans: []domain.Span{{Authority: domain.AuthorityUser, Access: taskAccess(), Parts: []domain.InputPart{big}}}},
		} {
			if _, err := f.ingest(user, e); !errors.Is(err, domain.ErrInvalidRecord) {
				t.Errorf("%s: err = %v, want a limit rejection", name, err)
			}
		}
		if updates != 0 {
			t.Errorf("over-limit new events entered %d write transactions, want 0", updates)
		}
		if _, err := f.ingest(user, known); err != nil {
			t.Errorf("known EventID retry over the limits: %v (F3)", err)
		}

		defer func(v uint64) { hardMaxEventBytes = v }(hardMaxEventBytes)
		hardMaxEventBytes = 1024
		updates = 0
		if _, err := f.ingest(user, known); !errors.Is(err, domain.ErrInvalidRecord) || updates != 0 {
			t.Errorf("over the hard ceiling: err = %v, updates %d; want rejection before any transaction", err, updates)
		}
	})
}

// TestSizeGateMatchesValidateFor_SEC21: the length-only admission gate
// accepts exactly what ValidateFor's limit accounting accepts at the edge,
// so it never rejects a valid event and never admits an over-limit one.
func TestSizeGateMatchesValidateFor_SEC21(t *testing.T) {
	user := principal(domain.AuthorityUser)
	l := domain.Limits{MaxSpanBytes: 64, MaxEventBytes: 96, MaxBlobBytes: 32}.Effective()
	mk := func(text, blob int) domain.Event {
		parts := []domain.InputPart{{Type: domain.PartText, MediaType: "text/plain", Text: strings.Repeat("t", text)}}
		if blob > 0 {
			parts = append(parts, domain.InputPart{Type: domain.PartImage, MediaType: "image/png", Data: make([]byte, blob)})
		}
		return domain.Event{EventID: "edge", Kind: domain.EventUser, Spans: []domain.Span{{Authority: domain.AuthorityUser, Access: taskAccess(), Parts: parts}}}
	}
	for _, c := range []struct{ text, blob int }{{64, 32}, {65, 0}, {64, 33}, {60, 32}, {64, 31}} {
		e := mk(c.text, c.blob)
		gate, exact := checkSizes(e, configuredSizes(l)) == nil, e.ValidateFor(user, l) == nil
		if gate != exact {
			t.Errorf("text %d blob %d: gate accepts %v, ValidateFor accepts %v", c.text, c.blob, gate, exact)
		}
	}
}

// TestKnownEventIDCannotSmuggleOversizePayload_SEC31: the cheap read that
// admits an over-limit retry also proves it can match: the stored receipt
// must be this principal's and the event's per-span and per-part counts
// and byte lengths must equal the stored envelope's. Otherwise it is a bare
// ErrEventIDConflict from the read, and the oversized payload is never
// copied, hashed, or taken into a write transaction, for the same
// principal or another; an exact retry still replays (F3).
func TestKnownEventIDCannotSmuggleOversizePayload_SEC31(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("known", "small original", false))
		img := domain.Event{EventID: "img", Kind: domain.EventUser, Spans: []domain.Span{{Authority: domain.AuthorityUser, Access: taskAccess(),
			Parts: []domain.InputPart{{Type: domain.PartImage, MediaType: "image/png", Data: make([]byte, 2048)}}}}}
		f.mustIngest(user, img)

		updates := 0
		f.s = countingStore{f.s, &updates}
		f.in.Limits = domain.Limits{MaxSpanBytes: 1024, MaxEventBytes: 1024, MaxBlobBytes: 1024}
		huge := domain.InputPart{Type: domain.PartImage, MediaType: "image/png", Data: make([]byte, 64<<10)}
		other := domain.Principal{SessionID: sess, WorkflowID: "wf2", TaskID: "T2", AgentID: "Z", Authority: domain.AuthorityUser}
		for name, c := range map[string]struct {
			p domain.Principal
			a domain.AccessBoundary
		}{"same principal": {user, taskAccess()}, "other principal": {other, domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "T2"}}} {
			e := domain.Event{EventID: "known", Kind: domain.EventUser, Spans: []domain.Span{{Authority: domain.AuthorityUser, Access: c.a, Parts: []domain.InputPart{huge}}}}
			if _, err := f.ingest(c.p, e); err != domain.ErrEventIDConflict {
				t.Errorf("%s: err = %v, want bare ErrEventIDConflict", name, err)
			}
		}
		if updates != 0 {
			t.Errorf("mismatched over-limit retries entered %d write transactions, want 0", updates)
		}
		if _, err := f.ingest(user, img); err != nil {
			t.Errorf("exact over-limit retry: %v (F3)", err)
		}
	})
}
