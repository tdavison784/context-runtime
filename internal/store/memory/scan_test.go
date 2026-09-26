package memory

import (
	"context"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestKeyedReadsDoNotScan checks that CurrentVersions and
// ObligationsBySource read only their key's index entry (SPEC-2.1): with
// hundreds of unrelated pointers and obligations in the session, neither
// iterates a whole table.
func TestKeyedReadsDoNotScan(t *testing.T) {
	s := New()
	defer s.Close()
	ctx := context.Background()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		for i := range 300 {
			id := fmt.Sprintf("d%d", i)
			if err := tx.InsertItem(storetest.NewDirective("s", id, id, tx.NextSeq(), id)); err != nil {
				return err
			}
			if err := tx.SetCurrentVersion(id); err != nil {
				return err
			}
			if err := tx.InsertObligationVersion(storetest.NewObligation("s", "o"+id, 1, tx.NextSeq(), id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, "s", func(stx store.Tx) error {
		inner := stx.(*store.Guard).TxBase.(*tx)
		inner.directives.scanned, inner.obligations.scanned = 0, 0
		ids, err := stx.CurrentVersions("task", domain.NamespaceDirective, "d7")
		if err != nil || len(ids) != 1 || ids[0] != "d7" {
			t.Errorf("CurrentVersions = %v, %v", ids, err)
		}
		obls, err := stx.ObligationsBySource("d7", 2)
		if err != nil || len(obls) != 1 || obls[0].ObligationID != "od7" {
			t.Errorf("ObligationsBySource = %v, %v", obls, err)
		}
		if inner.directives.scanned != 0 || inner.obligations.scanned != 0 {
			t.Errorf("scanned %d pointers and %d obligations, want 0", inner.directives.scanned, inner.obligations.scanned)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
