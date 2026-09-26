package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

var planViewer = domain.Principal{SessionID: "s", WorkflowID: "wf", TaskID: "task", AgentID: "agent", Authority: domain.AuthorityUser}

// TestAccessLookupsUseIndex locks every F1 lookup to an index probe: no
// lookup scans a table, whatever the session holds (SEC-1.1, SEC-1.2,
// SPEC-1.3).
func TestAccessLookupsUseIndex(t *testing.T) {
	s, _ := openTemp(t)
	clause, args, _ := ownerClause("workflow_id", "task_id", "agent_id", planViewer)
	assertIndexed(t, s, []string{"session_id", "blob_hash", "workflow_id", "task_id", "agent_id"}, "SELECT item_id FROM lookup_blob WHERE session_id=? AND blob_hash=? AND "+clause+" ORDER BY seq, item_id",
		append([]any{"s", "h"}, args...)...)
	assertIndexed(t, s, []string{"session_id", "content_hash", "task_id", "section", "directive_id", "kind", "role", "authority", "scope", "access_session_id", "workflow_id", "access_task_id", "agent_id"}, canonicalSQL, "s", "h", "task", "", "", "fact", "", "USER", "TASK", "s", "", "task", "")
	assertIndexed(t, s, []string{"session_id", "task_id", "authority", "scope", "access_session_id", "workflow_id", "access_task_id", "agent_id"}, workingSQL, "s", "task", "USER", "TASK", "s", "", "task", "")
	assertIndexed(t, s, []string{"session_id", "rule_version", "locator_key", "workflow_id", "task_id", "agent_id"}, "SELECT item_id FROM lookup_source WHERE session_id=? AND rule_version=? AND locator_key=? AND "+clause+
		" AND (seq > ? OR seq = ? AND item_id > ?) ORDER BY seq, item_id", append(append([]any{"s", "v", "k"}, args...), 0, 0, "")...)
	rclause, rargs, _ := ownerClause("f_access_workflow_id", "f_access_task_id", "f_access_agent_id", planViewer)
	assertIndexed(t, s, []string{"session_id", "f_locator_key", "f_rule_version", "f_access_workflow_id", "f_access_task_id", "f_access_agent_id"}, "SELECT id FROM rec_reference WHERE session_id=? AND f_locator_key=? AND f_rule_version=? AND "+rclause+
		" AND (f_seq > ? OR f_seq = ? AND id > ?) ORDER BY f_seq, id LIMIT ?", append(append([]any{"s", "k", "v"}, rargs...), 0, 0, "", 2)...)
}

