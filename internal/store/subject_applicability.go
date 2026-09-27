package store

import (
	"errors"
	"path"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// SubjectApplicability derives whether a subject state's observation
// describes the current authoritative resource state (DUR-3.1 (B), L1,
// P3-22): the one rule planning, eligibility, retrieval and reevaluation
// use, since a state's recorded Applicability is only its value at filing.
// It is a few exact-key reads and fails closed: any read error yields
// UNKNOWN and the error, never CURRENT.
func SubjectApplicability(r SemanticReader, st domain.SubjectState) (domain.ApplicabilityState, error) {
	obs, err := r.Observation(st.ObservationID)
	if err != nil {
		return domain.ApplicabilityUnknown, err
	}
	run, err := r.ObservationRun(obs.RunID)
	if err != nil {
		return domain.ApplicabilityUnknown, err
	}
	return ObservationApplicability(r, obs, run)
}

// ObservationApplicability is SubjectApplicability for an observation and
// its run: UNKNOWN while the resource is unregistered or not KNOWN, CURRENT
// when a complete tests run's fingerprint or a complete file read's content
// equals the authoritative one, STALE otherwise.
func ObservationApplicability(r SemanticReader, obs domain.ObservationRecord, run domain.ObservationRun) (domain.ApplicabilityState, error) {
	t := run.Subject.Target
	resource := ""
	switch {
	case t.Tests != nil:
		resource = t.Tests.ResourceID
	case t.File != nil:
		resource = t.File.Locator.ResourceID
	default:
		return domain.ApplicabilityUnknown, domain.ErrInvalidRecord
	}
	rs, err := r.ResourceState(resource)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ApplicabilityUnknown, nil
	}
	if err != nil {
		return domain.ApplicabilityUnknown, err
	}
	if rs.ResourceID != resource || rs.Freshness != domain.ResourceKnown || rs.WorkspaceFingerprint == "" {
		return domain.ApplicabilityUnknown, nil
	}
	if !obs.TerminalComplete() {
		return domain.ApplicabilityStale, nil
	}
	if t.Tests != nil {
		if obs.ObservedWorkspaceFingerprint == rs.WorkspaceFingerprint {
			return domain.ApplicabilityCurrent, nil
		}
		return domain.ApplicabilityStale, nil
	}
	ps, ok, err := CurrentPathContent(r, t.File.Locator, rs)
	if err != nil {
		return domain.ApplicabilityUnknown, err
	}
	if !ok || obs.ObservedContentHash != ps.ContentHash {
		return domain.ApplicabilityStale, nil
	}
	return domain.ApplicabilityCurrent, nil
}

// CurrentPathContent returns a path's recorded content while it still
// describes the resource's current KNOWN state: recorded at a KNOWN report
// no newer than the resource, and no update after it was UNKNOWN, covered
// all paths, or named the path or a directory containing it (P3-19,
// SPEC-1.18). Content is recorded at its update's resulting revision, so
// the newest affecting update decides in one keyed read (H2, DUR-2.2).
// ok is false when there is no such current content.
func CurrentPathContent(r SemanticReader, loc domain.ResourceLocator, rs domain.ResourceState) (domain.ResourcePathState, bool, error) {
	loc, err := domain.ResourceLocatorV1(loc.ResourceID, ".", path.Join(loc.BaseDir, loc.Path))
	if err != nil {
		return domain.ResourcePathState{}, false, nil
	}
	ps, err := r.ResourcePathState(loc)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ResourcePathState{}, false, nil
	}
	if err != nil {
		return domain.ResourcePathState{}, false, err
	}
	if rs.Freshness != domain.ResourceKnown || ps.Freshness != domain.ResourceKnown || ps.ResourceRevision > rs.AuthoritativeRevision {
		return domain.ResourcePathState{}, false, nil
	}
	latest, err := r.LatestResourceUpdateAffectingPath(loc.ResourceID, loc.Path)
	if err != nil {
		// The recording update itself names the path, so none is an
		// integrity failure, never "current".
		return domain.ResourcePathState{}, false, err
	}
	if latest.ResultingAuthoritativeRevision > ps.ResourceRevision {
		return domain.ResourcePathState{}, false, nil
	}
	return ps, true, nil
}
