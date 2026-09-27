package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// A predicted LastSeq()+1 or zero is never the operation sequence (P3-1, W7-5).
func TestMembershipRejectsUnallocatedOperationSequence(t *testing.T) {
	s, service, actor, intent := membershipTestStore(t)
	for _, predicted := range []bool{true, false} {
		membershipFails(t, s, domain.ErrInvalidRecord, func(tx store.Tx) error {
			seq := uint64(0)
			if predicted {
				seq = tx.LastSeq() + 1
			}
			_, err := service.RegisterExchange(tx, actor, intent, seq)
			return err
		})
	}
	update(t, s, "s", func(tx store.Tx) error {
		_, err := service.RegisterExchange(tx, actor, intent, tx.NextSeq())
		return err
	})
}
