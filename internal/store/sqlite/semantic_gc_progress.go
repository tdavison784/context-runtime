package sqlite

import "github.com/tdavison784/context-runtime/internal/domain"

// GC progress (H3) is a W1 contract whose backend W2 implements; until then
// both methods fail closed with domain.ErrUnsupportedSchema.

func (s semRead) GCProgress(string) (domain.GCProgress, error) {
	return domain.GCProgress{}, domain.ErrUnsupportedSchema
}

func (s semTx) PutGCProgress(domain.GCProgress, uint64) (domain.GCProgress, error) {
	return domain.GCProgress{}, domain.ErrUnsupportedSchema
}
