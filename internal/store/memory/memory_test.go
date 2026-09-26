package memory_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store { return memory.New() })
}

// TestConversationOwnersImmutable covers a rule beyond the store contract
// that the SQLite store also enforces: a conversation's task and agent never
// change.
func TestConversationOwnersImmutable(t *testing.T) {
	s := memory.New()
	err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		c := storetest.NewConversation("s", "conv")
		if _, err := tx.PutConversation(c, 0); err != nil {
			return err
		}
		c.AgentID = "other"
		if _, err := tx.PutConversation(c, 1); !errors.Is(err, domain.ErrImmutable) {
			t.Errorf("PutConversation with a new agent: %v, want ErrImmutable", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClosed(t *testing.T) {
	s := memory.New()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(context.Background(), "s", func(store.Tx) error { return nil }); !errors.Is(err, memory.ErrClosed) {
		t.Errorf("Update after Close: %v, want ErrClosed", err)
	}
	if err := s.View(context.Background(), "s", func(store.ReadTx) error { return nil }); !errors.Is(err, memory.ErrClosed) {
		t.Errorf("View after Close: %v, want ErrClosed", err)
	}
}

func TestCanceledContext(t *testing.T) {
	s := memory.New()
	ctx, cancel := context.WithCancel(context.Background())
	err := s.Update(ctx, "s", func(tx store.Tx) error {
		tx.NextSeq()
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Update with a canceled context: %v, want context.Canceled", err)
	}
	_ = s.View(context.Background(), "s", func(tx store.ReadTx) error {
		if tx.LastSeq() != 0 {
			t.Errorf("LastSeq = %d after canceled Update, want 0", tx.LastSeq())
		}
		return nil
	})
}
