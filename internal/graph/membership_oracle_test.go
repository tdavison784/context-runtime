package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// SEC-2.8: a runtime-derived request ID of another principal gives the same
// error whether or not that principal's receipt exists, so membership
// receipts are no existence oracle. The owner still replays (DUR-2.8).
func TestMembershipDerivedRequestIDIsNoExistenceOracle(t *testing.T) {
	s, service, actor, intent := membershipTestStore(t)
	registered, err := domain.OperationRequestID(actor, "occurrence", 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	absent, _ := domain.OperationRequestID(actor, "occurrence", 2, 0)
	intent.RequestID = registered
	var first domain.RecordResult
	update(t, s, "s", func(tx store.Tx) error {
		var err error
		first, err = service.RegisterExchange(tx, actor, intent, 0)
		return err
	})
	foreign := actor
	foreign.AgentID = "b"
	probe := func(requestID string) error {
		i := intent
		i.RequestID = requestID
		i.Principal.AgentID = "b"
		return s.Update(ctx, "s", func(tx store.Tx) error {
			_, err := service.RegisterExchange(tx, foreign, i, 0)
			return err
		})
	}
	existing, missing := probe(registered), probe(absent)
	if existing == nil || missing == nil || existing.Error() != missing.Error() || errors.Is(existing, domain.ErrEventIDConflict) {
		t.Fatalf("existence oracle: existing=%v missing=%v", existing, missing)
	}
	update(t, s, "s", func(tx store.Tx) error {
		again, err := service.RegisterExchange(tx, actor, intent, 0)
		if err != nil || again.IDs[0] != first.IDs[0] {
			t.Fatalf("owner replay: %+v, %v", again, err)
		}
		return nil
	})
}
