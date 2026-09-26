package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// Deliberately hides the optional semantic facet; all actual writes and
// rollback remain the real memory transaction, not a semantic persistence stub.
type legacyOnly struct{ store.Tx }

func TestApplyWithoutReceiptStoragePoisonsIgnoredFailure(t *testing.T) {
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	p := storetest.NewPrincipal("s", domain.AuthorityUser)
	err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
		if err := tx.InsertItem(storetest.NewItem("s", "earlier", tx.NextSeq(), "must roll back")); err != nil {
			return err
		}
		got, err := s.ApplyResolve(legacyOnly{tx}, p, domain.ResolveIntent{RequestID: "r", ItemID: "earlier", ExpectedVersion: 1}, tx.NextSeq())
		if !errors.Is(err, domain.ErrUnsupportedSchema) || got.ItemID != "" {
			t.Fatalf("unsupported schema: %+v %v", got, err)
		}
		return nil // a caller ignoring a failed constituent must not commit
	})
	if !errors.Is(err, domain.ErrUnsupportedSchema) {
		t.Fatal(err)
	}
	if err := mem.View(context.Background(), "s", func(tx store.ReadTx) error {
		if tx.LastSeq() != 0 {
			t.Fatal("failed operation committed allocated sequence")
		}
		_, err := tx.Item("earlier")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("earlier write escaped rollback")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
