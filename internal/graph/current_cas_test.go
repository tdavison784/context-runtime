package graph

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
	"testing"
)

// The adapter exercises graph CAS arguments against real legacy memory writes;
// it is not a replacement for W2's backend structural/atomicity conformance tests.
type currentCASTx struct {
	store.Tx
	backend *currentCASBackend
}
type currentCASBackend struct {
	store.SemanticTx
	tx       store.Tx
	expected []string
	conflict bool
}

func (t *currentCASTx) SemanticTransaction() (store.SemanticTx, error) { return t.backend, nil }
func (b *currentCASBackend) SetCurrentVersion(id, expected string) error {
	b.expected = append(b.expected, expected)
	item, err := b.tx.Item(id)
	if err != nil {
		return err
	}
	key, _ := item.CurrentKey()
	actual, err := b.tx.CurrentVersion(key)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if b.conflict || actual != expected {
		return domain.ErrVersionConflict
	}
	return storetest.UncheckedSetCurrentVersion(b.tx, id)
}

func TestDirectiveFilingPassesExpectedPriorAndRollsBackCASFailure(t *testing.T) {
	s := memory.New()
	defer s.Close()
	actor := principal("s", domain.AuthorityUser)
	for _, step := range []struct {
		id, expected string
		conflict     bool
	}{{"first", "", false}, {"second", "first", false}, {"rejected", "second", true}} {
		err := s.Update(ctx, "s", func(tx store.Tx) error {
			item := newDirective("s", step.id, "key", tx.NextSeq(), step.id)
			item.Namespace = domain.NamespaceDirective
			mustInsert(t, tx, item)
			backend := &currentCASBackend{tx: tx, conflict: step.conflict}
			wrapped := &currentCASTx{Tx: tx, backend: backend}
			prior, err := ReplaceDirective(wrapped, actor, "task", "key", item.ID, "event-"+step.id)
			if len(backend.expected) != 1 || backend.expected[0] != step.expected {
				t.Fatalf("CAS expectation = %v, want %q", backend.expected, step.expected)
			}
			if step.conflict {
				return nil
			} // ignored error must still poison all edges/items
			if err == nil && prior != step.expected {
				t.Fatalf("prior = %q", prior)
			}
			return err
		})
		if step.conflict {
			if !errors.Is(err, domain.ErrVersionConflict) {
				t.Fatal("ignored CAS failure committed", err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		if _, err := tx.Item("rejected"); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("CAS failure kept occurrence", err)
		}
		if current, err := IsCurrent(tx, "second"); err != nil || !current {
			t.Fatal("CAS failure retired prior", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
