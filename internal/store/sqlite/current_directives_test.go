package sqlite

import (
	"context"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestCurrentDirectivesAcrossBoundaries(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		for _, spec := range []struct {
			id       string
			boundary domain.AccessBoundary
		}{
			{"z", domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}},
			{"a", domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: "s"}},
		} {
			item := reviewItem("s", spec.id, tx.NextSeq())
			item.TaskID, item.DirectiveID, item.Section = "task", "directive", domain.SectionPinned
			item.Scope, item.Access = spec.boundary.Scope, spec.boundary
			if err := tx.InsertItem(item); err != nil {
				return err
			}
			if err := tx.SetCurrentVersion(spec.id); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		ids, err := tx.CurrentVersions("task", domain.NamespaceDirective, "directive")
		if err != nil {
			return err
		}
		if !slices.Equal(ids, []string{"a", "z"}) {
			t.Fatalf("CurrentDirectives = %v", ids)
		}
		ids, err = tx.CurrentVersions("task", domain.NamespaceDirective, "absent")
		if err != nil || len(ids) != 0 {
			t.Fatalf("absent = %v, %v", ids, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
