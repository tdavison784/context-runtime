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

func (semRead) LifecycleByTarget(domain.TargetKind, string, store.Page) (store.ResultPage[domain.LifecycleEvent], error) {
	return store.ResultPage[domain.LifecycleEvent]{}, errUnsupported
}
func (semRead) CreationDeclaration(string) (domain.CreationDeclaration, error) {
	return domain.CreationDeclaration{}, errUnsupported
}
func (semRead) SnapshotDeclaration(string) (domain.SnapshotDeclaration, error) {
	return domain.SnapshotDeclaration{}, errUnsupported
}
func (semRead) GrantsFor(domain.Action, domain.GrantTarget, int) ([]domain.MutationGrant, error) {
	return nil, errUnsupported
}
func (semRead) OpenGoalsByTaskOwner(string, store.Page) (store.ResultPage[domain.ContextItem], error) {
	return store.ResultPage[domain.ContextItem]{}, errUnsupported
}
func (semRead) SemanticChanges(domain.Principal, domain.GrantTarget, store.Page) (store.ResultPage[domain.SemanticChange], error) {
	return store.ResultPage[domain.SemanticChange]{}, errUnsupported
}
func (semTx) InsertCreationDeclaration(domain.CreationDeclaration) error { return errUnsupported }
func (semTx) InsertSnapshotDeclaration(domain.SnapshotDeclaration) error { return errUnsupported }
func (semTx) SetCurrentVersion(string, string) error                     { return errUnsupported }
func (semTx) InsertSemanticChange(domain.SemanticChange) error           { return errUnsupported }

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

// Resources and observations.

func (semRead) ResourceBinding(string) (domain.ResourceBinding, error) {
	return domain.ResourceBinding{}, errUnsupported
}
func (semRead) ResourceState(string) (domain.ResourceState, error) {
	return domain.ResourceState{}, errUnsupported
}
func (semRead) ResourceUpdate(string) (domain.ResourceUpdate, error) {
	return domain.ResourceUpdate{}, errUnsupported
}
func (semRead) ResourceUpdates(string, store.Page) (store.ResultPage[domain.ResourceUpdate], error) {
	return store.ResultPage[domain.ResourceUpdate]{}, errUnsupported
}
func (semRead) ResourcePathState(domain.ResourceLocator) (domain.ResourcePathState, error) {
	return domain.ResourcePathState{}, errUnsupported
}
func (semRead) WorkspaceBinding(domain.WorkspaceBindingRef) (domain.WorkspaceBinding, error) {
	return domain.WorkspaceBinding{}, errUnsupported
}
func (semRead) WorkspaceBindingsByContext(string, string, string, store.Page) (store.ResultPage[domain.WorkspaceBinding], error) {
	return store.ResultPage[domain.WorkspaceBinding]{}, errUnsupported
}
func (semRead) Observation(string) (domain.ObservationRecord, error) {
	return domain.ObservationRecord{}, errUnsupported
}
func (semRead) ObservationRun(string) (domain.ObservationRun, error) {
	return domain.ObservationRun{}, errUnsupported
}
func (semRead) RunsBySubject(string, store.Page) (store.ResultPage[domain.ObservationRun], error) {
	return store.ResultPage[domain.ObservationRun]{}, errUnsupported
}
func (semRead) ObservationsByRun(string, store.Page) (store.ResultPage[domain.ObservationRecord], error) {
	return store.ResultPage[domain.ObservationRecord]{}, errUnsupported
}
func (semRead) SubjectState(string, string, domain.AccessBoundary) (domain.SubjectState, error) {
	return domain.SubjectState{}, errUnsupported
}
func (semRead) SubjectStatesByResource(string, store.Page) (store.ResultPage[domain.SubjectState], error) {
	return store.ResultPage[domain.SubjectState]{}, errUnsupported
}
func (semTx) InsertResourceBinding(domain.ResourceBinding) error { return errUnsupported }
func (semTx) InsertResourceUpdate(domain.ResourceUpdate) error   { return errUnsupported }
func (semTx) PutResourceState(domain.ResourceState, uint64) (domain.ResourceState, error) {
	return domain.ResourceState{}, errUnsupported
}
func (semTx) PutResourcePathState(domain.ResourcePathState, uint64) (domain.ResourcePathState, error) {
	return domain.ResourcePathState{}, errUnsupported
}
func (semTx) InsertWorkspaceBinding(domain.WorkspaceBinding) error { return errUnsupported }
func (semTx) InsertObservationRun(domain.ObservationRun) error     { return errUnsupported }
func (semTx) InsertObservation(domain.ObservationRecord) error     { return errUnsupported }
func (semTx) PutSubjectState(domain.SubjectState, uint64, string) (domain.SubjectState, error) {
	return domain.SubjectState{}, errUnsupported
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
