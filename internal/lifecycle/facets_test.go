package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// facets tracks the item IDs a test asserts over; every read and write goes
// through the real backend's semantic facet.
type facets struct{ items []string }

func newFacets(items ...string) *facets { return &facets{items: items} }

func (f *facets) update(mem store.Store, fn func(store.Tx) error) error {
	return mem.Update(context.Background(), "s", fn)
}

// readSemantic runs fn over the committed semantic facet.
func readSemantic(t *testing.T, mem store.Store, fn func(store.SemanticReader) error) {
	t.Helper()
	if err := mem.View(context.Background(), "s", func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		return fn(sem)
	}); err != nil {
		t.Fatal(err)
	}
}

func pendingGC(t *testing.T, mem store.Store) []domain.GCRequest {
	t.Helper()
	var out []domain.GCRequest
	readSemantic(t, mem, func(sem store.SemanticReader) error {
		page, err := sem.PendingGCRequests(store.Page{Limit: 16})
		out = page.Records
		return err
	})
	return out
}
