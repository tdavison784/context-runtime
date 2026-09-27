package ingest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// TestDerivedRequestIDsArePrincipalBound_SEC12 is SEC-1.2 (G3, C-2/P3-35)
// through ingest. The request ID ingest derives for an executed lifecycle
// command belongs to that command's principal. Another principal presenting
// it, whether or not the command exists, gets the same bare invalid-record
// error with nothing written (no oracle), and can never claim the ID of a
// victim's future event (no squat). Callers cannot name the runtime req_
// namespace at all.
func TestDerivedRequestIDsArePrincipalBound_SEC12(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		userA := principal(domain.AuthorityUser)
		userB := principal(domain.AuthorityUser)
		userB.AgentID = "B"
		agentB := agentPrincipal()
		agentB.AgentID = "B"
		f.mustIngest(userA, userEvent("goal-a", "## Goal [ga]\nA's goal.\n## Goal [ga2]\nA's second goal.\n", true))
		rb := f.mustIngest(userB, userEvent("goal-b", "## Goal [gb]\nB's goal.\n", true))
		gb := mustDirective(t, rb, "gb")
		derived := func(p domain.Principal, eventID string) string {
			t.Helper()
			id, err := domain.OperationRequestID(p, domain.CallerOccurrenceID(sess, eventID), 0, 1)
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		// A's Resolve executes as a hidden command under a derived ID.
		f.mustIngest(userA, userEvent("res-a", "## Resolve [ga]\n", true))
		svc := f.lifecycleService()
		for _, req := range []string{derived(userA, "res-a"), derived(userA, "res-missing"), derived(userA, "res-a2")} {
			in := domain.ResolveIntent{RequestID: req, ItemID: gb.ID, ExpectedVersion: gb.Version}
			f.requireAtomic(domain.ErrInvalidRecord, func() error {
				_, err := svc.ResolveStandalone(ctx, userB, in)
				return err
			})
			f.requireAtomic(domain.ErrInvalidRecord, func() error {
				_, err := svc.UnpinStandalone(ctx, agentB, in)
				return err
			})
		}
		// The attempted squat on A's next EventID left A's event unaffected.
		r := f.mustIngest(userA, userEvent("res-a2", "## Resolve [ga2]\n", true))
		if len(r.Lifecycle) != 1 || r.Lifecycle[0].Execution == nil || r.Lifecycle[0].Execution.Outcome != domain.CommandOutcomeExecuted {
			t.Fatalf("A's resolve after B's squat attempt: %+v", r.Lifecycle)
		}
		// Callers cannot name the runtime namespace.
		f.requireAtomic(domain.ErrInvalidRecord, func() error {
			_, err := f.ingest(userA, userEvent("req_chosen", "hello", false))
			return err
		})
		f.requireAtomic(domain.ErrInvalidRecord, func() error {
			_, err := svc.ResolveStandalone(ctx, userB, domain.ResolveIntent{RequestID: "req_chosen", ItemID: gb.ID, ExpectedVersion: gb.Version})
			return err
		})
	})
}
