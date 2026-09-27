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
	ctx := context.Background()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		return tx.InsertItem(storetest.NewItem("s", "fresh", tx.NextSeq(), "fresh"))
	}); err != nil {
		t.Fatal(err)
	}
	// Each rejected edge is probed in its own transaction: after a
	// successful write a rejection poisons the transaction (P3-1).
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
		err := s.Update(ctx, "s", func(tx store.Tx) error {
			return tx.InsertRelationship(storetest.NewRelationship("s", id, c.typ, c.from, c.to, tx.NextSeq()))
		})
		if !errors.Is(err, domain.ErrIntegrity) {
			t.Errorf("%s %s -> %s: err = %v, want ErrIntegrity", c.typ, c.from, c.to, err)
		}
	}
	err := s.Update(ctx, "s", func(tx store.Tx) error {
		return tx.InsertRelationship(storetest.NewRelationship("s", "dangling", domain.RelSupersedes, "fresh", "missing", tx.NextSeq()))
	})
	if !errors.Is(err, domain.ErrDanglingRelationship) {
		t.Errorf("missing endpoint: %v, want ErrDanglingRelationship", err)
	}
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		rels, err := tx.Relationships(store.RelationshipFilter{})
		if err != nil || len(rels) != 0 {
			t.Errorf("relationships written: %d, %v", len(rels), err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
