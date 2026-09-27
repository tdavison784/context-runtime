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
	// LatestResourceUpdateAffectingPath is the newest update of resourceID
	// that may change path (ALL-paths, or naming path or an ancestor
	// directory), ErrNotFound when none: one keyed lookup per path
	// component, independent of history (H2, DUR-2.2).
	LatestResourceUpdateAffectingPath(resourceID, path string) (domain.ResourceUpdate, error)
	ResourcePathState(locator domain.ResourceLocator) (domain.ResourcePathState, error)
	WorkspaceBinding(ref domain.WorkspaceBindingRef) (domain.WorkspaceBinding, error)
	WorkspaceBindingsByContext(sourceItemID, taskID, conversationID string, page Page) (ResultPage[domain.WorkspaceBinding], error)
	// CurrentWorkspaceBindingsByContext lists each binding ID once, at its
	// latest version, while that version is in the context, in (Seq, ID)
	// order of that version: a write-time pointer, so pages count live
	// bindings, not versions (H2).
	CurrentWorkspaceBindingsByContext(sourceItemID, taskID, conversationID string, page Page) (ResultPage[domain.WorkspaceBinding], error)
	// LatestWorkspaceBinding is the latest version of binding ID id by its
	// write-time pointer, whatever its context or history, ErrNotFound when
	// the ID was never bound: one exact-key read (SEC-4.10, SPEC-4.8).
	LatestWorkspaceBinding(id string) (domain.WorkspaceBinding, error)
	Observation(id string) (domain.ObservationRecord, error)
	ObservationRun(id string) (domain.ObservationRun, error)
	// Run ordinal is the allocated registration Seq (W4 Q-5), so Page's
	// (Seq, ID) cursor is also the deterministic pre-execution run order.
	RunsBySubject(subjectKey string, page Page) (ResultPage[domain.ObservationRun], error)
	ObservationsByRun(runID string, page Page) (ResultPage[domain.ObservationRecord], error)
	// ClosingObservation is the run's closing observation (ClosesRun),
	// ErrNotFound while the run is open: one keyed lookup, independent of
	// the run's partial reports (H2, DUR-2.6).
	ClosingObservation(runID string) (domain.ObservationRecord, error)
	// SubjectHighWater is the highest run ordinal with a complete PASS or
	// FAIL (TerminalComplete) among runs of exactly (subjectKey, taskID,
	// access), ErrNotFound when none. It is raised at write time by every
	// such observation, whatever its fingerprint or applicability, and read
	// with one keyed lookup (H1, H2).
	SubjectHighWater(subjectKey, taskID string, access domain.AccessBoundary) (uint64, error)
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

// Partition is one (task, access) subject partition of a subject key.
type Partition struct {
	TaskID string
	Access domain.AccessBoundary
}

// ProofRankPartitions are the subject partitions whose high-water marks
// can outrank a proof resting on run for obligation o (H1, SEC-2.9): the
// run's own partition, and each TASK partition of o's task (workflow in
// {"", o's}, agent in {"", o's}) whose boundary covers o's, so a result
// private to another agent never outranks a proof it does not cover.
func ProofRankPartitions(run domain.ObservationRun, o domain.ObligationVersion) []Partition {
	out := []Partition{{run.TaskID, run.Access}}
	if o.TaskID == "" {
		return out
	}
	for _, wf := range uniqueOf("", o.Access.WorkflowID) {
		for _, agent := range uniqueOf("", o.Access.AgentID) {
			p := Partition{o.TaskID, domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: o.SessionID, TaskID: o.TaskID, WorkflowID: wf, AgentID: agent}}
			if o.Access.Within(p.Access) && p != out[0] {
				out = append(out, p)
			}
		}
	}
	return out
}

func uniqueOf(a, b string) []string {
	if a == b {
		return []string{a}
	}
	return []string{a, b}
}

// ProducedBy reports whether item ev was produced by tool call execution:
// its recorded source names that call (P3-21). Content without a producing
// call never evidences a run.
func ProducedBy(ev domain.ContextItem, execution string) bool {
	return ev.Source != nil && ev.Source.ToolCallID != "" && ev.Source.ToolCallID == execution
}
