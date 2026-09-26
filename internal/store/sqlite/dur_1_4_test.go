package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestScanCancellationIsOperationalError(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		return tx.InsertItem(reviewItem("s", "i", tx.NextSeq()))
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	err := s.View(ctx, "s", func(tx store.ReadTx) error {
		cancel()
		_, err := tx.Item("i")
		return err
	})
	if !errors.Is(err, context.Canceled) || errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("Item after cancellation = %v", err)
	}
}
