package graph

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"testing"
)

func TestIgnoredGraphValidationFailureRollsBackCreation(t *testing.T) {
	s := memory.New()
	defer s.Close()
	err := s.Update(ctx, "s", func(tx store.Tx) error {
		item := taskItem("s", "fresh", tx.NextSeq(), domain.AuthorityUser)
		mustInsert(t, tx, item)
		_, _ = LinkDerived(tx, principal("s", domain.AuthorityUser), item.ID, []string{"missing"}, nil, "event")
		return nil
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("ignored validation failure committed", err)
	}
	err = s.View(ctx, "s", func(tx store.ReadTx) error {
		_, err := tx.Item("fresh")
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("creation survived failed graph operation", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
