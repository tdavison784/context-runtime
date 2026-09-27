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

// Distinct IDs that a lossy encoding would merge: "a\xffb" and "a\xfeb"
// both become "a�b", the third ID. A string list that references one
// of them (grant targets, coverage, evidence) must never come back naming
// another.
var confusableIDs = []string{"a\xfeb", "a\xffb", "a�b"}

func testByteExactStringLists(t *testing.T, s store.Store) {
	var wantItem domain.ContextItem
	var wantRel domain.Relationship
	var wantEvent domain.EventRecord
	var wantGrant domain.MutationGrant
	var wantTransition domain.ObligationTransition
	var wantObligation domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		for _, id := range confusableIDs {
			noErr(t, tx.InsertItem(NewItem(sessA, id, tx.NextSeq(), id)))
		}
		wantItem = NewItem(sessA, "tagged", tx.NextSeq(), "x")
		wantItem.Tags = []string{"t\xff", "t�"}
		noErr(t, tx.InsertItem(wantItem))
		wantRel = NewRelationship(sessA, "r", domain.RelDerivedFrom, "tagged", confusableIDs[1], tx.NextSeq())
		wantRel.Coverage = &domain.Coverage{ConversationID: "c", FromSeq: 1, ToSeq: 3, ItemIDs: []string{confusableIDs[0], confusableIDs[1]}}
		noErr(t, tx.InsertRelationship(wantRel))
		wantEvent = NewEvent(sessA, "e", tx.NextSeq(), "p")
		wantEvent.ItemIDs = []string{confusableIDs[1]}
		stored, _, err := tx.InsertEvent(wantEvent)
		noErr(t, err)
		wantEvent = stored
		wantGrant = NewGrant(sessA, "g", tx.NextSeq(), confusableIDs[1])
		noErr(t, tx.InsertGrant(wantGrant))
		noErr(t, tx.InsertObligationVersion(NewObligation(sessA, "o", 1, tx.NextSeq(), "tagged")))
		// BLOCKED: the raw path never satisfies (DUR-2.12); the transition
		// still carries its evidence and fingerprint lists.
		wantTransition = NewTransition(sessA, "tr", "o", 1, tx.NextSeq(), domain.ObligationUnresolved, domain.ObligationBlocked)
		wantTransition.EvidenceIDs = []string{confusableIDs[1]}
		wantTransition.Fingerprints = []string{"fp\xff"}
		wantObligation, err = tx.AppendObligationTransition(wantTransition, 1)
		noErr(t, err)
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		it, err := tx.Item("tagged")
		noErr(t, err)
		assertBytes(t, "item tags", it.Tags, wantItem.Tags)
		rels, err := tx.Relationships(store.RelationshipFilter{FromID: "tagged"})
		noErr(t, err)
		if len(rels) != 1 || rels[0].Coverage == nil {
			t.Fatalf("Relationships = %+v", rels)
		}
		assertBytes(t, "coverage item IDs", rels[0].Coverage.ItemIDs, wantRel.Coverage.ItemIDs)
		ev, err := tx.Event("e")
		noErr(t, err)
		assertBytes(t, "event item IDs", ev.ItemIDs, wantEvent.ItemIDs)
		g, err := tx.Grant("g")
		noErr(t, err)
		assertBytes(t, "grant target IDs", g.TargetIDs, wantGrant.TargetIDs)
		trs, err := tx.ObligationTransitions("o")
		noErr(t, err)
		if len(trs) != 1 {
			t.Fatalf("ObligationTransitions = %d records, want 1", len(trs))
		}
		assertBytes(t, "transition evidence IDs", trs[0].EvidenceIDs, wantTransition.EvidenceIDs)
		assertBytes(t, "transition fingerprints", trs[0].Fingerprints, wantTransition.Fingerprints)
		o, err := tx.Obligation("o")
		noErr(t, err)
		assertBytes(t, "obligation evidence IDs", o.EvidenceIDs, wantObligation.EvidenceIDs)
		return nil
	})
}

// assertBytes fails unless got and want hold the same strings byte for byte.
func assertBytes(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = % x, want % x", what, i, got[i], want[i])
		}
	}
}
