package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestMembershipRefusesReservedRequestIDs_SEC37: a membership caller can
// never name a reserved runtime namespace; a runtime ID of this very
// transaction (ingest's outcome path) is still accepted.
func TestMembershipRefusesReservedRequestIDs_SEC37(t *testing.T) {
	s, service, actor, intent := membershipTestStore(t)
	for _, id := range []string{"gc_x", "gcq_x", "evt_x", "itm_x"} {
		i := intent
		i.RequestID = id
		err := s.Update(ctx, "s", func(tx store.Tx) error {
			_, err := service.RegisterExchange(tx, actor, i, 0)
			return err
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("RegisterExchange accepted reserved request ID %q: %v", id, err)
		}
	}
	update(t, s, "s", func(tx store.Tx) error {
		id, err := domain.OperationRequestID(actor, actor, domain.CallerOccurrenceID("s", "event-1"), tx.NextSeq(), 1, 0)
		if err != nil {
			return err
		}
		i := intent
		i.RequestID = id
		_, err = service.RegisterExchange(tx, actor, i, 0)
		return err
	})
}
