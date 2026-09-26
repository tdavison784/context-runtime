package sqlite

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestHotReadsUseTheirBuilders checks that the reads graph and ingest issue
// per item run exactly the plan-guarded builder queries (SPEC-2.1), so
// reverting one to a session-wide read fails here as well as in the plan
// test.
func TestHotReadsUseTheirBuilders(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.View(context.Background(), "s", func(rtx store.ReadTx) error {
		tx := rtx.(*transaction)
		for _, f := range []store.RelationshipFilter{
			{Type: domain.RelSupersedes, ToID: "x"},
			{Type: domain.RelDuplicateOf, FromID: "x"},
		} {
			if _, err := tx.Relationships(f); err != nil {
				return err
			}
			if want, _ := relationshipQuery("s", f); tx.lastQuery != want {
				t.Errorf("Relationships(%+v) ran %q", f, tx.lastQuery)
			}
		}
		if _, err := tx.Items(store.ItemFilter{TaskID: "task"}); err != nil {
			return err
		}
		if want, _ := itemQuery("s", store.ItemFilter{TaskID: "task"}); tx.lastQuery != want {
			t.Errorf("Items(task) ran %q", tx.lastQuery)
		}
		if _, err := tx.ObligationVersions("o"); err != nil {
			return err
		}
		if want, _ := obligationVersionsQuery("s", "o"); tx.lastQuery != want {
			t.Errorf("ObligationVersions ran %q", tx.lastQuery)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
