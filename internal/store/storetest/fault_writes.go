// Code generated from the store write interfaces; DO NOT EDIT BY HAND.
// Regenerate when a write method is added: TestFaultWrappersComplete fails
// until every write of store.Tx and store.SemanticTx is intercepted.

package storetest

import "github.com/tdavison784/context-runtime/internal/domain"

var _ = domain.ErrInvalidRecord

func (f *faultTx) InsertEvent(a0 domain.EventRecord) (domain.EventRecord, bool, error) {
	if err := f.hit("InsertEvent"); err != nil {
		return *new(domain.EventRecord), *new(bool), err
	}
	return f.Tx.InsertEvent(a0)
}

func (f *faultTx) InsertIngestion(a0 domain.EventEnvelope, a1 domain.IngestReceipt) error {
	if err := f.hit("InsertIngestion"); err != nil {
		return err
	}
	return f.Tx.InsertIngestion(a0, a1)
}

func (f *faultTx) InsertUnresolvedReference(a0 domain.UnresolvedReference) error {
	if err := f.hit("InsertUnresolvedReference"); err != nil {
		return err
	}
	return f.Tx.InsertUnresolvedReference(a0)
}

func (f *faultTx) InsertItem(a0 domain.ContextItem) error {
	if err := f.hit("InsertItem"); err != nil {
		return err
	}
	return f.Tx.InsertItem(a0)
}

func (f *faultTx) UpdateItem(a0 string, a1 uint64, a2 domain.ItemChange, a3 domain.LifecycleEvent) (domain.ContextItem, error) {
	if err := f.hit("UpdateItem"); err != nil {
		return *new(domain.ContextItem), err
	}
	return f.Tx.UpdateItem(a0, a1, a2, a3)
}

func (f *faultTx) InsertRelationship(a0 domain.Relationship) error {
	if err := f.hit("InsertRelationship"); err != nil {
		return err
	}
	return f.Tx.InsertRelationship(a0)
}

func (f *faultTx) InsertBlob(a0 domain.Blob) error {
	if err := f.hit("InsertBlob"); err != nil {
		return err
	}
	return f.Tx.InsertBlob(a0)
}

func (f *faultTx) InsertObligationVersion(a0 domain.ObligationVersion) error {
	if err := f.hit("InsertObligationVersion"); err != nil {
		return err
	}
	return f.Tx.InsertObligationVersion(a0)
}

func (f *faultTx) UpdateObligationVersion(a0 domain.ObligationVersion, a1 uint64) (domain.ObligationVersion, error) {
	if err := f.hit("UpdateObligationVersion"); err != nil {
		return *new(domain.ObligationVersion), err
	}
	return f.Tx.UpdateObligationVersion(a0, a1)
}

func (f *faultTx) RetireObligationVersion(a0 string, a1 uint64, a2 uint64, a3 domain.LifecycleEvent) (domain.ObligationVersion, error) {
	if err := f.hit("RetireObligationVersion"); err != nil {
		return *new(domain.ObligationVersion), err
	}
	return f.Tx.RetireObligationVersion(a0, a1, a2, a3)
}

func (f *faultTx) AppendObligationTransition(a0 domain.ObligationTransition, a1 uint64) (domain.ObligationVersion, error) {
	if err := f.hit("AppendObligationTransition"); err != nil {
		return *new(domain.ObligationVersion), err
	}
	return f.Tx.AppendObligationTransition(a0, a1)
}

func (f *faultTx) InsertGrant(a0 domain.MutationGrant) error {
	if err := f.hit("InsertGrant"); err != nil {
		return err
	}
	return f.Tx.InsertGrant(a0)
}

func (f *faultTx) RevokeGrant(a0 string, a1 domain.LifecycleEvent) (domain.MutationGrant, error) {
	if err := f.hit("RevokeGrant"); err != nil {
		return *new(domain.MutationGrant), err
	}
	return f.Tx.RevokeGrant(a0, a1)
}

func (f *faultTx) PutTask(a0 domain.TaskState, a1 uint64, a2 domain.LifecycleEvent) (domain.TaskState, error) {
	if err := f.hit("PutTask"); err != nil {
		return *new(domain.TaskState), err
	}
	return f.Tx.PutTask(a0, a1, a2)
}

func (f *faultTx) AppendLifecycleEvent(a0 domain.LifecycleEvent) error {
	if err := f.hit("AppendLifecycleEvent"); err != nil {
		return err
	}
	return f.Tx.AppendLifecycleEvent(a0)
}

func (f *faultTx) PutConversation(a0 domain.Conversation, a1 uint64) (domain.Conversation, error) {
	if err := f.hit("PutConversation"); err != nil {
		return *new(domain.Conversation), err
	}
	return f.Tx.PutConversation(a0, a1)
}

func (f *faultTx) InsertCall(a0 domain.CallRecord) error {
	if err := f.hit("InsertCall"); err != nil {
		return err
	}
	return f.Tx.InsertCall(a0)
}

func (f *faultTx) UpdateCall(a0 domain.CallRecord, a1 uint64) (domain.CallRecord, error) {
	if err := f.hit("UpdateCall"); err != nil {
		return *new(domain.CallRecord), err
	}
	return f.Tx.UpdateCall(a0, a1)
}

