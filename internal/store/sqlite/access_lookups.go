package sqlite

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// errLookupUnimplemented marks the access-filtered lookups until their
// SQLite indexes land.
var errLookupUnimplemented = errors.New("sqlite: access-filtered lookups not implemented yet")

func (t *transaction) BlobReferrer(store.BlobReferrerFilter) (store.Lookup, error) {
	return store.Lookup{}, errLookupUnimplemented
}
func (t *transaction) CanonicalCandidates(store.CanonicalFilter) (store.Lookup, error) {
	return store.Lookup{}, errLookupUnimplemented
}
func (t *transaction) CurrentWorking(store.WorkingFilter) (store.Lookup, error) {
	return store.Lookup{}, errLookupUnimplemented
}
func (t *transaction) SourceItems(store.SourceFilter) (store.Lookup, error) {
	return store.Lookup{}, errLookupUnimplemented
}
func (t *transaction) VisibleReferences(store.VisibleReferenceFilter) ([]domain.UnresolvedReference, bool, store.Cursor, error) {
	return nil, false, store.Cursor{}, errLookupUnimplemented
}
