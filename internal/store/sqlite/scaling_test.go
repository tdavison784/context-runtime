package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// derivedLoadBytes derives n items from one transcript in one transaction,
// as ingest does for a span with n directives, and returns the item bytes
// the store decoded and verified doing it.
func derivedLoadBytes(t *testing.T, n int) uint64 {
	t.Helper()
	s, _ := openTemp(t)
	actor := storetest.NewPrincipal("s", domain.AuthoritySystem)
	var loaded uint64
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		inner := tx.(*store.Guard).TxBase.(*transaction)
		var b strings.Builder
		for i := range n {
			fmt.Fprintf(&b, "- pinned requirement number %d\n", i)
		}
		tr := storetest.NewTranscript("s", "tr", tx.NextSeq(), b.String())
		tr.Authority = domain.AuthoritySystem
		if err := tx.InsertItem(tr); err != nil {
			return err
		}
		inner.itemBytesLoaded = 0
		for i := range n {
			d := storetest.NewDirective("s", fmt.Sprintf("d%d", i), fmt.Sprintf("d%d", i), tx.NextSeq(), fmt.Sprintf("pinned requirement number %d", i))
			d.Authority = domain.AuthoritySystem
			if err := tx.InsertItem(d); err != nil {
				return err
			}
			if _, err := graph.LinkDerived(tx, actor, d.ID, []string{"tr"}, &domain.Coverage{}, "evt"); err != nil {
				return err
			}
		}
		loaded = inner.itemBytesLoaded
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return loaded
}

// TestDerivedLinksLoadTranscriptOnce checks SPEC-3.1 item 2 at store level:
// deriving n items from one growing transcript in one transaction decodes
// and verifies the transcript a bounded number of times, so the bytes the
// store loads grow linearly in n, not with n times the transcript.
func TestDerivedLinksLoadTranscriptOnce(t *testing.T) {
	small, large := derivedLoadBytes(t, 500), derivedLoadBytes(t, 4000)
	if ratio := float64(large) / float64(small); ratio > 12 {
		t.Errorf("item bytes loaded: 500 items %d, 4000 items %d (x%.1f); want about x8 (linear)", small, large, ratio)
	}
}

// TestLargeTranscriptStaysCached_SPEC41 guards configured spans above the
// former 16 MiB cache cap: derived links must decode the transcript once.
func TestLargeTranscriptStaysCached_SPEC41(t *testing.T) {
	const size = 17 << 20
	s, _ := openTemp(t)
	actor := storetest.NewPrincipal("s", domain.AuthoritySystem)
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		inner := tx.(*store.Guard).TxBase.(*transaction)
		tr := storetest.NewTranscript("s", "tr", tx.NextSeq(), strings.Repeat("x", size))
		tr.Authority = domain.AuthoritySystem
		if err := tx.InsertItem(tr); err != nil {
			return err
		}
		inner.itemBytesLoaded = 0
		for i := range 8 {
			d := storetest.NewDirective("s", fmt.Sprintf("d%d", i), fmt.Sprintf("d%d", i), tx.NextSeq(), "small")
			d.Authority = domain.AuthoritySystem
			if err := tx.InsertItem(d); err != nil {
				return err
			}
			if _, err := graph.LinkDerived(tx, actor, d.ID, []string{"tr"}, &domain.Coverage{}, "evt"); err != nil {
				return err
			}
		}
		if inner.itemBytesLoaded > 2*uint64(size) {
			t.Errorf("loaded %d item bytes for eight links to a %d-byte transcript; want one transcript decode", inner.itemBytesLoaded, size)
		}
		if _, bytes := inner.itemCacheFootprint(); bytes < size {
			t.Errorf("large transcript not cached: footprint %d bytes", bytes)
		}
		if inner.itemCache.maxBytes != 2*size {
			t.Errorf("transcript cache cap = %d, want %d", inner.itemCache.maxBytes, 2*size)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
