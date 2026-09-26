package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestInsertRelationshipRejectsCorruptEndpoint_SPEC44: an item whose text
// migration 0001 altered (its parts no longer match its content hash) is
// never accepted as a relationship endpoint, whichever side it is on;
// ErrIntegrity is returned and nothing is written. A missing endpoint is
// still ErrDanglingRelationship.
func TestInsertRelationshipRejectsCorruptEndpoint_SPEC44(t *testing.T) {
	l := openLegacy(t, 1)
	lossy := storetest.NewItem("s", "lossy", 1, "a\xffb")
	l.insert("item", lossy, map[string]any{"f_parts": legacyPartsJSON(t, lossy.Parts)})
	s := l.upgrade()
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		if err := tx.InsertItem(storetest.NewItem("s", "fresh", tx.NextSeq(), "fresh")); err != nil {
			return err
		}
		for _, c := range []struct {
			typ      domain.RelationshipType
			from, to string
		}{
			{domain.RelDerivedFrom, "fresh", "lossy"},
			{domain.RelSupersedes, "fresh", "lossy"},
			{domain.RelDuplicateOf, "fresh", "lossy"},
			{domain.RelDerivedFrom, "lossy", "fresh"},
			{domain.RelSupersedes, "lossy", "fresh"},
		} {
			id := string(c.typ) + "-" + c.from + "-" + c.to
			err := tx.InsertRelationship(storetest.NewRelationship("s", id, c.typ, c.from, c.to, tx.NextSeq()))
			if !errors.Is(err, domain.ErrIntegrity) {
				t.Errorf("%s %s -> %s: err = %v, want ErrIntegrity", c.typ, c.from, c.to, err)
			}
			if _, err := tx.Relationships(store.RelationshipFilter{Type: c.typ, FromID: c.from}); err != nil {
				return err
			}
		}
		rels, err := tx.Relationships(store.RelationshipFilter{})
		if err != nil || len(rels) != 0 {
			t.Errorf("relationships written: %d, %v", len(rels), err)
		}
		if err := tx.InsertRelationship(storetest.NewRelationship("s", "dangling", domain.RelSupersedes, "fresh", "missing", tx.NextSeq())); !errors.Is(err, domain.ErrDanglingRelationship) {
			t.Errorf("missing endpoint: %v, want ErrDanglingRelationship", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
