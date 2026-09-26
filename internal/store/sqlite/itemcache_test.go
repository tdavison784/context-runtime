package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestItemCacheBounded_SPEC41 reproduces SPEC-4.1: one transaction pages
// SourceItems over many large sourced items (as ingest's declareReference
// does) and then reads each item directly. The per-transaction item cache
// must stay within its fixed entry and byte caps however many large items
// the transaction touches.
func TestItemCacheBounded_SPEC41(t *testing.T) {
	const n, size = 48, 512 << 10 // 24 MiB of text, above the byte cap
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		for i := range n {
			it := storetest.NewItem("s", fmt.Sprintf("big-%02d", i), tx.NextSeq(), strings.Repeat(string(rune('a'+i%26)), size))
			it.Source = &domain.SourceRef{Kind: domain.SourcePath, Locator: "big.go"}
			if err := tx.InsertItem(it); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		inner := tx.(*store.Guard).TxBase.(*transaction)
		check := func(stage string) {
			entries, bytes := inner.itemCacheFootprint()
			if entries > itemCacheMaxEntries || bytes > itemCacheMaxBytes {
				t.Errorf("%s: item cache holds %d entries, %d bytes; caps are %d, %d", stage, entries, bytes, itemCacheMaxEntries, itemCacheMaxBytes)
			}
		}
		f := store.SourceFilter{Viewer: storetest.NewPrincipal("s", domain.AuthorityUser), LocatorKey: "path:big.go", Page: store.Page{Limit: 4}}
		seen := 0
		for {
			l, err := tx.SourceItems(f)
			if err != nil {
				return err
			}
			seen += len(l.Items)
			check(fmt.Sprintf("after %d sources", seen))
			if !l.More {
				break
			}
			f.Page.After = l.Next
		}
		if seen != n {
			t.Fatalf("paged %d sources, want %d", seen, n)
		}
		for i := range n {
			it, err := tx.Item(fmt.Sprintf("big-%02d", i))
			if err != nil || len(it.Parts[0].Text) != size {
				t.Fatalf("Item(big-%02d) = %d bytes, %v", i, len(it.Parts[0].Text), err)
			}
		}
		check("after direct reads")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestLookupScanDoesNotFillItemCache_SPEC41 keeps point-read working data
// hot while an indexed source lookup pages through unrelated items.
func TestLookupScanDoesNotFillItemCache_SPEC41(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		for i := range 5 {
			it := storetest.NewItem("s", fmt.Sprintf("source-%d", i), tx.NextSeq(), "source text")
			it.Source = &domain.SourceRef{Kind: domain.SourcePath, Locator: "shared.go"}
			if err := tx.InsertItem(it); err != nil {
				return err
			}
		}
		return tx.InsertItem(storetest.NewItem("s", "hot", tx.NextSeq(), "hot text"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		inner := tx.(*store.Guard).TxBase.(*transaction)
		if _, err := tx.Item("hot"); err != nil {
			return err
		}
		beforeEntries, beforeBytes := inner.itemCacheFootprint()
		f := store.SourceFilter{Viewer: storetest.NewPrincipal("s", domain.AuthorityUser), LocatorKey: "path:shared.go", Page: store.Page{Limit: 2}}
		for {
			page, err := tx.SourceItems(f)
			if err != nil {
				return err
			}
			if entries, bytes := inner.itemCacheFootprint(); entries != beforeEntries || bytes != beforeBytes {
				t.Errorf("lookup filled item cache: (%d, %d), want (%d, %d)", entries, bytes, beforeEntries, beforeBytes)
			}
			if !page.More {
				break
			}
			f.Page.After = page.Next
		}
		inner.itemBytesLoaded = 0
		if _, err := tx.Item("hot"); err != nil {
			return err
		}
		if inner.itemBytesLoaded != 0 {
			t.Errorf("lookup evicted point-read item; reloaded %d bytes", inner.itemBytesLoaded)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestItemCacheEntryCap_SPEC41: many small items stay within the entry cap.
func TestItemCacheEntryCap_SPEC41(t *testing.T) {
	n := itemCacheMaxEntries + 100
	s, _ := openTemp(t)
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		inner := tx.(*store.Guard).TxBase.(*transaction)
		for i := range n {
			if err := tx.InsertItem(storetest.NewItem("s", fmt.Sprintf("small-%04d", i), tx.NextSeq(), "x")); err != nil {
				return err
			}
		}
		for i := range n {
			if _, err := tx.Item(fmt.Sprintf("small-%04d", i)); err != nil {
				return err
			}
		}
		if entries, _ := inner.itemCacheFootprint(); entries > itemCacheMaxEntries {
			t.Errorf("item cache holds %d entries, cap %d", entries, itemCacheMaxEntries)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestItemCacheLRU(t *testing.T) {
	item := func(id string, n int) domain.ContextItem {
		return storetest.NewItem("s", id, 1, strings.Repeat("x", n))
	}
	var c *itemCache
	if _, ok := c.get("a"); ok {
		t.Fatal("nil cache hit")
	}
	c.remove("a") // nil-safe
	c = &itemCache{}
	half := itemCacheMaxBytes / 2
	c.put("a", item("a", half))
	c.put("b", item("b", half))
	if _, ok := c.get("a"); !ok { // a is now most recently used
		t.Fatal("a missing")
	}
	c.put("c", item("c", 1)) // over the byte cap: evicts b, the least recent
	if _, ok := c.get("b"); ok {
		t.Error("least recently used entry survived")
	}
	for _, id := range []string{"a", "c"} {
		if _, ok := c.get(id); !ok {
			t.Errorf("%s evicted", id)
		}
	}
	c.put("huge", item("huge", itemCacheMaxBytes+1))
	if _, ok := c.get("huge"); ok {
		t.Error("an item above the byte cap was cached")
	}
	c.put("a", item("a", 10)) // replacing an entry re-costs it
	c.remove("c")
	if entries, bytes := c.footprint(); entries != 1 || bytes != 10 {
		t.Errorf("footprint = %d entries, %d bytes; want 1, 10", entries, bytes)
	}
}

// TestRolledBackMethodClearsItemCache: a store method whose savepoint rolls
// back clears the item cache, so no entry refreshed inside the rolled-back
// method can outlive it (SPEC-4.3).
func TestRolledBackMethodClearsItemCache(t *testing.T) {
	s, _ := openTemp(t)
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		inner := tx.(*store.Guard).TxBase.(*transaction)
		if err := tx.InsertItem(storetest.NewItem("s", "x", tx.NextSeq(), "x")); err != nil {
			return err
		}
		if _, err := tx.Item("x"); err != nil {
			return err
		}
		if entries, _ := inner.itemCacheFootprint(); entries != 1 {
			t.Fatalf("cache holds %d entries after a read, want 1", entries)
		}
		boom := errors.New("boom")
		if err := inner.atomic(func() error { return boom }); err != boom {
			t.Fatalf("atomic = %v", err)
		}
		if entries, _ := inner.itemCacheFootprint(); entries != 0 {
			t.Errorf("cache holds %d entries after a rolled-back method, want 0", entries)
		}
		if it, err := tx.Item("x"); err != nil || it.ID != "x" {
			t.Errorf("re-read after rollback = %v, %v", it.ID, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
