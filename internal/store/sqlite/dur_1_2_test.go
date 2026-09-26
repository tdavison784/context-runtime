package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestCancelledUpdateRollsBack(t *testing.T) {
	s, _ := openTemp(t)
	ctx, cancel := context.WithCancel(context.Background())
	err := s.Update(ctx, "cancelled", func(tx store.Tx) error {
		item := reviewItem("cancelled", "i", tx.NextSeq())
		if err := tx.InsertItem(item); err != nil {
			return err
		}
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Update = %v, want context.Canceled", err)
	}
	if err := s.View(context.Background(), "cancelled", func(tx store.ReadTx) error {
		if tx.LastSeq() != 0 {
			t.Fatalf("rolled-back sequence = %d", tx.LastSeq())
		}
		_, err := tx.Item("i")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("rolled-back item = %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
