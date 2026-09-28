package storetest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

var (
	errPoisonA = errors.New("storetest: poison A")
	errPoisonB = errors.New("storetest: poison B")
)

// testPoisonRollsBack checks Tx.Poison (DUR-1.3): once poisoned, Update
// returns the poison error even if fn returns nil, and nothing the
// transaction wrote before or after is committed.
func testPoisonRollsBack(t *testing.T, s store.Store) {
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem(sessA, "before", tx.NextSeq(), "x")))
		tx.Poison(errPoisonA)
		// Reads still work, including the transaction's own writes.
		_, err := tx.Item("before")
		noErr(t, err)
		return nil
	})
	wantErr(t, err, errPoisonA)
	view(t, s, sessA, func(tx store.ReadTx) error {
		_, err := tx.Item("before")
		wantErr(t, err, domain.ErrNotFound)
		if tx.LastSeq() != 0 {
			t.Errorf("LastSeq = %d after a poisoned transaction, want 0", tx.LastSeq())
		}
		return nil
	})
	sessions, err := s.Sessions(ctx)
	noErr(t, err)
	if len(sessions) != 0 {
		t.Errorf("Sessions = %v after a poisoned transaction, want none", sessions)
	}
	// The next transaction is unaffected.
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertItem(NewItem(sessA, "after", tx.NextSeq(), "y"))
	})
}

// testPoisonFirstErrorWins checks that Poison is idempotent and keeps its
// first error, that an error fn returns after poisoning does not replace
// it, and that Poison(nil) poisons with store.ErrPoisoned.
func testPoisonFirstErrorWins(t *testing.T, s store.Store) {
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.Poison(errPoisonA)
		tx.Poison(errPoisonA)
		tx.Poison(errPoisonB)
		wantErr(t, tx.InsertItem(NewItem(sessA, "x", tx.NextSeq(), "x")), errPoisonA)
		return errPoisonB
	})
	wantErr(t, err, errPoisonA)
	if errors.Is(err, errPoisonB) {
		t.Errorf("Update error %v carries the later error", err)
	}
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.Poison(nil)
		return nil
	})
	wantErr(t, err, store.ErrPoisoned)
}

// testPoisonBlocksEveryWrite calls every write method of store.Tx on a
// poisoned transaction and requires the poison error, so a write method
// added later cannot bypass Poison.
func testPoisonBlocksEveryWrite(t *testing.T, s store.Store) {
	reads := map[string]bool{"NextSeq": true, "Allocated": true, "Poison": true}
	rt := reflect.TypeFor[store.ReadTx]()
	for i := range rt.NumMethod() {
		reads[rt.Method(i).Name] = true
	}
	txType := reflect.TypeFor[store.Tx]()
	checked := 0
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tx.Poison(errPoisonA)
		v := reflect.ValueOf(tx)
		for i := range txType.NumMethod() {
			m := txType.Method(i)
			if reads[m.Name] {
				continue
			}
			args := make([]reflect.Value, m.Type.NumIn())
			for j := range args {
				args[j] = reflect.Zero(m.Type.In(j))
			}
			out := v.MethodByName(m.Name).Call(args)
			last := out[len(out)-1].Interface()
			if err, _ := last.(error); !errors.Is(err, errPoisonA) {
				t.Errorf("%s on a poisoned transaction: error = %v, want the poison error", m.Name, last)
			}
			checked++
		}
		return nil
	})
	wantErr(t, err, errPoisonA)
	// A sanity floor, not the exact count; SPEC-1.21 removed the unchecked
	// SetCurrentVersion(itemID) from store.Tx.
	if checked < 19 {
		t.Errorf("checked %d write methods, want every one (at least 19)", checked)
	}
}
