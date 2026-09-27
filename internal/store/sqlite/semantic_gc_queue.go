package sqlite

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// The DUR-3.2 GC queue cursor and per-trigger pending read are a W1
// contract whose backend W2 implements; until then they fail closed with
// domain.ErrUnsupportedSchema.

func (s semRead) PendingGCRequestsByTrigger([]domain.GCTrigger, store.Page) (store.ResultPage[domain.GCRequest], error) {
	return store.ResultPage[domain.GCRequest]{}, domain.ErrUnsupportedSchema
}

func (s semRead) GCQueueCursor() (domain.GCQueueCursor, error) {
	return domain.GCQueueCursor{}, domain.ErrUnsupportedSchema
}

func (s semTx) PutGCQueueCursor(domain.GCQueueCursor, uint64) (domain.GCQueueCursor, error) {
	return domain.GCQueueCursor{}, domain.ErrUnsupportedSchema
}
