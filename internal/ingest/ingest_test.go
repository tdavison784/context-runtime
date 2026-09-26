package ingest

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestRetryIdentity_D14: a retried event returns the original receipt
// unchanged without allocating anything; a different request under the
// same EventID is a bare conflict; anonymous events are never idempotent.
func TestRetryIdentity_D14(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		p := principal(domain.AuthorityUser)
		first := f.mustIngest(p, userEvent("e1", "hello", false))
		seq := f.lastSeq()

		again := f.mustIngest(p, userEvent("e1", "hello", false))
		if !reflect.DeepEqual(first, again) {
			t.Errorf("retry receipt differs:\n%+v\n%+v", first, again)
		}
		if f.lastSeq() != seq {
			t.Errorf("retry allocated sequence numbers: %d -> %d", seq, f.lastSeq())
		}

		for name, e := range map[string]domain.Event{
			"text":      userEvent("e1", "hello!", false),
			"capable":   userEvent("e1", "hello", true),
			"principal": userEvent("e1", "hello", false),
		} {
			pp := p
			if name == "principal" {
				pp.AgentID = "B"
			}
			_, err := f.ingest(pp, e)
			if err != domain.ErrEventIDConflict {
				t.Errorf("%s: err = %v, want bare ErrEventIDConflict", name, err)
			}
		}

		a1 := f.mustIngest(p, userEvent("", "hello", false))
		a2 := f.mustIngest(p, userEvent("", "hello", false))
		if a1.OccurrenceID == a2.OccurrenceID || slices.Equal(a1.ItemIDs(), a2.ItemIDs()) {
			t.Errorf("anonymous events aliased: %s %s", a1.OccurrenceID, a2.OccurrenceID)
		}
	})
}

// TestDerivedIDsStable_D14: item IDs derive from (session, EventID, index),
// so the same event in a fresh store reproduces them.
func TestDerivedIDsStable_D14(t *testing.T) {
	var ids [][]string
	eachStore(t, func(t *testing.T, f *fixture) {
		ids = append(ids, f.mustIngest(principal(domain.AuthorityUser), userEvent("e1", "hello", false)).ItemIDs())
	})
	if len(ids) != 2 || !slices.Equal(ids[0], ids[1]) {
		t.Errorf("IDs differ across stores: %v", ids)
	}
}

// TestT18_PastedDocument is T18 step 1 (ingestion state): a USER span not
// marked directive-capable yields exactly one user_message and no goal,
// pin, or obligation; the skipped headings are diagnosed without content.
func TestT18_PastedDocument(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		doc := "Design notes\n## Goal\nShip the thing\n## Pinned\n- Never deploy on Fridays\n"
		r := f.mustIngest(principal(domain.AuthorityUser), userEvent("e1", doc, false))
		if got := kinds(f.items()); !slices.Equal(got, []domain.Kind{domain.KindUserMessage}) {
			t.Fatalf("items = %v, want one user_message", got)
		}
		if r.Items[0].Role != domain.RoleTranscript || r.Items[0].Parts[0].Text != doc {
			t.Errorf("transcript = %+v, want the verbatim span", r.Items[0])
		}
		notParsed := 0
		for _, d := range r.Diagnostics {
			if d.Code == domain.DirectiveNotParsed && d.Reason == domain.ReasonSourceNotCapable {
				notParsed++
			}
		}
		if notParsed != 2 {
			t.Errorf("DirectiveNotParsed diagnostics = %d, want 2: %+v", notParsed, r.Diagnostics)
		}
		f.view(func(tx store.ReadTx) error {
			obs, err := tx.Obligations("")
			if len(obs) != 0 {
				t.Errorf("obligations = %d, want 0", len(obs))
			}
			return err
		})
	})
}

