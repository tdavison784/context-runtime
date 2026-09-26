package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// docItem returns an item whose parts reference each blob in order.
func docItem(sess, id string, seq uint64, blobs ...domain.Blob) domain.ContextItem {
	it := NewItem(sess, id, seq, "caption "+id)
	for _, b := range blobs {
		it.Parts = append(it.Parts, domain.ContentPart{Type: domain.PartDocument, MediaType: "application/pdf", BlobHash: b.Hash, BlobSize: uint64(len(b.Data))})
	}
	it.ContentHash, it.SemanticBytes = domain.ContentHash(it.Parts), domain.SemanticBytes(it.Parts)
	return it
}

// testItemsByBlob checks the bounded lookup of items referencing a blob
// (R19, R5): each referencing item once, in (Seq, ID) order, with the
// transaction's own writes and never another session's.
func testItemsByBlob(t *testing.T, s store.Store) {
	x, y := NewBlob(sessA, []byte("blob x")), NewBlob(sessA, []byte("blob y"))
	update(t, s, sessB, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(NewBlob(sessB, x.Data)))
		return tx.InsertItem(docItem(sessB, "foreign", tx.NextSeq(), NewBlob(sessB, x.Data)))
	})
	var b, c, d domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertBlob(x))
		noErr(t, tx.InsertBlob(y))
		noErr(t, tx.InsertItem(NewItem(sessA, "text", tx.NextSeq(), "no blob")))
		seq := tx.NextSeq()
		c = docItem(sessA, "c", seq, x, y, x)
		b = docItem(sessA, "b", seq, x)
		noErr(t, tx.InsertItem(c))
		noErr(t, tx.InsertItem(b))
		got, err := tx.ItemsByBlob(x.Hash, 2)
		noErr(t, err)
		assertEqual(t, "own writes", got, []domain.ContextItem{b, c})
		return nil
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(docItem(sessA, "rolled-back", tx.NextSeq(), x)))
		return errRollback
	})
	wantErr(t, err, errRollback)
	update(t, s, sessA, func(tx store.Tx) error {
		d = docItem(sessA, "a-later", tx.NextSeq(), y)
		return tx.InsertItem(d)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.ItemsByBlob(x.Hash, 2)
		noErr(t, err)
		assertEqual(t, "blob x", got, []domain.ContextItem{b, c})
		got, err = tx.ItemsByBlob(y.Hash, 2)
		noErr(t, err)
		assertEqual(t, "blob y", got, []domain.ContextItem{c, d})
		got, err = tx.ItemsByBlob(domain.HashBytes([]byte("absent")), 1)
		noErr(t, err)
		assertEqual(t, "absent blob", got, []domain.ContextItem{})
		_, err = tx.ItemsByBlob(x.Hash, 1)
		wantErr(t, err, store.ErrLimitExceeded)
		_, err = tx.ItemsByBlob(x.Hash, 0)
		wantErr(t, err, domain.ErrInvalidRecord)
		_, err = tx.ItemsByBlob("not-a-hash", 1)
		wantErr(t, err, domain.ErrInvalidRecord)
		got, err = tx.ItemsByBlob(y.Hash, 2)
		noErr(t, err)
		got[0].Parts[1].BlobHash = "scribbled"
		again, err := tx.ItemsByBlob(y.Hash, 2)
		noErr(t, err)
		assertEqual(t, "after mutating a result", again, []domain.ContextItem{c, d})
		return nil
	})
}
