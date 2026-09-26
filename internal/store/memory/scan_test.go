package memory

import (
	"context"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestKeyedReadsDoNotScan checks that CurrentVersions and
// ObligationsBySource read only their key's index entry (SPEC-2.1): with
// hundreds of unrelated pointers and obligations in the session, neither
// iterates a whole table.
func TestKeyedReadsDoNotScan(t *testing.T) {
	s := New()
	defer s.Close()
	ctx := context.Background()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		for i := range 300 {
			id := fmt.Sprintf("d%d", i)
			if err := tx.InsertItem(storetest.NewDirective("s", id, id, tx.NextSeq(), id)); err != nil {
				return err
			}
			if err := tx.SetCurrentVersion(id); err != nil {
				return err
			}
			if err := tx.InsertObligationVersion(storetest.NewObligation("s", "o"+id, 1, tx.NextSeq(), id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, "s", func(stx store.Tx) error {
		inner := stx.(*store.Guard).TxBase.(*tx)
		inner.directives.scanned, inner.obligations.scanned = 0, 0
		ids, err := stx.CurrentVersions("task", domain.NamespaceDirective, "d7")
		if err != nil || len(ids) != 1 || ids[0] != "d7" {
			t.Errorf("CurrentVersions = %v, %v", ids, err)
		}
		obls, err := stx.ObligationsBySource("d7", 2)
		if err != nil || len(obls) != 1 || obls[0].ObligationID != "od7" {
			t.Errorf("ObligationsBySource = %v, %v", obls, err)
		}
		if inner.directives.scanned != 0 || inner.obligations.scanned != 0 {
			t.Errorf("scanned %d pointers and %d obligations, want 0", inner.directives.scanned, inner.obligations.scanned)
		}
		// Graph reads per item (SPEC-1.3, SPEC-2.1): relationships by type
		// and endpoint, and items by task, read their indexes only.
		inner.rels.scanned, inner.items.scanned = 0, 0
		for _, f := range []store.RelationshipFilter{
			{Type: domain.RelSupersedes, ToID: "d7"},
			{Type: domain.RelDuplicateOf, FromID: "d7"},
		} {
			if _, err := stx.Relationships(f); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := stx.Items(store.ItemFilter{TaskID: "task"}); err != nil {
			t.Fatal(err)
		}
		if inner.rels.scanned != 0 || inner.items.scanned != 0 {
			t.Errorf("scanned %d relationships and %d items, want 0", inner.rels.scanned, inner.items.scanned)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestRelationshipsReadTheirTypedKey checks SPEC-3.1 item 5: a
// relationship read by (type, endpoint) yields only that type's edges from
// the index, however many edges of other types share the endpoint (the
// dedup path reads SUPERSEDES into an item with thousands of DUPLICATE_OF
// edges).
func TestRelationshipsReadTheirTypedKey(t *testing.T) {
	s := New()
	defer s.Close()
	ctx := context.Background()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		if err := tx.InsertItem(storetest.NewItem("s", "c", tx.NextSeq(), "c")); err != nil {
			return err
		}
		// Unrelated SUPERSEDES edges, so a type-only index would yield
		// entries the (type, endpoint) key excludes (SPEC-4.3).
		for i := range 200 {
			x, y := fmt.Sprintf("x%d", i), fmt.Sprintf("y%d", i)
			for _, id := range []string{x, y} {
				if err := tx.InsertItem(storetest.NewItem("s", id, tx.NextSeq(), id)); err != nil {
					return err
				}
			}
			if err := tx.InsertRelationship(storetest.NewRelationship("s", "s"+x, domain.RelSupersedes, x, y, tx.NextSeq())); err != nil {
				return err
			}
		}
		for i := range 1000 {
			id := fmt.Sprintf("d%d", i)
			if err := tx.InsertItem(storetest.NewItem("s", id, tx.NextSeq(), "c")); err != nil {
				return err
			}
			if err := tx.InsertRelationship(storetest.NewRelationship("s", "e"+id, domain.RelDuplicateOf, id, "c", tx.NextSeq())); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.View(ctx, "s", func(rtx store.ReadTx) error {
		r := rtx.(*readTx)
		for _, f := range []store.RelationshipFilter{
			{Type: domain.RelSupersedes, ToID: "c"},
			{Type: domain.RelSupersedes, FromID: "d7"},
		} {
			r.rels.scanned = 0
			// Every relationship index counts, so neither a full scan nor a
			// type-only index can stand in for (type, endpoint) (SPEC-3.2).
			yieldsBefore := r.relsTo.yields + r.relsFrom.yields + r.relsByType.yields
			rels, err := rtx.Relationships(f)
			if err != nil || len(rels) != 0 {
				t.Errorf("Relationships(%+v) = %v, %v", f, rels, err)
			}
			if n := r.relsTo.yields + r.relsFrom.yields + r.relsByType.yields - yieldsBefore; n > 1 || r.rels.scanned != 0 {
				t.Errorf("Relationships(%+v) walked %d index entries and scanned %d edges", f, n, r.rels.scanned)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUntypedEndpointReadsAllRelationshipTypes_SPEC31 ranges over the domain
// set, so adding a valid type without probing its endpoint key fails here.
func TestUntypedEndpointReadsAllRelationshipTypes_SPEC31(t *testing.T) {
	s := New()
	defer s.Close()
	ctx := context.Background()
	types := domain.RelationshipTypes()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		for _, id := range []string{"from", "to"} {
			if err := tx.InsertItem(storetest.NewItem("s", id, tx.NextSeq(), id)); err != nil {
				return err
			}
		}
		for _, typ := range types {
			if err := tx.InsertRelationship(storetest.NewRelationship("s", string(typ), typ, "from", "to", tx.NextSeq())); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		for _, f := range []store.RelationshipFilter{{FromID: "from"}, {ToID: "to"}} {
			rels, err := tx.Relationships(f)
			if err != nil {
				return err
			}
			seen := map[domain.RelationshipType]bool{}
			for _, rel := range rels {
				seen[rel.Type] = true
			}
			if len(rels) != len(types) {
				t.Errorf("Relationships(%+v) returned %d edges, want %d", f, len(rels), len(types))
			}
			for _, typ := range types {
				if !seen[typ] {
					t.Errorf("Relationships(%+v) missed %s", f, typ)
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