// TestTurns_D18: a USER event opens a turn exactly once however many spans
// it has; a HARNESS event opens one only when marked; AGENT/TOOL events
// never do; retries never advance it.
func TestTurns_D18(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		turn := func() uint64 {
			var n uint64
			f.view(func(tx store.ReadTx) error {
				ts, err := tx.Task("T")
				n = ts.Turn
				return err
			})
			return n
		}
		sys := principal(domain.AuthoritySystem)
		r := f.mustIngest(sys, domain.Event{EventID: "h0", Kind: domain.EventHarness, Spans: []domain.Span{textSpan(domain.AuthorityHarness, false, "setup")}})
		if turn() != 0 || r.OpenedTurn != 0 {
			t.Errorf("unmarked HARNESS event opened a turn")
		}
		two := userEvent("u1", "one", false)
		two.Spans = append(two.Spans, textSpan(domain.AuthorityUser, false, "two"))
		r = f.mustIngest(sys, two)
		if turn() != 1 || r.OpenedTurn != 1 || r.TurnID != domain.DerivedTurnID(sess, "T", 1) {
			t.Errorf("turn = %d, receipt %d %q; want 1", turn(), r.OpenedTurn, r.TurnID)
		}
		for _, it := range r.Items {
			if it.CreatedTurn != 1 || it.TurnID != r.TurnID {
				t.Errorf("item %s turn = %d %q", it.ID, it.CreatedTurn, it.TurnID)
			}
		}
		f.mustIngest(sys, two)
		f.mustIngest(sys, domain.Event{EventID: "a1", Kind: domain.EventAgent, Spans: []domain.Span{textSpan(domain.AuthorityAgent, false, "reply")}})
		f.mustIngest(sys, domain.Event{EventID: "h1", Kind: domain.EventHarness, TurnBoundary: true, Spans: []domain.Span{textSpan(domain.AuthorityHarness, false, "next")}})
		if turn() != 2 {
			t.Errorf("turn = %d, want 2", turn())
		}
	})
}

// TestTurnOwnership_D18: TURN-scoped evidence needs an opened turn, and a
// COMPLETED task or a task of another workflow is never touched.
func TestTurnOwnership_D18(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		tool := domain.Event{EventID: "t0", Kind: domain.EventTool, Spans: []domain.Span{textSpan(domain.AuthorityTool, false, "output")}}
		if _, err := f.ingest(sys, tool); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("tool result before any turn: err = %v, want ErrInvalidRecord", err)
		}
		f.mustIngest(sys, userEvent("u1", "hi", false))
		f.mustIngest(sys, tool)

		other := sys
		other.WorkflowID = "wf2"
		if _, err := f.ingest(other, userEvent("u2", "hi", false)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Errorf("foreign workflow: err = %v", err)
		}
	})
}

// TestAuthority_D15: spans cannot outrank the caller or the event kind,
// low-authority spans cannot be directive-capable, span boundaries must
// match the principal, and any rejection writes nothing.
func TestAuthority_D15(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		before := f.lastSeq()
		cases := map[string]struct {
			p domain.Principal
			e domain.Event
		}{
			"SpanOutranksCaller": {user, domain.Event{EventID: "x1", Kind: domain.EventSystem, Spans: []domain.Span{textSpan(domain.AuthoritySystem, false, "x")}}},
			"SpanOutranksKind":   {principal(domain.AuthoritySystem), domain.Event{EventID: "x2", Kind: domain.EventUser, Spans: []domain.Span{textSpan(domain.AuthoritySystem, false, "x")}}},
			"CapableAgentSpan":   {user, domain.Event{EventID: "x3", Kind: domain.EventAgent, Spans: []domain.Span{textSpan(domain.AuthorityAgent, true, "x")}}},
			"ForeignBoundary": {user, domain.Event{EventID: "x4", Kind: domain.EventUser, Spans: []domain.Span{func() domain.Span {
				s := textSpan(domain.AuthorityUser, false, "x")
				s.Access.TaskID = "other"
				return s
			}()}}},
		}
		for name, c := range cases {
			if _, err := f.ingest(c.p, c.e); err == nil {
				t.Errorf("%s: accepted", name)
			}
		}
		if f.lastSeq() != before || len(f.items()) != 0 {
			t.Errorf("a rejected event wrote state")
		}
	})
}

