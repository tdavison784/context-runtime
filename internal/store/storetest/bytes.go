package storetest

import (
	"bytes"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// losslessTexts are text part payloads a store must return byte for byte:
// invalid UTF-8 (61 ff 62), an encoded surrogate, a BOM, NUL, CR/CRLF line
// endings, and a line separator. Stores never repair or normalize text
// (FR-ING-007, D3).
var losslessTexts = []string{
	"a\xffb",
	"\xed\xa0\x80 surrogate",
	"\xef\xbb\xbf# Pinned\r\n- x\rNUL\x00end ",
}

// losslessItem returns an item whose text parts (and a media type) carry
// losslessTexts.
func losslessItem(sess, id string, seq uint64) domain.ContextItem {
	it := NewItem(sess, id, seq, "")
	it.Parts = nil
	for _, text := range losslessTexts {
		it.Parts = append(it.Parts, domain.ContentPart{Type: domain.PartText, MediaType: "text/plain; x=\xfe", Text: text})
	}
	it.ContentHash = domain.ContentHash(it.Parts)
	it.SemanticBytes = domain.SemanticBytes(it.Parts)
	return it
}

// checkLossless fails unless got carries want's exact part bytes and its
// stored content hash still verifies against them.
func checkLossless(t *testing.T, what string, got, want domain.ContextItem) {
	t.Helper()
	if len(got.Parts) != len(want.Parts) {
		t.Fatalf("%s: %d parts, want %d", what, len(got.Parts), len(want.Parts))
	}
	for i := range want.Parts {
		g, w := got.Parts[i], want.Parts[i]
		if !bytes.Equal([]byte(g.Text), []byte(w.Text)) || g.MediaType != w.MediaType {
			t.Errorf("%s part %d: text % x media % x, want text % x media % x", what, i, g.Text, g.MediaType, w.Text, w.MediaType)
		}
	}
	if got.ContentHash != want.ContentHash || domain.ContentHash(got.Parts) != got.ContentHash {
		t.Errorf("%s: stored content hash no longer verifies against the returned parts", what)
	}
	assertEqual(t, what, got, want)
}

func testItemLosslessText(t *testing.T, s store.Store) {
	var want domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		want = losslessItem(sessA, "raw", tx.NextSeq())
		noErr(t, tx.InsertItem(want))
		got, err := tx.Item("raw")
		noErr(t, err)
		checkLossless(t, "Item inside Update", got, want)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Item("raw")
		noErr(t, err)
		checkLossless(t, "Item", got, want)
		items, err := tx.Items(store.ItemFilter{})
		noErr(t, err)
		if len(items) != 1 {
			t.Fatalf("Items = %d records, want 1", len(items))
		}
		checkLossless(t, "Items", items[0], want)
		return nil
	})
}

func testLosslessTextAcrossRestart(t *testing.T, open Opener) {
	s := openDurable(t, open)
	var want domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		want = losslessItem(sessA, "raw", tx.NextSeq())
		return tx.InsertItem(want)
	})
	s = reopen(t, s, open)
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Item("raw")
		noErr(t, err)
		checkLossless(t, "Item after restart", got, want)
		return nil
	})
}
