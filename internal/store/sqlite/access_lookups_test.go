package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

var planViewer = domain.Principal{SessionID: "s", WorkflowID: "wf", TaskID: "task", AgentID: "agent", Authority: domain.AuthorityUser}

// TestAccessLookupsUseIndex locks every F1 lookup, through its production
// query builder, to an exact-key index search with a LIMIT (SPEC-2.1,
// DUR-2.1): no lookup scans or reads more than one batch per query,
// whatever the session holds.
func TestAccessLookupsUseIndex(t *testing.T) {
	s, _ := openTemp(t)
	mid := store.Cursor{Seq: 7, ID: "m"}
	check := func(keys []string, q string, args []any) {
		t.Helper()
		if !strings.Contains(q, "LIMIT ?") {
			t.Errorf("query has no LIMIT: %q", q)
		}
		assertIndexed(t, s, keys, q, args...)
	}
	owners := []string{"workflow_id", "task_id", "agent_id"}
	blob, _ := blobReferrerQuery("s", "h", planViewer, planViewer)
	q, args := blob(mid, lookupBatch)
	check(append([]string{"session_id", "blob_hash"}, owners...), q, args)
	q, args = canonicalQuery("s", store.CanonicalFilter{TaskID: "task", Kind: domain.KindFact, Authority: domain.AuthorityUser,
		Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}, ContentHash: "h"})(mid, lookupBatch)
	check([]string{"session_id", "content_hash", "task_id", "section", "directive_id", "kind", "role", "authority", "scope", "access_session_id", "workflow_id", "access_task_id", "agent_id"}, q, args)
	q, args = workingQuery("s", store.WorkingFilter{TaskID: "task", Authority: domain.AuthorityUser,
		Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}})(mid, lookupBatch)
	check([]string{"session_id", "task_id", "authority", "scope", "access_session_id", "workflow_id", "access_task_id", "agent_id"}, q, args)
	q, args = sourceItemsQuery("s", "k", planViewer)(mid, lookupBatch)
	check(append([]string{"session_id", "rule_version", "locator_key"}, owners...), q, args)
	q, args = visibleReferencesQuery("s", "k", planViewer, mid, 3)
	check([]string{"session_id", "f_locator_key", "f_rule_version", "f_access_workflow_id", "f_access_task_id", "f_access_agent_id"}, q, args)
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

// TestObligationReadsUseIndex locks obligation reads to their keys
// (SPEC-2.1): versions of one obligation by primary key, versions bound to
// a source by the 0006 index. Ingest reads the latest version once per
// declared obligation, so a session-wide read made one event quadratic.
func TestObligationReadsUseIndex(t *testing.T) {
	s, _ := openTemp(t)
	q, args := obligationVersionsQuery("s", "o")
	assertIndexed(t, s, []string{"session_id", "id"}, q, args...)
	q, args = obligationsBySourceQuery("s", "item", 2)
	assertIndexed(t, s, []string{"session_id", "f_source_item_id"}, q, args...)
}

// TestRetireLookupsUseIndex locks the DELETEs that retire an item from the
// lookup tables to (session_id, item_id) index searches (SPEC-3.1 item 1):
// every SUPERSEDES or DUPLICATE_OF edge runs them, so a session-prefix
// search made each supersession grow with the session.
func TestRetireLookupsUseIndex(t *testing.T) {
	s, _ := openTemp(t)
	for _, table := range []string{"lookup_canonical", "lookup_working", "lookup_source", "lookup_blob"} {
		assertIndexed(t, s, []string{"session_id", "item_id"}, retireLookupSQL(table), "s", "x")
	}
}
