package ingest

import (
	"errors"
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
