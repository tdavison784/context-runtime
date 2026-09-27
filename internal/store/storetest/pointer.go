package storetest

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// UncheckedSetCurrentVersion is a TEST FIXTURE ONLY: it points the item's
// current-version key at it through the backend's raw pointer write, with no
// expected-prior, duplicate, superseded or namespace check. Fixtures use it
// to model legacy (pre-upgrade) or deliberately corrupted pointer states.
// It is not part of store.Tx (SPEC-1.21); production code must use the
// semantic facet's CAS, SemanticTx.SetCurrentVersion, and a boundary test
// rejects any production reference to it.
func UncheckedSetCurrentVersion(tx store.Tx, itemID string) error {
	var base any = tx
	if g, ok := tx.(*store.Guard); ok {
		base = g.TxBase
	}
	raw, ok := base.(interface{ UncheckedSetCurrentVersion(string) error })
	if !ok {
		return domain.ErrUnsupportedSchema
	}
	return raw.UncheckedSetCurrentVersion(itemID)
}
