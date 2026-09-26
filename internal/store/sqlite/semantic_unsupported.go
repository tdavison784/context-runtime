package sqlite

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

func (semRead) OpenGoalsByTaskOwner(string, store.Page) (store.ResultPage[domain.ContextItem], error) {
	return store.ResultPage[domain.ContextItem]{}, errUnsupported
}

// Obligation proofs.

func (semRead) ExactObligation(domain.ObligationRef) (domain.ObligationVersion, error) {
	return domain.ObligationVersion{}, errUnsupported
}
func (semRead) ObligationDeclaration(domain.ObligationRef) (domain.ObligationDeclaration, error) {
	return domain.ObligationDeclaration{}, errUnsupported
}
func (semRead) ApplicabilityProof(string) (domain.ApplicabilityProof, error) {
	return domain.ApplicabilityProof{}, errUnsupported
}
func (semRead) Assertion(string) (domain.AssertionRecord, error) {
	return domain.AssertionRecord{}, errUnsupported
}
func (semRead) TransitionDetail(string) (domain.TransitionDetail, error) {
	return domain.TransitionDetail{}, errUnsupported
}
func (semRead) ProofDependencies(string, store.Page) (store.ResultPage[domain.ProofDependency], error) {
	return store.ResultPage[domain.ProofDependency]{}, errUnsupported
}
func (semRead) CurrentBoundObligationsBySubject(string, store.Page) (store.ResultPage[domain.ObligationVersion], error) {
	return store.ResultPage[domain.ObligationVersion]{}, errUnsupported
}
func (semRead) CurrentProofsByDependency(string, string, store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	return store.ResultPage[domain.ApplicabilityProof]{}, errUnsupported
}
func (semRead) ObligationsByTaskOwner(string, store.Page) (store.ResultPage[domain.ObligationVersion], error) {
	return store.ResultPage[domain.ObligationVersion]{}, errUnsupported
}
func (semRead) TransitionsByVersion(domain.ObligationRef, store.Page) (store.ResultPage[domain.ObligationTransition], error) {
	return store.ResultPage[domain.ObligationTransition]{}, errUnsupported
}
func (semRead) Satisfies(domain.Principal, domain.ObligationRef, bool, store.Page) (store.ResultPage[domain.SatisfiesRelation], error) {
	return store.ResultPage[domain.SatisfiesRelation]{}, errUnsupported
}
func (semTx) InsertObligationDeclaration(domain.ObligationDeclaration) error {
	return errUnsupported
}
func (semTx) InsertApplicabilityProof(domain.ApplicabilityProof, []domain.ProofDependency) error {
	return errUnsupported
}
func (semTx) InsertAssertion(domain.AssertionRecord) error { return errUnsupported }
func (semTx) AppendSemanticObligationTransition(domain.ObligationTransition, domain.TransitionDetail, uint64) (domain.ObligationVersion, error) {
	return domain.ObligationVersion{}, errUnsupported
}
func (semTx) SetObligationMaterialization(domain.ObligationRef, bool, uint64, domain.LifecycleEvent) (domain.ObligationVersion, error) {
	return domain.ObligationVersion{}, errUnsupported
}

// Retrieval.

func (semRead) RetrievalLease(string) (domain.RetrievalLease, error) {
	return domain.RetrievalLease{}, errUnsupported
}
func (semRead) RetrievalResult(string) (domain.RetrievalResult, error) {
	return domain.RetrievalResult{}, errUnsupported
}
func (semRead) RetrievalEvent(string) (domain.RetrievalEvent, error) {
	return domain.RetrievalEvent{}, errUnsupported
}
func (semRead) Projection(string) (domain.ProjectionRecord, error) {
	return domain.ProjectionRecord{}, errUnsupported
}
func (semRead) ProjectionByItem(string) (domain.ProjectionRecord, error) {
	return domain.ProjectionRecord{}, errUnsupported
}
func (semRead) LeasesByHolder(domain.Principal, string, string, store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	return store.ResultPage[domain.RetrievalLease]{}, errUnsupported
}
func (semRead) LeasesBySource(domain.ItemContentRef, store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	return store.ResultPage[domain.RetrievalLease]{}, errUnsupported
}
func (semRead) RetrievalEventsByRequest(domain.Principal, string, store.Page) (store.ResultPage[domain.RetrievalEvent], error) {
	return store.ResultPage[domain.RetrievalEvent]{}, errUnsupported
}
func (semTx) InsertRetrievalLease(domain.RetrievalLease) error   { return errUnsupported }
func (semTx) InsertRetrievalResult(domain.RetrievalResult) error { return errUnsupported }
func (semTx) InsertRetrievalEvent(domain.RetrievalEvent) error   { return errUnsupported }
func (semTx) InsertProjection(domain.ProjectionRecord) error     { return errUnsupported }

// GC receipts.

func (semRead) GCRequest(string) (domain.GCRequest, error) {
	return domain.GCRequest{}, errUnsupported
}
func (semRead) GCResult(string) (domain.GCResult, error) { return domain.GCResult{}, errUnsupported }
func (semRead) CollectReceipt(string) (domain.CollectReceipt, error) {
	return domain.CollectReceipt{}, errUnsupported
}
func (semRead) PendingGCRequests(store.Page) (store.ResultPage[domain.GCRequest], error) {
	return store.ResultPage[domain.GCRequest]{}, errUnsupported
}
func (semRead) GCCandidates(store.GCCandidateFilter) (store.ResultPage[domain.ContextItem], error) {
	return store.ResultPage[domain.ContextItem]{}, errUnsupported
}
func (semTx) InsertGCRequest(domain.GCRequest) error           { return errUnsupported }
func (semTx) InsertGCResult(domain.GCResult) error             { return errUnsupported }
func (semTx) InsertCollectReceipt(domain.CollectReceipt) error { return errUnsupported }
