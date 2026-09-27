package graph

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// A predicted LastSeq()+1 is never the operation sequence (P3-1, W7-5); 0
// defers allocation until after the replay check, so an identical retry
// consumes no sequence (FR-ING-006).
func TestMembershipOperationSequenceIsAllocatedOrDeferred(t *testing.T) {
	s, service, actor, intent := membershipTestStore(t)
	membershipFails(t, s, domain.ErrInvalidRecord, func(tx store.Tx) error {
		_, err := service.RegisterExchange(tx, actor, intent, tx.LastSeq()+1)
		return err
	})
	var first domain.RecordResult
	update(t, s, "s", func(tx store.Tx) error {
		var err error
		first, err = service.RegisterExchange(tx, actor, intent, 0)
		return err
	})
	update(t, s, "s", func(tx store.Tx) error {
		before := tx.LastSeq()
		again, err := service.RegisterExchange(tx, actor, intent, 0)
		if err != nil || !reflect.DeepEqual(again, first) || tx.LastSeq() != before {
			t.Fatalf("retry: %+v, %v, LastSeq %d -> %d", again, err, before, tx.LastSeq())
		}
		return nil
	})
}
