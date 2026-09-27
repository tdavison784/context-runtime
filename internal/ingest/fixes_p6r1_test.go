package ingest

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/store"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Regression tests for PR #6 review round 1 (ingest findings).

// TestEmptyOperationStreamRejected_G4 is SEC-1.11/SPEC-1.2 (G4, P3-34/40):
// an event with spans and an empty, non-nil Operations stream is malformed
// and aborts with nothing written, rather than committing an envelope whose
// spans were never ingested. The nil form still ingests every span, and it
// is not locked out by the rejected empty form (nil-vs-empty round trip).
func TestEmptyOperationStreamRejected_G4(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := userEvent("g4-empty-ops", "hello there", false)
		e.Operations = []domain.SemanticOperation{}
		f.requireAtomic(domain.ErrInvalidRecord, func() error {
			_, err := f.ingest(user, e)
			return err
		})
		e.Operations = nil
		r := f.mustIngest(user, e)
		if len(r.ItemIDs()) != 1 {
			t.Fatalf("nil stream ingested %d items, want 1", len(r.ItemIDs()))
		}
		again, err := f.ingest(user, e)
		if err != nil || !reflect.DeepEqual(again, r) {
			t.Fatalf("nil-stream retry = %v", err)
		}
		e.Operations = []domain.SemanticOperation{}
		if _, err := f.ingest(user, e); !errors.Is(err, domain.ErrInvalidRecord) && !errors.Is(err, domain.ErrEventIDConflict) {
			t.Fatalf("empty-stream retry of the nil form = %v, want rejection", err)
		}
	})
}

// TestPlainIngestCannotClaimOutcomeEventIDs_SEC14 is SEC-1.4 (G3, R20.1):
// the outcome- EventID namespace belongs to the call ledger's outcome path.
// A plain Ingest or Apply naming a call's output EventID, one of its tool
// results, or any other outcome- ID aborts with nothing written, so it can
// neither squat the canonical ID (making the real outcome conflict) nor be
// replayed by the outcome path as if it had been registered.
func TestPlainIngestCannotClaimOutcomeEventIDs_SEC14(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthorityUser), userEvent("q", "Run the tests.", false))
		agent := agentPrincipal()
		b := f.inference(agent, "r1")
		out := outcomeEvent(b, "Running the tests.")
		tool := toolResultEvent(b, "run-tests", "PASS 12/12")
		other := userEvent("outcome-chosen", "hello", false)
		for _, tc := range map[string]struct {
			p domain.Principal
			e domain.Event
		}{
			"output ID":      {agent, out},
			"tool result ID": {agent, tool},
			"harness tool":   {dispatcherFor(agent), tool},
			"any outcome-":   {principal(domain.AuthorityUser), other},
		} {
			f.requireAtomic(domain.ErrInvalidRecord, func() error {
				_, err := f.ingest(tc.p, tc.e)
				return err
			})
			f.requireAtomic(domain.ErrInvalidRecord, func() error {
				return f.s.Update(ctx, sess, func(tx store.Tx) error {
					_, err := f.in.Apply(tx, tc.p, tc.e, "")
					return err
				})
			})
		}
		// The real outcome and its tool result still register under their
		// bindings.
		m := &OutcomeMembership{Dispatcher: dispatcherFor(agent), ToolCallIDs: []string{"run-tests"}}
		if _, err := f.in.IngestOutcome(ctx, f.s, b, out, m); err != nil {
			t.Fatal(err)
		}
		if _, err := f.in.IngestOutcome(ctx, f.s, b, tool, &OutcomeMembership{Dispatcher: dispatcherFor(agent)}); err != nil {
			t.Fatal(err)
		}
		if _, ms := f.members(b.ExchangeID); len(ms) != 3 {
			t.Fatalf("members = %d, want output, tool call and tool result", len(ms))
		}
	})
}

// TestOutcomeMembershipIsRequestIdentity_DUR17 is DUR-1.7 (G3): an
// outcome's membership (dispatcher and ordered tool call IDs, or none) is
// part of its request identity. A retry with different membership
// conflicts with nothing written instead of silently succeeding; an
// untrusted dispatcher is refused even on the retry path; the exact retry
// replays its receipt.
func TestOutcomeMembershipIsRequestIdentity_DUR17(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthorityUser), userEvent("q", "Run the tests.", false))
		agent := agentPrincipal()
		b := f.inference(agent, "r1")
		out := outcomeEvent(b, "Running the tests.")
		m := &OutcomeMembership{Dispatcher: dispatcherFor(agent), ToolCallIDs: []string{"call-a"}}
		first, err := f.in.IngestOutcome(ctx, f.s, b, out, m)
		if err != nil {
			t.Fatal(err)
		}
		selfDispatch := &OutcomeMembership{Dispatcher: agent, ToolCallIDs: []string{"call-a"}}
		for _, tc := range map[string]struct {
			m    *OutcomeMembership
			want error
		}{
			"other tool calls":  {&OutcomeMembership{Dispatcher: dispatcherFor(agent), ToolCallIDs: []string{"call-x", "call-y"}}, domain.ErrEventIDConflict},
			"reordered/dropped": {&OutcomeMembership{Dispatcher: dispatcherFor(agent)}, domain.ErrEventIDConflict},
			"no membership":     {nil, domain.ErrEventIDConflict},
			"agent dispatcher":  {selfDispatch, domain.ErrInvalidAuthorityPromotion},
		} {
			f.requireAtomic(tc.want, func() error {
				_, err := f.in.IngestOutcome(ctx, f.s, b, out, tc.m)
				return err
			})
		}
		seq := f.lastSeq()
		again, err := f.in.IngestOutcome(ctx, f.s, b, out, m)
		if err != nil || !reflect.DeepEqual(again, first) || f.lastSeq() != seq {
			t.Fatalf("exact retry = %v (equal %v)", err, reflect.DeepEqual(again, first))
		}
	})
	semanticStores(t, func(t *testing.T, f *fixture) {
		// Without membership first, a retry that adds one conflicts rather
		// than reporting success for a round that can never progress.
		f.mustIngest(principal(domain.AuthorityUser), userEvent("q", "Run the tests.", false))
		agent := agentPrincipal()
		b := f.inference(agent, "r1")
		out := outcomeEvent(b, "Running the tests.")
		if _, err := f.in.IngestOutcome(ctx, f.s, b, out, nil); err != nil {
			t.Fatal(err)
		}
		f.requireAtomic(domain.ErrEventIDConflict, func() error {
			_, err := f.in.IngestOutcome(ctx, f.s, b, out, &OutcomeMembership{Dispatcher: dispatcherFor(agent)})
			return err
		})
	})
}