func (f *faultTx) PutCallAttempt(a0 domain.CallAttempt) error {
	if err := f.hit("PutCallAttempt"); err != nil {
		return err
	}
	return f.Tx.PutCallAttempt(a0)
}

func (f *faultSemantic) InsertCreationDeclaration(a0 domain.CreationDeclaration) error {
	if err := f.hit("InsertCreationDeclaration"); err != nil {
		return err
	}
	return f.SemanticTx.InsertCreationDeclaration(a0)
}

func (f *faultSemantic) InsertSnapshotDeclaration(a0 domain.SnapshotDeclaration) error {
	if err := f.hit("InsertSnapshotDeclaration"); err != nil {
		return err
	}
	return f.SemanticTx.InsertSnapshotDeclaration(a0)
}

func (f *faultSemantic) InsertCoverage(a0 domain.CoverageRecord, a1 []domain.CoverageMember) error {
	if err := f.hit("InsertCoverage"); err != nil {
		return err
	}
	return f.SemanticTx.InsertCoverage(a0, a1)
}

func (f *faultSemantic) SetCurrentVersion(a0 string, a1 string) error {
	if err := f.hit("SetCurrentVersion"); err != nil {
		return err
	}
	return f.SemanticTx.SetCurrentVersion(a0, a1)
}

func (f *faultSemantic) InsertSemanticChange(a0 domain.SemanticChange) error {
	if err := f.hit("InsertSemanticChange"); err != nil {
		return err
	}
	return f.SemanticTx.InsertSemanticChange(a0)
}

func (f *faultSemantic) InsertObligationDeclaration(a0 domain.ObligationDeclaration) error {
	if err := f.hit("InsertObligationDeclaration"); err != nil {
		return err
	}
	return f.SemanticTx.InsertObligationDeclaration(a0)
}

func (f *faultSemantic) InsertApplicabilityProof(a0 domain.ApplicabilityProof, a1 []domain.ProofDependency) error {
	if err := f.hit("InsertApplicabilityProof"); err != nil {
		return err
	}
	return f.SemanticTx.InsertApplicabilityProof(a0, a1)
}

func (f *faultSemantic) InsertAssertion(a0 domain.AssertionRecord) error {
	if err := f.hit("InsertAssertion"); err != nil {
		return err
	}
	return f.SemanticTx.InsertAssertion(a0)
}

func (f *faultSemantic) AppendSemanticObligationTransition(a0 domain.ObligationTransition, a1 domain.TransitionDetail, a2 uint64) (domain.ObligationVersion, error) {
	if err := f.hit("AppendSemanticObligationTransition"); err != nil {
		return *new(domain.ObligationVersion), err
	}
	return f.SemanticTx.AppendSemanticObligationTransition(a0, a1, a2)
}

func (f *faultSemantic) SetObligationMaterialization(a0 domain.ObligationRef, a1 bool, a2 uint64, a3 domain.LifecycleEvent) (domain.ObligationVersion, error) {
	if err := f.hit("SetObligationMaterialization"); err != nil {
		return *new(domain.ObligationVersion), err
	}
	return f.SemanticTx.SetObligationMaterialization(a0, a1, a2, a3)
}

func (f *faultSemantic) InsertResourceBinding(a0 domain.ResourceBinding) error {
	if err := f.hit("InsertResourceBinding"); err != nil {
		return err
	}
	return f.SemanticTx.InsertResourceBinding(a0)
}

func (f *faultSemantic) InsertResourceUpdate(a0 domain.ResourceUpdate) error {
	if err := f.hit("InsertResourceUpdate"); err != nil {
		return err
	}
	return f.SemanticTx.InsertResourceUpdate(a0)
}

func (f *faultSemantic) PutResourceState(a0 domain.ResourceState, a1 uint64) (domain.ResourceState, error) {
	if err := f.hit("PutResourceState"); err != nil {
		return *new(domain.ResourceState), err
	}
	return f.SemanticTx.PutResourceState(a0, a1)
}

func (f *faultSemantic) PutResourcePathState(a0 domain.ResourcePathState, a1 uint64) (domain.ResourcePathState, error) {
	if err := f.hit("PutResourcePathState"); err != nil {
		return *new(domain.ResourcePathState), err
	}
	return f.SemanticTx.PutResourcePathState(a0, a1)
}

func (f *faultSemantic) InsertWorkspaceBinding(a0 domain.WorkspaceBinding) error {
	if err := f.hit("InsertWorkspaceBinding"); err != nil {
		return err
	}
	return f.SemanticTx.InsertWorkspaceBinding(a0)
}

func (f *faultSemantic) InsertObservationRun(a0 domain.ObservationRun) error {
	if err := f.hit("InsertObservationRun"); err != nil {
		return err
	}
	return f.SemanticTx.InsertObservationRun(a0)
}

func (f *faultSemantic) InsertObservation(a0 domain.ObservationRecord) error {
	if err := f.hit("InsertObservation"); err != nil {
		return err
	}
	return f.SemanticTx.InsertObservation(a0)
}