// TestBlobReferences_R5: supplied bytes are stored and hashed; a reference
// is accepted only for a blob an accessible item already references within
// a no-broader boundary, and missing and inaccessible references fail
// identically.
func TestBlobReferences_R5(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		img := []byte("\x89PNG fake")
		hash := domain.HashBytes(img)
		imgSpan := func(a domain.AccessBoundary, part domain.InputPart) domain.Span {
			return domain.Span{Authority: domain.AuthorityUser, Access: a, Parts: []domain.InputPart{part}}
		}
		private := domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, AgentID: "A", TaskID: "T"}
		agentA := principal(domain.AuthorityUser)
		f.mustIngest(agentA, domain.Event{EventID: "img", Kind: domain.EventUser, Spans: []domain.Span{imgSpan(private, domain.InputPart{Type: domain.PartImage, MediaType: "image/png", Data: img})}})

		ref := domain.InputPart{Type: domain.PartImage, MediaType: "image/png", BlobHash: hash, BlobSize: uint64(len(img))}
		agentB := agentA
		agentB.AgentID = "B"
		_, errHidden := f.ingest(agentB, domain.Event{EventID: "r1", Kind: domain.EventUser, Spans: []domain.Span{imgSpan(taskAccess(), ref)}})
		missing := ref
		missing.BlobHash = domain.HashBytes([]byte("nope"))
		_, errMissing := f.ingest(agentB, domain.Event{EventID: "r2", Kind: domain.EventUser, Spans: []domain.Span{imgSpan(taskAccess(), missing)}})
		if errHidden != domain.ErrNotFound || errMissing != domain.ErrNotFound {
			t.Errorf("hidden = %v, missing = %v; want identical bare ErrNotFound", errHidden, errMissing)
		}
		// Agent A may not widen its own private image to the whole task by
		// reference either.
		if _, err := f.ingest(agentA, domain.Event{EventID: "r3", Kind: domain.EventUser, Spans: []domain.Span{imgSpan(taskAccess(), ref)}}); err != domain.ErrNotFound {
			t.Errorf("widening reference: err = %v, want ErrNotFound", err)
		}
		if _, err := f.ingest(agentA, domain.Event{EventID: "r4", Kind: domain.EventUser, Spans: []domain.Span{imgSpan(private, ref)}}); err != nil {
			t.Errorf("same-boundary reference: %v", err)
		}
	})
}

// TestCallerBuffersCopied_D14: mutating the caller's event after Ingest
// returns cannot change what was stored.
func TestCallerBuffersCopied_D14(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		data := []byte("doc bytes")
		e := domain.Event{EventID: "d1", Kind: domain.EventUser, Spans: []domain.Span{{Authority: domain.AuthorityUser, Access: taskAccess(),
			Parts: []domain.InputPart{{Type: domain.PartDocument, MediaType: "application/pdf", Data: data}}}}}
		f.mustIngest(principal(domain.AuthorityUser), e)
		data[0] = 'X'
		f.view(func(tx store.ReadTx) error {
			b, err := tx.Blob(domain.HashBytes([]byte("doc bytes")))
			if err != nil || string(b.Data) != "doc bytes" {
				t.Errorf("blob = %q, %v", b.Data, err)
			}
			return nil
		})
	})
}

// TestBlobReferenceLookupBound_R19: blob-reference authorization reads the
// bounded blob index; more referrers than the lookup limit reject the event
// (fail closed) instead of deciding on a partial list.
func TestBlobReferenceLookupBound_R19(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		img := []byte("shared image")
		part := domain.InputPart{Type: domain.PartImage, MediaType: "image/png", Data: img}
		user := principal(domain.AuthorityUser)
		for _, id := range []string{"i1", "i2", "i3"} {
			f.mustIngest(user, domain.Event{EventID: id, Kind: domain.EventUser, Spans: []domain.Span{{Authority: domain.AuthorityUser, Access: taskAccess(), Parts: []domain.InputPart{part}}}})
		}
		ref := domain.InputPart{Type: domain.PartImage, MediaType: "image/png", BlobHash: domain.HashBytes(img), BlobSize: uint64(len(img))}
		e := domain.Event{EventID: "r1", Kind: domain.EventUser, Spans: []domain.Span{{Authority: domain.AuthorityUser, Access: taskAccess(), Parts: []domain.InputPart{ref}}}}
		f.in.LookupLimit = 2
		before := f.lastSeq()
		if _, err := f.ingest(user, e); !errors.Is(err, store.ErrLimitExceeded) || f.lastSeq() != before {
			t.Errorf("over the bound: err = %v", err)
		}
		// Within the blob bound; the image transcript's duplicate lookup
		// also sees three identical earlier transcripts plus itself.
		f.in.LookupLimit = 4
		if _, err := f.ingest(user, e); err != nil {
			t.Errorf("within the bound: %v", err)
		}
	})
}
