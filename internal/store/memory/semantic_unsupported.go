package memory

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Facet methods whose record families have not landed in this backend yet.
// They fail closed with domain.ErrUnsupportedSchema: no stub ever reports a
// successful write or an empty read. Each family moves out of this file when
// it is implemented.

var errUnsupported = domain.ErrUnsupportedSchema

// Declarations and semantic changes.

func (r semRead) OpenGoalsByTaskOwner(string, store.Page) (store.ResultPage[domain.ContextItem], error) {
	return store.ResultPage[domain.ContextItem]{}, errUnsupported
}

// Retrieval.

func (r semRead) RetrievalLease(string) (domain.RetrievalLease, error) {
	return domain.RetrievalLease{}, errUnsupported
}
func (r semRead) RetrievalResult(string) (domain.RetrievalResult, error) {
	return domain.RetrievalResult{}, errUnsupported
}
func (r semRead) RetrievalEvent(string) (domain.RetrievalEvent, error) {
	return domain.RetrievalEvent{}, errUnsupported
}
func (r semRead) Projection(string) (domain.ProjectionRecord, error) {
	return domain.ProjectionRecord{}, errUnsupported
}
func (r semRead) ProjectionByItem(string) (domain.ProjectionRecord, error) {
	return domain.ProjectionRecord{}, errUnsupported
}
func (r semRead) LeasesByHolder(domain.Principal, string, string, store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	return store.ResultPage[domain.RetrievalLease]{}, errUnsupported
}
func (r semRead) LeasesBySource(domain.ItemContentRef, store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	return store.ResultPage[domain.RetrievalLease]{}, errUnsupported
}
func (r semRead) RetrievalEventsByRequest(domain.Principal, string, store.Page) (store.ResultPage[domain.RetrievalEvent], error) {
	return store.ResultPage[domain.RetrievalEvent]{}, errUnsupported
}
func (t *semTx) InsertRetrievalLease(domain.RetrievalLease) error   { return errUnsupported }
func (t *semTx) InsertRetrievalResult(domain.RetrievalResult) error { return errUnsupported }
func (t *semTx) InsertRetrievalEvent(domain.RetrievalEvent) error   { return errUnsupported }
func (t *semTx) InsertProjection(domain.ProjectionRecord) error     { return errUnsupported }

// GC receipts.

func (r semRead) GCRequest(string) (domain.GCRequest, error) {
	return domain.GCRequest{}, errUnsupported
}
func (r semRead) GCResult(string) (domain.GCResult, error) { return domain.GCResult{}, errUnsupported }
func (r semRead) CollectReceipt(string) (domain.CollectReceipt, error) {
	return domain.CollectReceipt{}, errUnsupported
}
func (r semRead) PendingGCRequests(store.Page) (store.ResultPage[domain.GCRequest], error) {
	return store.ResultPage[domain.GCRequest]{}, errUnsupported
}
func (r semRead) GCCandidates(store.GCCandidateFilter) (store.ResultPage[domain.ContextItem], error) {
	return store.ResultPage[domain.ContextItem]{}, errUnsupported
}
func (t *semTx) InsertGCRequest(domain.GCRequest) error           { return errUnsupported }
func (t *semTx) InsertGCResult(domain.GCResult) error             { return errUnsupported }
func (t *semTx) InsertCollectReceipt(domain.CollectReceipt) error { return errUnsupported }
