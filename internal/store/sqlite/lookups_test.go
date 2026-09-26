package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// assertIndexed fails when SQLite would answer q with a full table scan
// (R19: ingest lookups are never session-wide scans).
func assertIndexed(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	for _, step := range plan {
		if strings.HasPrefix(step, "SCAN ") && !strings.Contains(step, "USING") {
			t.Errorf("query scans a table: %q\nplan: %v", q, plan)
		}
	}
	if len(plan) == 0 || !strings.Contains(strings.Join(plan, " "), "USING") {
		t.Errorf("query uses no index: %q\nplan: %v", q, plan)
	}
}

// TestUpgradeItemBlobIndex checks that migration 0009 indexes items stored
// before it.
func TestUpgradeItemBlobIndex(t *testing.T) {
	l := openLegacy(t, 8)
	blob := storetest.NewBlob("s", []byte("legacy pdf"))
	if _, err := l.db.Exec("INSERT INTO sessions(session_id,last_seq,committed) VALUES('s',100,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("INSERT INTO blobs(session_id,hash,media_type,data,data_nil) VALUES(?,?,?,?,0)", "s", blob.Hash, blob.MediaType, blob.Data); err != nil {
		t.Fatal(err)
	}
	it := storetest.NewItem("s", "legacy", 1, "caption")
	it.Parts = append(it.Parts,
		domain.ContentPart{Type: domain.PartDocument, MediaType: "application/pdf", BlobHash: blob.Hash, BlobSize: uint64(len(blob.Data))},
		domain.ContentPart{Type: domain.PartDocument, MediaType: "application/pdf", BlobHash: blob.Hash, BlobSize: uint64(len(blob.Data))})
	it.ContentHash, it.SemanticBytes = domain.ContentHash(it.Parts), domain.SemanticBytes(it.Parts)
	l.insert("item", it, nil)
	l.insert("item", storetest.NewItem("s", "text-only", 2, "no blob"), nil)
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		got, err := tx.BlobReferrer(store.BlobReferrerFilter{Viewer: planViewer, BlobHash: blob.Hash, Within: it.Access})
		if err != nil || len(got.Items) != 1 || got.Items[0].ID != "legacy" {
			t.Errorf("BlobReferrer after upgrade (0009 then 0012) = %+v, %v", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeDuplicateIndex checks that items stored before migration 0004
// (NULL role) are duplicate candidates after 0010.
func TestUpgradeDuplicateIndex(t *testing.T) {
	l := openLegacy(t, 3)
	it := storetest.NewItem("s", "legacy", 1, "same")
	l.insert("item", it, nil)
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		got, err := tx.CanonicalCandidates(store.CanonicalFilter{Viewer: planViewer, TaskID: it.TaskID, Section: it.Section, Kind: it.Kind,
			Role: domain.RoleSemantic, Authority: it.Authority, Access: it.Access, ContentHash: it.ContentHash, Limit: 1})
		if err != nil || len(got.Items) != 1 || got.Items[0].ID != "legacy" {
			t.Errorf("CanonicalCandidates after upgrade (0010 then 0012) = %+v, %v", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestUpgradeItemSourceIndex checks that migration 0011's Go step indexes
// items stored before it, under the same keys InsertItem uses.
func TestUpgradeItemSourceIndex(t *testing.T) {
	l := openLegacy(t, 10)
	for i, src := range []*domain.SourceRef{
		{Kind: domain.SourcePath, Locator: "./docs//a.md"},
		{Kind: domain.SourcePath, Locator: "/abs"},
		{Kind: domain.SourceTool, Locator: "docs/a.md"},
		nil,
	} {
		it := storetest.NewItem("s", "legacy-"+string(rune('0'+i)), uint64(i+1), "x")
		it.Source = src
		l.insert("item", it, nil)
	}
	s := l.upgrade()
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		it := storetest.NewItem("s", "new", tx.NextSeq(), "y")
		it.Source = &domain.SourceRef{Kind: domain.SourcePath, Locator: "docs/a.md"}
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		got, err := tx.SourceItems(store.SourceFilter{Viewer: planViewer, LocatorKey: "path:docs/a.md", Page: store.Page{Limit: 2}})
		if err != nil || len(got.Items) != 2 || got.Items[0].ID != "legacy-0" || got.Items[1].ID != "new" {
			t.Errorf("SourceItems after upgrade (0011 step then 0012) = %+v, %v", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestLegacyLookupsDropped checks that the tables and indexes of the
// deleted pre-F1 lookups are gone after every migration, so no write keeps
// maintaining them.
func TestLegacyLookupsDropped(t *testing.T) {
	s, _ := openTemp(t)
	for _, name := range []string{"item_blobs", "item_sources", "item_duplicate", "reference_locator"} {
		var n int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name=?", name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s still exists after migrations", name)
		}
	}
}
