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
	combos := ownerCombos(planViewer)
	var q string
	var args []any
	for _, b := range blobReferrerQueries("s", "h", combos) {
		q, args = b(mid, lookupBatch)
		check(append([]string{"session_id", "blob_hash"}, owners...), q, args)
	}
	q, args = canonicalQuery("s", store.CanonicalFilter{TaskID: "task", Kind: domain.KindFact, Authority: domain.AuthorityUser,
		Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}, ContentHash: "h"})(mid, lookupBatch)
	check([]string{"session_id", "content_hash", "task_id", "section", "directive_id", "kind", "role", "authority", "scope", "access_session_id", "workflow_id", "access_task_id", "agent_id"}, q, args)
	q, args = workingQuery("s", store.WorkingFilter{TaskID: "task", Authority: domain.AuthorityUser,
		Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}})(mid, lookupBatch)
	check([]string{"session_id", "task_id", "authority", "scope", "access_session_id", "workflow_id", "access_task_id", "agent_id"}, q, args)
	for _, b := range sourceItemsQueries("s", "k", combos) {
		q, args = b(mid, lookupBatch)
		check(append([]string{"session_id", "rule_version", "locator_key"}, owners...), q, args)
	}
	for _, b := range visibleReferencesQueries("s", "k", combos) {
		q, args = b(mid, 3)
		check([]string{"session_id", "f_locator_key", "f_rule_version", "f_access_workflow_id", "f_access_task_id", "f_access_agent_id"}, q, args)
	}
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

// assertSeeks fails unless q's plan searches an index with cursor inside
// its constraint and sorts nothing: a page after a cursor then costs what
// it returns, not the matches before the cursor (SPEC-3.1 item 4).
func assertSeeks(t *testing.T, s *Store, cursor string, q string, args ...any) {
	t.Helper()
	rows, err := s.db.Query("EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seeks := false
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "TEMP B-TREE") {
			t.Errorf("query sorts (%s): %q", detail, q)
		}
		if strings.HasPrefix(detail, "SEARCH ") && strings.Contains(detail, cursor) {
			seeks = true
		}
	}
	if !seeks {
		t.Errorf("cursor %s is not part of an index search: %q", cursor, q)
	}
}

// TestLookupCursorsSeek runs every paged or batched lookup builder, one
// query per permitted owner combination, at a mid-list cursor (SPEC-3.1
// item 4).
func TestLookupCursorsSeek(t *testing.T) {
	s, _ := openTemp(t)
	mid := store.Cursor{Seq: 7, ID: "m"}
	combos := ownerCombos(planViewer)
	if len(combos) != 8 {
		t.Fatalf("owner combinations = %d, want 8", len(combos))
	}
	for _, b := range blobReferrerQueries("s", "h", combos) {
		q, args := b(mid, lookupBatch)
		assertSeeks(t, s, "(seq,item_id)>(?,?)", q, args...)
	}
	for _, b := range sourceItemsQueries("s", "k", combos) {
		q, args := b(mid, lookupBatch)
		assertSeeks(t, s, "(seq,item_id)>(?,?)", q, args...)
	}
	for _, b := range visibleReferencesQueries("s", "k", combos) {
		q, args := b(mid, 3)
		assertSeeks(t, s, "(f_seq,id)>(?,?)", q, args...)
	}
	q, args := canonicalQuery("s", store.CanonicalFilter{TaskID: "task", Kind: domain.KindFact, Authority: domain.AuthorityUser,
		Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}, ContentHash: "h"})(mid, lookupBatch)
	assertSeeks(t, s, "(seq,item_id)>(?,?)", q, args...)
	q, args = workingQuery("s", store.WorkingFilter{TaskID: "task", Authority: domain.AuthorityUser,
		Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}})(mid, lookupBatch)
	assertSeeks(t, s, "(seq,item_id)>(?,?)", q, args...)
}

// TestSourceItemsReportsUnverifiedOnce is DUR-3.1: paging through sources
// interleaved with legacy rows 0001 altered reports each unverified ID on
// exactly one page, never one past the page's Next cursor (which the next
// page reads again).
func TestSourceItemsReportsUnverifiedOnce(t *testing.T) {
	l := openLegacy(t, 1)
	for i, id := range []string{"src-a", "lossy-1", "src-b", "lossy-2", "src-c"} {
		text := id
		if strings.HasPrefix(id, "lossy") {
			text = id + "\xff"
		}
		it := storetest.NewItem("s", id, uint64(i+1), text)
		it.Source = &domain.SourceRef{Kind: domain.SourcePath, Locator: "src/main.go"}
		o := legacyLists(t, "item", it)
		o["f_parts"] = legacyPartsJSON(t, it.Parts)
		l.insert("item", it, o)
	}
	s := l.upgrade()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		f := store.SourceFilter{Viewer: planViewer, LocatorKey: "path:src/main.go", Page: store.Page{Limit: 1}}
		var items []string
		seen := map[string]int{}
		for page := 0; ; page++ {
			lk, err := tx.SourceItems(f)
			if err != nil {
				return err
			}
			for _, it := range lk.Items {
				items = append(items, it.ID)
			}
			for _, id := range lk.Unverified {
				seen[id]++
			}
			if page > 10 {
				t.Fatal("paging does not terminate")
			}
			if !lk.More {
				break
			}
			f.Page.After = lk.Next
		}
		if strings.Join(items, ",") != "src-a,src-b,src-c" {
			t.Errorf("items = %v", items)
		}
		if seen["lossy-1"] != 1 || seen["lossy-2"] != 1 {
			t.Errorf("unverified reported %v, want each once", seen)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
