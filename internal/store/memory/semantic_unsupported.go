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

func (r semRead) LifecycleByTarget(domain.TargetKind, string, store.Page) (store.ResultPage[domain.LifecycleEvent], error) {
	return store.ResultPage[domain.LifecycleEvent]{}, errUnsupported
}
func (r semRead) GrantsFor(domain.Action, domain.GrantTarget, int) ([]domain.MutationGrant, error) {
	return nil, errUnsupported
}
func (r semRead) OpenGoalsByTaskOwner(string, store.Page) (store.ResultPage[domain.ContextItem], error) {
	return store.ResultPage[domain.ContextItem]{}, errUnsupported
}
func (r semRead) SemanticChanges(domain.Principal, domain.GrantTarget, store.Page) (store.ResultPage[domain.SemanticChange], error) {
	return store.ResultPage[domain.SemanticChange]{}, errUnsupported
}
func (t *semTx) InsertSemanticChange(domain.SemanticChange) error { return errUnsupported }

// Obligation proofs.

func (r semRead) ExactObligation(domain.ObligationRef) (domain.ObligationVersion, error) {
	return domain.ObligationVersion{}, errUnsupported
}
func (r semRead) ObligationDeclaration(domain.ObligationRef) (domain.ObligationDeclaration, error) {
	return domain.ObligationDeclaration{}, errUnsupported
}
func (r semRead) ApplicabilityProof(string) (domain.ApplicabilityProof, error) {
	return domain.ApplicabilityProof{}, errUnsupported
}
func (r semRead) Assertion(string) (domain.AssertionRecord, error) {
	return domain.AssertionRecord{}, errUnsupported
}
func (r semRead) TransitionDetail(string) (domain.TransitionDetail, error) {
	return domain.TransitionDetail{}, errUnsupported
}
func (r semRead) ProofDependencies(string, store.Page) (store.ResultPage[domain.ProofDependency], error) {
	return store.ResultPage[domain.ProofDependency]{}, errUnsupported
}
func (r semRead) CurrentBoundObligationsBySubject(string, store.Page) (store.ResultPage[domain.ObligationVersion], error) {
	return store.ResultPage[domain.ObligationVersion]{}, errUnsupported
}
func (r semRead) CurrentProofsByDependency(string, string, store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	return store.ResultPage[domain.ApplicabilityProof]{}, errUnsupported
}
func (r semRead) ObligationsByTaskOwner(string, store.Page) (store.ResultPage[domain.ObligationVersion], error) {
	return store.ResultPage[domain.ObligationVersion]{}, errUnsupported
}
func (r semRead) TransitionsByVersion(domain.ObligationRef, store.Page) (store.ResultPage[domain.ObligationTransition], error) {
	return store.ResultPage[domain.ObligationTransition]{}, errUnsupported
}
func (r semRead) Satisfies(domain.Principal, domain.ObligationRef, bool, store.Page) (store.ResultPage[domain.SatisfiesRelation], error) {
	return store.ResultPage[domain.SatisfiesRelation]{}, errUnsupported
}
func (t *semTx) InsertObligationDeclaration(domain.ObligationDeclaration) error {
	return errUnsupported
}
func (t *semTx) InsertApplicabilityProof(domain.ApplicabilityProof, []domain.ProofDependency) error {
	return errUnsupported
}
func (t *semTx) InsertAssertion(domain.AssertionRecord) error { return errUnsupported }
func (t *semTx) AppendSemanticObligationTransition(domain.ObligationTransition, domain.TransitionDetail, uint64) (domain.ObligationVersion, error) {
	return domain.ObligationVersion{}, errUnsupported
}
func (t *semTx) SetObligationMaterialization(domain.ObligationRef, bool, uint64, domain.LifecycleEvent) (domain.ObligationVersion, error) {
	return domain.ObligationVersion{}, errUnsupported
}

// Resources and observations.

func (r semRead) ResourceBinding(string) (domain.ResourceBinding, error) {
	return domain.ResourceBinding{}, errUnsupported
}
func (r semRead) ResourceState(string) (domain.ResourceState, error) {
	return domain.ResourceState{}, errUnsupported
}
func (r semRead) ResourceUpdate(string) (domain.ResourceUpdate, error) {
	return domain.ResourceUpdate{}, errUnsupported
}
func (r semRead) ResourceUpdates(string, store.Page) (store.ResultPage[domain.ResourceUpdate], error) {
	return store.ResultPage[domain.ResourceUpdate]{}, errUnsupported
}
func (r semRead) ResourcePathState(domain.ResourceLocator) (domain.ResourcePathState, error) {
	return domain.ResourcePathState{}, errUnsupported
}
func (r semRead) WorkspaceBinding(domain.WorkspaceBindingRef) (domain.WorkspaceBinding, error) {
	return domain.WorkspaceBinding{}, errUnsupported
}
func (r semRead) WorkspaceBindingsByContext(string, string, string, store.Page) (store.ResultPage[domain.WorkspaceBinding], error) {
	return store.ResultPage[domain.WorkspaceBinding]{}, errUnsupported
}
func (r semRead) Observation(string) (domain.ObservationRecord, error) {
	return domain.ObservationRecord{}, errUnsupported
}
func (r semRead) ObservationRun(string) (domain.ObservationRun, error) {
	return domain.ObservationRun{}, errUnsupported
}
func (r semRead) RunsBySubject(string, store.Page) (store.ResultPage[domain.ObservationRun], error) {
	return store.ResultPage[domain.ObservationRun]{}, errUnsupported
}
func (r semRead) ObservationsByRun(string, store.Page) (store.ResultPage[domain.ObservationRecord], error) {
	return store.ResultPage[domain.ObservationRecord]{}, errUnsupported
}
func (r semRead) SubjectState(string, string, domain.AccessBoundary) (domain.SubjectState, error) {
	return domain.SubjectState{}, errUnsupported
}
func (r semRead) SubjectStatesByResource(string, store.Page) (store.ResultPage[domain.SubjectState], error) {
	return store.ResultPage[domain.SubjectState]{}, errUnsupported
}
func (t *semTx) InsertResourceBinding(domain.ResourceBinding) error { return errUnsupported }
func (t *semTx) InsertResourceUpdate(domain.ResourceUpdate) error   { return errUnsupported }
func (t *semTx) PutResourceState(domain.ResourceState, uint64) (domain.ResourceState, error) {
	return domain.ResourceState{}, errUnsupported
}
func (t *semTx) PutResourcePathState(domain.ResourcePathState, uint64) (domain.ResourcePathState, error) {
	return domain.ResourcePathState{}, errUnsupported
}
func (t *semTx) InsertWorkspaceBinding(domain.WorkspaceBinding) error { return errUnsupported }
func (t *semTx) InsertObservationRun(domain.ObservationRun) error     { return errUnsupported }
func (t *semTx) InsertObservation(domain.ObservationRecord) error     { return errUnsupported }
func (t *semTx) PutSubjectState(domain.SubjectState, uint64, string) (domain.SubjectState, error) {
	return domain.SubjectState{}, errUnsupported
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
