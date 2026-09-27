package ingest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestLifecycleTxEntriesRefuseReservedRequestIDs_SEC37: the public
// transaction-level lifecycle entries are caller paths too; they never
// accept a reserved runtime namespace as a new request ID.
func TestLifecycleTxEntriesRefuseReservedRequestIDs_SEC37(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		svc := f.lifecycleService()
		u := principal(domain.AuthorityUser)
		g := mustDirective(t, f.mustIngest(u, userEvent("u-goal", "## Goal [g1]\nShip.\n", true)), "g1")
		for _, id := range []string{"gc_x", "gcq_x", "evt_x", "itm_x"} {
			err := f.s.Update(ctx, sess, func(tx store.Tx) error {
				_, err := svc.Resolve(tx, u, domain.ItemMutationIntent{RequestID: id, ItemID: g.ID, ExpectedVersion: g.Version}, tx.NextSeq())
				return err
			})
			if !errors.Is(err, domain.ErrInvalidRecord) {
				t.Errorf("Resolve(tx) accepted reserved request ID %q: %v", id, err)
			}
		}
	})
}
