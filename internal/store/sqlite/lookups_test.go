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

func TestItemsByBlobUsesIndex(t *testing.T) {
	s, _ := openTemp(t)
	assertIndexed(t, s, "SELECT item_id FROM item_blobs WHERE session_id=? AND blob_hash=? LIMIT ?", "s", "h", 2)
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
		got, err := tx.ItemsByBlob(blob.Hash, 1)
		if err != nil || len(got) != 1 || got[0].ID != "legacy" {
			t.Errorf("ItemsByBlob after upgrade = %v, %v", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
