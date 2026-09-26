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

func TestDuplicateCandidatesUseIndex(t *testing.T) {
	s, _ := openTemp(t)
	assertIndexed(t, s, duplicateSQL, "s", "h", "task", "", "", "USER", "TASK", "s", "", "task", "", 2)
}

// TestUpgradeDuplicateIndex checks that items stored before migration 0004
// (NULL role) are duplicate candidates after 0010.
func TestUpgradeDuplicateIndex(t *testing.T) {
	l := openLegacy(t, 3)
	it := storetest.NewItem("s", "legacy", 1, "same")
	l.insert("item", it, nil)
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		got, err := tx.DuplicateCandidates(store.DuplicateFilter{TaskID: it.TaskID, Section: it.Section, Role: domain.RoleSemantic,
			Authority: it.Authority, Access: it.Access, ContentHash: it.ContentHash, Limit: 1})
		if err != nil || len(got) != 1 || got[0].ID != "legacy" {
			t.Errorf("DuplicateCandidates after upgrade = %v, %v", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUnresolvedReferencesByKeyUseIndex(t *testing.T) {
	s, _ := openTemp(t)
	base := schemas["reference"].selectSQL + " WHERE session_id=? AND f_locator_key=?"
	assertIndexed(t, s, base+referenceOrderSQL, "s", "k", 2)
	assertIndexed(t, s, base+" AND f_rule_version=?"+referenceOrderSQL, "s", "k", "locator/v1", 2)
}

func TestItemsBySourceKeyUsesIndex(t *testing.T) {
	s, _ := openTemp(t)
	assertIndexed(t, s, sourceKeySQL, "s", domain.LocatorRuleVersion, "path:a", 2)
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
		got, err := tx.ItemsBySourceKey("path:docs/a.md", 2)
		if err != nil || len(got) != 2 || got[0].ID != "legacy-0" || got[1].ID != "new" {
			t.Errorf("ItemsBySourceKey after upgrade = %v, %v", got, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
