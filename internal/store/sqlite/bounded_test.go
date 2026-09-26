package sqlite

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestLookupsDoBoundedWork checks DUR-2.1 on SQLite: finding one blob
// referrer and one page of sources reads a bounded batch of rows and loads
// a handful of items however many visible matches the session holds, and
// duplicates leave lookup_blob.
func TestLookupsDoBoundedWork(t *testing.T) {
	s, _ := openTemp(t)
	hash, key := storetest.BoundedLookupFixture(t, s, "s", 300)
	viewer := storetest.NewPrincipal("s", domain.AuthorityUser)
	within := domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: "s"}
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r := tx.(*transaction)
		r.lookupRows, r.lookupLoads = 0, 0
		l, err := tx.BlobReferrer(store.BlobReferrerFilter{Viewer: viewer, BlobHash: hash, Within: within})
		if err != nil || len(l.Items) != 1 || l.Items[0].ID != "ref-0000" {
			t.Errorf("BlobReferrer = %+v, %v", l, err)
		}
		if r.lookupRows > lookupBatch || r.lookupLoads > 2 {
			t.Errorf("BlobReferrer read %d rows and loaded %d items", r.lookupRows, r.lookupLoads)
		}
		r.lookupRows, r.lookupLoads = 0, 0
		l, err = tx.SourceItems(store.SourceFilter{Viewer: viewer, LocatorKey: key, Page: store.Page{Limit: 2}})
		if err != nil || len(l.Items) != 2 || !l.More {
			t.Errorf("SourceItems = %+v, %v", l, err)
		}
		if r.lookupRows > lookupBatch || r.lookupLoads > 4 {
			t.Errorf("one page of 2 sources read %d rows and loaded %d items", r.lookupRows, r.lookupLoads)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow("SELECT COUNT(*) FROM lookup_blob WHERE session_id='s' AND blob_hash=?", hash).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 300 {
		t.Errorf("lookup_blob holds %d referrers, want the 300 non-duplicates", n)
	}
}
