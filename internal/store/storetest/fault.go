package storetest

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/store"
)

// ErrInjected is the failure FaultStore injects into a write.
var ErrInjected = errors.New("storetest: injected write failure")

// FaultStore wraps a store and fails the FailAt-th write (counting from 1,
// legacy Tx and semantic facet writes alike) of each Update with
// ErrInjected before it reaches the store; FailAt 0 injects nothing. Writes
// is the number of writes the most recent Update attempted. An injected
// failure after an earlier write poisons the transaction, as the Guard does
// for a real failure (P3-1). A FaultStore is for one goroutine.
type FaultStore struct {
	store.Store
	FailAt int
	Writes int
}

// Update implements store.Store.
func (f *FaultStore) Update(ctx context.Context, sessionID string, fn func(store.Tx) error) error {
	ft := &faultTx{failAt: f.FailAt}
	err := f.Store.Update(ctx, sessionID, func(tx store.Tx) error {
		ft.Tx = tx
		return fn(ft)
	})
	f.Writes = ft.writes
	return err
}

// faultTx intercepts every write of store.Tx (fault_writes.go) and hands
// out a semantic facet that counts against the same write sequence.
type faultTx struct {
	store.Tx
	failAt, writes int
}

func (f *faultTx) hit(method string) error {
	f.writes++
	if f.writes != f.failAt {
		return nil
	}
	if f.writes > 1 {
		f.Tx.Poison(ErrInjected)
	}
	return fmt.Errorf("%s: %w", method, ErrInjected)
}

// SemanticTransaction implements store.SemanticTransactionProvider.
func (f *faultTx) SemanticTransaction() (store.SemanticTx, error) {
	sem, err := store.Semantic(f.Tx)
	if err != nil {
		return nil, err
	}
	return &faultSemantic{SemanticTx: sem, parent: f}, nil
}

// SemanticReadBackend implements store.SemanticReadProvider, so reads
// through store.ReadSemantic keep working inside a FaultStore Update.
func (f *faultTx) SemanticReadBackend() store.SemanticReader {
	r, err := store.ReadSemantic(f.Tx)
	if err != nil {
		return nil
	}
	return r
}

// faultSemantic intercepts every semantic facet write.
type faultSemantic struct {
	store.SemanticTx
	parent *faultTx
}

func (f *faultSemantic) hit(method string) error { return f.parent.hit(method) }

// AtomicCase is a multi-write operation whose effects must be all or
// nothing (P3-1): Setup commits the preconditions on a fresh store, Op runs
// the writes in one Update and returns the first error, and Snapshot reads
// the state a failed attempt must leave unchanged.
type AtomicCase struct {
	Session  string
	Setup    func(t *testing.T, s store.Store)
	Op       func(t *testing.T, tx store.Tx) error
	Snapshot func(t *testing.T, tx store.ReadTx) any
}

// CheckAtomic runs c once cleanly to count its writes, then once per write
// on a fresh store with that write failing, and requires each failed
// attempt to report ErrInjected and leave Snapshot exactly as before.
func CheckAtomic(t *testing.T, newStore func(t *testing.T) store.Store, c AtomicCase) {
	t.Helper()
	fresh := func() store.Store {
		s := newStore(t)
		t.Cleanup(func() { _ = s.Close() })
		c.Setup(t, s)
		return s
	}
	clean := &FaultStore{Store: fresh()}
	if err := clean.Update(ctx, c.Session, func(tx store.Tx) error { return c.Op(t, tx) }); err != nil {
		t.Fatalf("clean run: %v", err)
	}
	if clean.Writes < 2 {
		t.Fatalf("clean run attempted %d writes; an atomicity case needs several", clean.Writes)
	}
	snapshot := func(s store.Store) any {
		var v any
		if err := s.View(ctx, c.Session, func(tx store.ReadTx) error { v = c.Snapshot(t, tx); return nil }); err != nil {
			t.Fatal(err)
		}
		return v
	}
	for k := 1; k <= clean.Writes; k++ {
		s := fresh()
		before := snapshot(s)
		fs := &FaultStore{Store: s, FailAt: k}
		err := fs.Update(ctx, c.Session, func(tx store.Tx) error { return c.Op(t, tx) })
		if !errors.Is(err, ErrInjected) {
			t.Errorf("failing write %d of %d: error = %v, want ErrInjected", k, clean.Writes, err)
		}
		if after := snapshot(s); !reflect.DeepEqual(before, after) {
			t.Errorf("failing write %d of %d left partial state:\n before %+v\n after  %+v", k, clean.Writes, before, after)
		}
	}
}
