package memory

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestLookupsDoBoundedWork checks DUR-2.1 on the memory store: finding one
// blob referrer and one page of sources loads a handful of items however
// many visible matches the session holds, and duplicates leave the blob
// index.
func TestLookupsDoBoundedWork(t *testing.T) {
	s := New()
	defer s.Close()
	hash, key := storetest.BoundedLookupFixture(t, s, "s", 300)
	viewer := storetest.NewPrincipal("s", domain.AuthorityUser)
	within := domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: "s"}
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r := tx.(*readTx)
		r.items.gets = 0
		l, err := tx.BlobReferrer(store.BlobReferrerFilter{Viewer: viewer, BlobHash: hash, Within: within})
		if err != nil || len(l.Items) != 1 || l.Items[0].ID != "ref-0000" {
			t.Errorf("BlobReferrer = %+v, %v", l, err)
		}
		if r.items.gets > 2 {
			t.Errorf("BlobReferrer loaded %d items, want at most 2", r.items.gets)
		}
		r.items.gets = 0
		l, err = tx.SourceItems(store.SourceFilter{Viewer: viewer, LocatorKey: key, Page: store.Page{Limit: 2}})
		if err != nil || len(l.Items) != 2 || !l.More {
			t.Errorf("SourceItems = %+v, %v", l, err)
		}
		if r.items.gets > 4 {
			t.Errorf("one page of 2 sources loaded %d items, want at most 4", r.items.gets)
		}
		if n := len(r.blobOwners.base[blobKey{hash, owners{}}]); n != 300 {
			t.Errorf("blob index holds %d referrers, want the 300 non-duplicates", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
