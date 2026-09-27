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
	// LastWorkspaceDivergenceRev is resourceID's monotone write-time
	// divergence pointer (K1 A1): the ResultingAuthoritativeRevision of
	// the latest update whose workspace fingerprint changed or whose
	// freshness became UNKNOWN, raised in that report's own O(1)
	// transaction. 0 when never raised (never an error). The pointer only
	// rises, so a WORKSPACE dependency's ResourceRevision below it is
	// invalid for good.
	LastWorkspaceDivergenceRev(resourceID string) (uint64, error)
	// LastAffectingRev is the (resourceID, key) monotone write-time
	// pointer (K1 A1): the latest revision of an update that touched key
	// with content different from the path's prior content. key is a
	// canonical resource-relative path or one of its ancestor
	// directories; "" is the ALL key, raised by every UNKNOWN and
	// ALL-paths report. A same-content path report does not raise its
	// key; 0 when never raised (never an error).
	LastAffectingRev(resourceID, key string) (uint64, error)
	// FirstWorkspaceDivergenceAfter is the earliest divergence update of
	// resourceID with resulting revision > rev (K1 A1): a keyset seek
	// over the divergence raises, never a scan; ErrNotFound when none.
	// Settlement takes its update ID as the cause (K1-api.2).
	FirstWorkspaceDivergenceAfter(resourceID string, rev uint64) (domain.ResourceUpdate, error)
	// FirstAffectingUpdateAfter is the earliest update raising
	// (resourceID, key)'s pointer with revision > rev (K1 A1): a keyset
	// seek over that exact key, never a prefix scan; ErrNotFound when
	// none.
	FirstAffectingUpdateAfter(resourceID, key string, rev uint64) (domain.ResourceUpdate, error)
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
	// Every state filed for the resource, whatever its applicability, in
	// first-filing order: applicability is a filing-time fact, not a
	// read-time filter (L1, SEC-4.11, DUR-4.7).
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
