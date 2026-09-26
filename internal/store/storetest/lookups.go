package storetest

import (
	"github.com/tdavison784/context-runtime/internal/domain"
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

// sourcedItem returns an item whose source is a locator of the given kind.
func sourcedItem(sess, id string, seq uint64, kind domain.SourceKind, locator string) domain.ContextItem {
	it := NewItem(sess, id, seq, "contents of "+id)
	it.Source = &domain.SourceRef{Kind: kind, Locator: locator}
	return it
}
