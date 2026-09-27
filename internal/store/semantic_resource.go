package store

import "github.com/tdavison784/context-runtime/internal/domain"

type ResourceReader interface {
	ResourceBinding(resourceID string) (domain.ResourceBinding, error)
	ResourceState(resourceID string) (domain.ResourceState, error)
	ResourceUpdate(id string) (domain.ResourceUpdate, error)
	ResourceUpdates(resourceID string, page Page) (ResultPage[domain.ResourceUpdate], error)
	ResourcePathState(locator domain.ResourceLocator) (domain.ResourcePathState, error)
	WorkspaceBinding(ref domain.WorkspaceBindingRef) (domain.WorkspaceBinding, error)
	WorkspaceBindingsByContext(sourceItemID, taskID, conversationID string, page Page) (ResultPage[domain.WorkspaceBinding], error)
	Observation(id string) (domain.ObservationRecord, error)
	ObservationRun(id string) (domain.ObservationRun, error)
	// Run ordinal is the allocated registration Seq (W4 Q-5), so Page's
	// (Seq, ID) cursor is also the deterministic pre-execution run order.
	RunsBySubject(subjectKey string, page Page) (ResultPage[domain.ObservationRun], error)
	ObservationsByRun(runID string, page Page) (ResultPage[domain.ObservationRecord], error)
	SubjectState(subjectKey, taskID string, access domain.AccessBoundary) (domain.SubjectState, error)
	SubjectStatesByResource(resourceID string, page Page) (ResultPage[domain.SubjectState], error)
}
type ResourceWriter interface {
	InsertResourceBinding(domain.ResourceBinding) error
	InsertResourceUpdate(domain.ResourceUpdate) error
	PutResourceState(domain.ResourceState, uint64) (domain.ResourceState, error)
	PutResourcePathState(domain.ResourcePathState, uint64) (domain.ResourcePathState, error)
	InsertWorkspaceBinding(domain.WorkspaceBinding) error
	InsertObservationRun(domain.ObservationRun) error
	InsertObservation(domain.ObservationRecord) error
	// Cause is an existing observation or resource-update ID. It records why
	// current applicability changed and cannot be a caller-authored string.
	PutSubjectState(state domain.SubjectState, expectedRevision uint64, causeID string) (domain.SubjectState, error)
}

// ClosesRun reports whether o is its run's closing outcome: a complete
// PASS/FAIL, or an ERROR, TIMEOUT or CANCELLED. Stores accept no
// observation of a run after its closing one (DUR-1.1, G1), matching the
// obligation service's rule.
func ClosesRun(o domain.ObservationRecord) bool {
	return o.TerminalComplete() || o.Outcome == domain.OutcomeError || o.Outcome == domain.OutcomeTimeout || o.Outcome == domain.OutcomeCancelled
}
