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
	// H5: a runtime request ID binds the event sequence allocated in the
	// transaction that first uses it.
	var registered, absent string
	var first domain.RecordResult
	update(t, s, "s", func(tx store.Tx) error {
		seq, occurrence := tx.NextSeq(), domain.CallerOccurrenceID("s", "event-1")
		var err error
		if registered, err = domain.OperationRequestID(actor, actor, occurrence, seq, 1, 0); err != nil {
			return err
		}
		if absent, err = domain.OperationRequestID(actor, actor, occurrence, seq, 2, 0); err != nil {
			return err
		}
		intent.RequestID = registered
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
	// A later transaction: runtime receipts never replay through a service
	// (an outcome retry replays its event receipt), so even the owner is
	// refused, identically to an absent receipt (SEC-3.6).
	for _, id := range []string{registered, absent} {
		i := intent
		i.RequestID = id
		err := s.Update(ctx, "s", func(tx store.Tx) error {
			_, err := service.RegisterExchange(tx, actor, i, 0)
			return err
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatalf("later-transaction runtime request %q: %v", id, err)
		}
	}
	_ = first
}
