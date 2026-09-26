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

// The behaviors below go beyond the store contract; they match the SQLite
// store so both enforce the same invariants.

func TestCallReservation(t *testing.T) {
	s := memory.New()
	err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		c1 := storetest.NewCall("s", "c1", "conv", tx.NextSeq())
		if err := tx.InsertCall(c1); err != nil {
			return err
		}
		c2 := storetest.NewCall("s", "c2", "conv", tx.NextSeq())
		if err := tx.InsertCall(c2); !errors.Is(err, domain.ErrCallInFlight) {
			t.Errorf("second reserving InsertCall: %v, want ErrCallInFlight", err)
		}
		c2.State, c2.FinishedSeq = domain.CallFailed, c2.PreparedSeq
		if err := tx.InsertCall(c2); err != nil {
			return err
		}
		// Releasing c1 lets another call reserve the conversation.
		done := c1.Clone()
		done.State, done.FinishedSeq, done.Revision = domain.CallFailed, tx.NextSeq(), 2
		if err := tx.UpdateCall(done, 1); err != nil {
			return err
		}
		return tx.InsertCall(storetest.NewCall("s", "c3", "conv", tx.NextSeq()))
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestConversationOwnersImmutable(t *testing.T) {
	s := memory.New()
	err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		c := storetest.NewConversation("s", "conv")
		if err := tx.PutConversation(c, 0); err != nil {
			return err
		}
		c.Revision, c.AgentID = 2, "other"
		if err := tx.PutConversation(c, 1); !errors.Is(err, domain.ErrImmutable) {
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
