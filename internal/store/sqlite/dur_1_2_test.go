package sqlite

import (
	"context"
	"errors"
	"sync"
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

// commitEdgeContext closes Done immediately after Update's last pre-commit
// Err check returns nil. The following SQL must use WithoutCancel: using the
// caller context there reports cancellation instead of committing.
type commitEdgeContext struct {
	context.Context
	mu        sync.Mutex
	done      chan struct{}
	armed     bool
	cancelled bool
}

func newCommitEdgeContext() *commitEdgeContext {
	return &commitEdgeContext{Context: context.Background(), done: make(chan struct{})}
}
func (c *commitEdgeContext) Done() <-chan struct{} { return c.done }
func (c *commitEdgeContext) arm() {
	c.mu.Lock()
	c.armed = true
	c.mu.Unlock()
}
func (c *commitEdgeContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancelled {
		return context.Canceled
	}
	if c.armed {
		c.cancelled = true
		close(c.done)
		return nil
	}
	return nil
}

func TestCancellationAtCommitBoundaryReportsCommitted(t *testing.T) {
	s, _ := openTemp(t)
	ctx := newCommitEdgeContext()
	err := s.Update(ctx, "edge", func(tx store.Tx) error {
		if err := tx.InsertItem(reviewItem("edge", "i", tx.NextSeq())); err != nil {
			return err
		}
		ctx.arm()
		return nil
	})
	if err != nil {
		t.Fatalf("Update at commit boundary = %v", err)
	}
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatal("test context did not cancel at commit boundary")
	}
	if err := s.View(context.Background(), "edge", func(tx store.ReadTx) error {
		if _, err := tx.Item("i"); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("committed item: %v", err)
	}
}
