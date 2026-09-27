package store

import (
	"fmt"
	path "path"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
)

type ResourceReader interface {
	ResourceBinding(resourceID string) (domain.ResourceBinding, error)
	ResourceState(resourceID string) (domain.ResourceState, error)
	ResourceUpdate(id string) (domain.ResourceUpdate, error)
	ResourceUpdates(resourceID string, page Page) (ResultPage[domain.ResourceUpdate], error)
	// ResourceUpdatesAffectingPath pages, in (Seq, ID) order, only the
	// updates of resourceID that may change the canonical resource-relative
	// path: ALL-paths (including every UNKNOWN) updates and updates naming
	// path or one of its ancestor directories. Its cost scales with those,
	// not with unrelated edits (G2, SEC-1.7, DUR-1.2).
	ResourceUpdatesAffectingPath(resourceID, path string, page Page) (ResultPage[domain.ResourceUpdate], error)
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
	// Only CURRENT states, in first-filing order: STALE/UNKNOWN history is
	// not a live dependent and never counts toward a page (G2, SEC-1.8).
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

// PathAffectKeys returns the ChangedPaths entries that affect the canonical
// resource-relative path p: p itself and each of its ancestor directories,
// shallowest first. A noncanonical p is ErrInvalidRecord.
func PathAffectKeys(p string) ([]string, error) {
	if p == "" || p == "." || p == ".." || path.IsAbs(p) || strings.HasPrefix(p, "../") || path.Clean(p) != p {
		return nil, fmt.Errorf("%w: noncanonical resource path %q", domain.ErrInvalidRecord, p)
	}
	var keys []string
	for i, c := range p {
		if c == '/' {
			keys = append(keys, p[:i])
		}
	}
	return append(keys, p), nil
}