func (f *faultSemantic) PutSubjectState(a0 domain.SubjectState, a1 uint64, a2 string) (domain.SubjectState, error) {
	if err := f.hit("PutSubjectState"); err != nil {
		return *new(domain.SubjectState), err
	}
	return f.SemanticTx.PutSubjectState(a0, a1, a2)
}

func (f *faultSemantic) InsertLogicalExchange(a0 domain.LogicalExchange) error {
	if err := f.hit("InsertLogicalExchange"); err != nil {
		return err
	}
	return f.SemanticTx.InsertLogicalExchange(a0)
}

func (f *faultSemantic) PutLogicalExchange(a0 domain.LogicalExchange, a1 uint64) (domain.LogicalExchange, error) {
	if err := f.hit("PutLogicalExchange"); err != nil {
		return *new(domain.LogicalExchange), err
	}
	return f.SemanticTx.PutLogicalExchange(a0, a1)
}

func (f *faultSemantic) InsertExchangeMember(a0 domain.ExchangeMember) error {
	if err := f.hit("InsertExchangeMember"); err != nil {
		return err
	}
	return f.SemanticTx.InsertExchangeMember(a0)
}

func (f *faultSemantic) InsertExchangeAcknowledgment(a0 domain.ExchangeAcknowledgment) error {
	if err := f.hit("InsertExchangeAcknowledgment"); err != nil {
		return err
	}
	return f.SemanticTx.InsertExchangeAcknowledgment(a0)
}

func (f *faultSemantic) InsertAdmissionManifest(a0 domain.AdmissionManifest) error {
	if err := f.hit("InsertAdmissionManifest"); err != nil {
		return err
	}
	return f.SemanticTx.InsertAdmissionManifest(a0)
}

func (f *faultSemantic) PutConversationMembership(a0 domain.ConversationMembershipState, a1 uint64) (domain.ConversationMembershipState, error) {
	if err := f.hit("PutConversationMembership"); err != nil {
		return *new(domain.ConversationMembershipState), err
	}
	return f.SemanticTx.PutConversationMembership(a0, a1)
}

func (f *faultSemantic) InsertCheckpoint(a0 domain.Checkpoint) error {
	if err := f.hit("InsertCheckpoint"); err != nil {
		return err
	}
	return f.SemanticTx.InsertCheckpoint(a0)
}

func (f *faultSemantic) InsertOwnerRegistration(a0 domain.OwnerRegistration) error {
	if err := f.hit("InsertOwnerRegistration"); err != nil {
		return err
	}
	return f.SemanticTx.InsertOwnerRegistration(a0)
}

func (f *faultSemantic) InsertRetrievalLease(a0 domain.RetrievalLease) error {
	if err := f.hit("InsertRetrievalLease"); err != nil {
		return err
	}
	return f.SemanticTx.InsertRetrievalLease(a0)
}

func (f *faultSemantic) InsertRetrievalResult(a0 domain.RetrievalResult) error {
	if err := f.hit("InsertRetrievalResult"); err != nil {
		return err
	}
	return f.SemanticTx.InsertRetrievalResult(a0)
}

func (f *faultSemantic) InsertRetrievalEvent(a0 domain.RetrievalEvent) error {
	if err := f.hit("InsertRetrievalEvent"); err != nil {
		return err
	}
	return f.SemanticTx.InsertRetrievalEvent(a0)
}

func (f *faultSemantic) InsertProjection(a0 domain.ProjectionRecord) error {
	if err := f.hit("InsertProjection"); err != nil {
		return err
	}
	return f.SemanticTx.InsertProjection(a0)
}

func (f *faultSemantic) InsertMutationReceipt(a0 domain.MutationReceipt) error {
	if err := f.hit("InsertMutationReceipt"); err != nil {
		return err
	}
	return f.SemanticTx.InsertMutationReceipt(a0)
}

func (f *faultSemantic) InsertToolExecutionReceipt(a0 domain.ToolExecutionReceipt) error {
	if err := f.hit("InsertToolExecutionReceipt"); err != nil {
		return err
	}
	return f.SemanticTx.InsertToolExecutionReceipt(a0)
}

func (f *faultSemantic) InsertGCRequest(a0 domain.GCRequest) error {
	if err := f.hit("InsertGCRequest"); err != nil {
		return err
	}
	return f.SemanticTx.InsertGCRequest(a0)
}

func (f *faultSemantic) PutGCProgress(a0 domain.GCProgress, a1 uint64) (domain.GCProgress, error) {
	if err := f.hit("PutGCProgress"); err != nil {
		return *new(domain.GCProgress), err
	}
	return f.SemanticTx.PutGCProgress(a0, a1)
}

func (f *faultSemantic) InsertGCResult(a0 domain.GCResult) error {
	if err := f.hit("InsertGCResult"); err != nil {
		return err
	}
	return f.SemanticTx.InsertGCResult(a0)
}

func (f *faultSemantic) InsertCollectReceipt(a0 domain.CollectReceipt) error {
	if err := f.hit("InsertCollectReceipt"); err != nil {
		return err
	}
	return f.SemanticTx.InsertCollectReceipt(a0)
}
