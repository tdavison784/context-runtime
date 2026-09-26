package storetest

import (
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// BoundedLookupFixture fills session sess with n visible referrers of one
// blob, n visible items read from one source path, and n identical items
// classified DUPLICATE_OF the first, so a store test can check that
// lookups do work independent of n (DUR-2.1). It returns the blob hash
// and the source key.
func BoundedLookupFixture(t *testing.T, s store.Store, sess string, n int) (blobHash, sourceKey string) {
	t.Helper()
	b := NewBlob(sess, []byte("bounded blob"))
	if err := s.Update(ctx, sess, func(tx store.Tx) error {
		if err := tx.InsertBlob(b); err != nil {
			return err
		}
		for i := range n {
			if err := tx.InsertItem(docItem(sess, fmt.Sprintf("ref-%04d", i), tx.NextSeq(), b)); err != nil {
				return err
			}
			if err := tx.InsertItem(sourcedItem(sess, fmt.Sprintf("src-%04d", i), tx.NextSeq(), domain.SourcePath, "src/main.go")); err != nil {
				return err
			}
		}
		// Repeats of ref-0000 with identical content and boundary.
		first, err := tx.Item("ref-0000")
		if err != nil {
			return err
		}
		for i := range n {
			d := first
			d.ID, d.Seq = fmt.Sprintf("dup-%04d", i), tx.NextSeq()
			if err := tx.InsertItem(d); err != nil {
				return err
			}
			if err := tx.InsertRelationship(NewRelationship(sess, "edge-"+d.ID, domain.RelDuplicateOf, d.ID, "ref-0000", tx.NextSeq())); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return b.Hash, "path:src/main.go"
}
