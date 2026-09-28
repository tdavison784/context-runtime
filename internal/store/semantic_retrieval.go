package store

import "github.com/tdavison784/context-runtime/internal/domain"

type RetrievalReader interface {
	RetrievalLease(id string) (domain.RetrievalLease, error)
	RetrievalResult(id string) (domain.RetrievalResult, error)
	RetrievalEvent(id string) (domain.RetrievalEvent, error)
	Projection(id string) (domain.ProjectionRecord, error)
	ProjectionByItem(itemID string) (domain.ProjectionRecord, error)
	LeasesByHolder(holder domain.Principal, conversationID, turnID string, page Page) (ResultPage[domain.RetrievalLease], error)
	// Internal protection query includes ALL holders, not just the collector.
	LeasesBySource(source domain.ItemContentRef, page Page) (ResultPage[domain.RetrievalLease], error)
	RetrievalEventsByRequest(viewer domain.Principal, requestID string, page Page) (ResultPage[domain.RetrievalEvent], error)
}
type RetrievalWriter interface {
	InsertRetrievalLease(domain.RetrievalLease) error
	InsertRetrievalResult(domain.RetrievalResult) error
	InsertRetrievalEvent(domain.RetrievalEvent) error
	InsertProjection(domain.ProjectionRecord) error
}