// TestUpgradeAccessLookups checks migration 0012's backfill: live items are
// indexed with their owners, retired ones are not.
func TestUpgradeAccessLookups(t *testing.T) {
	l := openLegacy(t, 11)
	blob := storetest.NewBlob("s", []byte("legacy pdf"))
	if _, err := l.db.Exec("INSERT INTO sessions(session_id,last_seq,committed) VALUES('s',100,1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.db.Exec("INSERT INTO blobs(session_id,hash,media_type,data,data_nil) VALUES(?,?,?,?,0)", "s", blob.Hash, blob.MediaType, blob.Data); err != nil {
		t.Fatal(err)
	}
	canonical := storetest.NewItem("s", "canonical", 1, "ok")
	dup := storetest.NewItem("s", "dup", 2, "ok")
	withBlob := storetest.NewItem("s", "with-blob", 3, "caption")
	withBlob.Parts = append(withBlob.Parts, domain.ContentPart{Type: domain.PartDocument, MediaType: "application/pdf", BlobHash: blob.Hash, BlobSize: uint64(len(blob.Data))})
	withBlob.ContentHash, withBlob.SemanticBytes = domain.ContentHash(withBlob.Parts), domain.SemanticBytes(withBlob.Parts)
	sourced := storetest.NewItem("s", "sourced", 4, "file")
	sourced.Source = &domain.SourceRef{Kind: domain.SourcePath, Locator: "src/main.go"}
	for _, it := range []domain.ContextItem{canonical, dup, withBlob, sourced} {
		l.insert("item", it, nil)
	}
	l.insert("relationship", storetest.NewRelationship("s", "dup-edge", domain.RelDuplicateOf, "dup", "canonical", 5), nil)
	for _, q := range []string{
		"INSERT INTO item_blobs(session_id,blob_hash,item_id) VALUES('s','" + blob.Hash + "','with-blob')",
		"INSERT INTO item_sources(session_id,rule_version,locator_key,item_id) VALUES('s','reference-locator/v1','path:src/main.go','sourced')",
	} {
		if _, err := l.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		c, err := tx.CanonicalCandidates(store.CanonicalFilter{Viewer: planViewer, TaskID: "task", Kind: canonical.Kind, Authority: canonical.Authority,
			Access: canonical.Access, ContentHash: canonical.ContentHash, Limit: 1})
		if err != nil || len(c.Items) != 1 || c.Items[0].ID != "canonical" {
			t.Errorf("canonical after upgrade = %+v, %v", c, err)
		}
		b, err := tx.BlobReferrer(store.BlobReferrerFilter{Viewer: planViewer, BlobHash: blob.Hash, Within: withBlob.Access})
		if err != nil || len(b.Items) != 1 || b.Items[0].ID != "with-blob" {
			t.Errorf("blob referrer after upgrade = %+v, %v", b, err)
		}
		src, err := tx.SourceItems(store.SourceFilter{Viewer: planViewer, LocatorKey: "path:src/main.go", Page: store.Page{Limit: 1}})
		if err != nil || len(src.Items) != 1 || src.Items[0].ID != "sourced" {
			t.Errorf("sources after upgrade = %+v, %v", src, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestLegacyUnverifiedNeverBlocks is DUR-1.4: a legacy row 0001 altered is
// reported as unverified and excluded from candidates, identical new
// content still finds its own canonical item, and reading the row directly
// still fails with ErrIntegrity.
func TestLegacyUnverifiedNeverBlocks(t *testing.T) {
	l := openLegacy(t, 1)
	lossy := storetest.NewItem("s", "lossy", 1, "a\xffb")
	l.insert("item", lossy, map[string]any{"f_parts": legacyPartsJSON(t, lossy.Parts)})
	s := l.upgrade()
	filter := store.CanonicalFilter{Viewer: planViewer, TaskID: "task", Kind: lossy.Kind, Authority: lossy.Authority,
		Access: lossy.Access, ContentHash: lossy.ContentHash, Limit: 1}
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		c, err := tx.CanonicalCandidates(filter)
		if err != nil || len(c.Items) != 0 || len(c.Unverified) != 1 || c.Unverified[0] != "lossy" {
			t.Errorf("before new content: %+v, %v", c, err)
		}
		fresh := storetest.NewItem("s", "fresh", tx.NextSeq(), "a\xffb")
		if err := tx.InsertItem(fresh); err != nil {
			return err
		}
		c, err = tx.CanonicalCandidates(filter)
		if err != nil || len(c.Items) != 1 || c.Items[0].ID != "fresh" || len(c.Unverified) != 1 {
			t.Errorf("after new content: %+v, %v", c, err)
		}
		if _, err := tx.Item("lossy"); !errors.Is(err, domain.ErrIntegrity) {
			t.Errorf("direct read = %v, want ErrIntegrity", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestGraphReadsUseIndex locks the reads ingest and graph issue per item
// to exact-key index searches through the production query builders
// (SPEC-1.3, SPEC-2.1): relationships by (type, source) and (type, target)
// and items by task, each still ordered by (Seq, ID).
func TestGraphReadsUseIndex(t *testing.T) {
	s, _ := openTemp(t)
	for _, c := range []struct {
		f    store.RelationshipFilter
		keys []string
	}{
		{store.RelationshipFilter{Type: domain.RelSupersedes, ToID: "x"}, []string{"session_id", "f_type", "f_to_id"}},
		{store.RelationshipFilter{Type: domain.RelDuplicateOf, FromID: "x"}, []string{"session_id", "f_type", "f_from_id"}},
		{store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: "x"}, []string{"session_id", "f_type", "f_from_id"}},
	} {
		q, args := relationshipQuery("s", c.f)
		assertIndexed(t, s, c.keys, q, args...)
	}
	q, args := itemQuery("s", store.ItemFilter{TaskID: "task"})
	assertIndexed(t, s, []string{"session_id", "f_task_id"}, q, args...)
}
