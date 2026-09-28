package store

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// applicabilityReader serves the few exact reads SubjectApplicability makes,
// failing any of them on demand.
type applicabilityReader struct {
	SemanticReader
	obs    domain.ObservationRecord
	run    domain.ObservationRun
	rs     *domain.ResourceState
	ps     *domain.ResourcePathState
	latest domain.ResourceUpdate
	fail   string
}

var errRead = errors.New("injected read failure")

func (r *applicabilityReader) Observation(string) (domain.ObservationRecord, error) {
	if r.fail == "observation" {
		return domain.ObservationRecord{}, errRead
	}
	return r.obs, nil
}
func (r *applicabilityReader) ObservationRun(string) (domain.ObservationRun, error) {
	if r.fail == "run" {
		return domain.ObservationRun{}, errRead
	}
	return r.run, nil
}
func (r *applicabilityReader) ResourceState(string) (domain.ResourceState, error) {
	if r.fail == "resource" {
		return domain.ResourceState{}, errRead
	}
	if r.rs == nil {
		return domain.ResourceState{}, domain.ErrNotFound
	}
	return *r.rs, nil
}
func (r *applicabilityReader) ResourcePathState(domain.ResourceLocator) (domain.ResourcePathState, error) {
	if r.fail == "path" {
		return domain.ResourcePathState{}, errRead
	}
	if r.ps == nil {
		return domain.ResourcePathState{}, domain.ErrNotFound
	}
	return *r.ps, nil
}
func (r *applicabilityReader) LatestResourceUpdateAffectingPath(string, string) (domain.ResourceUpdate, error) {
	if r.fail == "latest" {
		return domain.ResourceUpdate{}, errRead
	}
	return r.latest, nil
}

// L1 (SPEC-4.10, SEC-4.11): the shared subject-applicability rule is exact,
// and fails closed: any read error yields a non-CURRENT state and the error.
func TestSubjectApplicabilityIsExactAndFailsClosed(t *testing.T) {
	complete := domain.ObservationRecord{Outcome: domain.OutcomePass, Completeness: domain.ObservationComplete, RunID: "run"}
	known := func(fp string, rev uint64) *domain.ResourceState {
		return &domain.ResourceState{ResourceID: "repo", Freshness: domain.ResourceKnown, WorkspaceFingerprint: fp, AuthoritativeRevision: rev}
	}
	testsRun := domain.ObservationRun{Subject: domain.ObservationSubject{Family: domain.ObservationTests, Target: domain.TargetSpec{Tests: &domain.TestsTarget{ResourceID: "repo"}}}}
	loc := domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: "docs/a.md"}
	fileRun := domain.ObservationRun{Subject: domain.ObservationSubject{Family: domain.ObservationFileRead, Target: domain.TargetSpec{File: &domain.FileTarget{Locator: loc, Mode: domain.FileCurrentContent}}}}
	testsObs := complete
	testsObs.ObservedWorkspaceFingerprint = "fp1"
	fileObs := complete
	fileObs.ObservedContentHash = "h1"
	path := func(hash string, rev uint64) *domain.ResourcePathState {
		return &domain.ResourcePathState{Locator: loc, ContentHash: hash, ResourceRevision: rev, Freshness: domain.ResourceKnown}
	}
	for name, c := range map[string]struct {
		r    applicabilityReader
		want domain.ApplicabilityState
		err  bool
	}{
		"tests current":         {r: applicabilityReader{obs: testsObs, run: testsRun, rs: known("fp1", 3)}, want: domain.ApplicabilityCurrent},
		"tests stale":           {r: applicabilityReader{obs: testsObs, run: testsRun, rs: known("fp2", 4)}, want: domain.ApplicabilityStale},
		"resource unknown":      {r: applicabilityReader{obs: testsObs, run: testsRun, rs: &domain.ResourceState{ResourceID: "repo", Freshness: domain.ResourceUnknown}}, want: domain.ApplicabilityUnknown},
		"resource unregistered": {r: applicabilityReader{obs: testsObs, run: testsRun}, want: domain.ApplicabilityUnknown},
		"file current":          {r: applicabilityReader{obs: fileObs, run: fileRun, rs: known("fp1", 5), ps: path("h1", 4), latest: domain.ResourceUpdate{ResultingAuthoritativeRevision: 4}}, want: domain.ApplicabilityCurrent},
		"file path changed":     {r: applicabilityReader{obs: fileObs, run: fileRun, rs: known("fp1", 5), ps: path("h1", 4), latest: domain.ResourceUpdate{ResultingAuthoritativeRevision: 5}}, want: domain.ApplicabilityStale},
		"file other content":    {r: applicabilityReader{obs: fileObs, run: fileRun, rs: known("fp1", 5), ps: path("h2", 4), latest: domain.ResourceUpdate{ResultingAuthoritativeRevision: 4}}, want: domain.ApplicabilityStale},
		"file never recorded":   {r: applicabilityReader{obs: fileObs, run: fileRun, rs: known("fp1", 5)}, want: domain.ApplicabilityStale},
		"observation fails":     {r: applicabilityReader{obs: testsObs, run: testsRun, rs: known("fp1", 3), fail: "observation"}, err: true},
		"run fails":             {r: applicabilityReader{obs: testsObs, run: testsRun, rs: known("fp1", 3), fail: "run"}, err: true},
		"resource fails":        {r: applicabilityReader{obs: testsObs, run: testsRun, rs: known("fp1", 3), fail: "resource"}, err: true},
		"path fails":            {r: applicabilityReader{obs: fileObs, run: fileRun, rs: known("fp1", 5), ps: path("h1", 4), fail: "path"}, err: true},
		"latest fails":          {r: applicabilityReader{obs: fileObs, run: fileRun, rs: known("fp1", 5), ps: path("h1", 4), fail: "latest"}, err: true},
	} {
		got, err := SubjectApplicability(&c.r, domain.SubjectState{ObservationID: "obs"})
		if c.err {
			if err == nil || got == domain.ApplicabilityCurrent {
				t.Errorf("%s: %s, %v; want a non-CURRENT state and the error", name, got, err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s: %s, %v; want %s", name, got, err, c.want)
		}
	}
}

// TestCurrentPathContentUnknownResourceState pins CurrentPathContent's
// UNKNOWN guard (SPEC-6.6(b), TEST-6.3): a path state recorded KNOWN must
// not read as current content while the resource's state is UNKNOWN — the
// recording update's report was a gap, so the recorded content describes
// the resource only as of a state that no longer holds. The construction
// keeps every other refusal inert (the path revision equals the resource's
// authoritative revision and the latest affecting update is no newer), so
// the resource-freshness conjunct alone decides.
func TestCurrentPathContentUnknownResourceState(t *testing.T) {
	loc := domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: "docs/a.md"}
	ps := domain.ResourcePathState{Locator: loc, ContentHash: "h1", ResourceRevision: 4, Freshness: domain.ResourceKnown}
	latest := domain.ResourceUpdate{ResultingAuthoritativeRevision: 4}
	// Control: a KNOWN resource state at the same revision reads current.
	known := applicabilityReader{ps: &ps, latest: latest,
		rs: &domain.ResourceState{ResourceID: "repo", Freshness: domain.ResourceKnown, AuthoritativeRevision: 4}}
	if _, ok, err := CurrentPathContent(&known, loc, *known.rs); err != nil || !ok {
		t.Errorf("KNOWN resource state: ok = %v, err = %v; want current content", ok, err)
	}
	// SPEC-6.6(b): the UNKNOWN resource state with the same KNOWN path
	// state must refuse, though nothing else does.
	unknown := applicabilityReader{ps: &ps, latest: latest,
		rs: &domain.ResourceState{ResourceID: "repo", Freshness: domain.ResourceUnknown, AuthoritativeRevision: 4}}
	if _, ok, err := CurrentPathContent(&unknown, loc, *unknown.rs); err != nil || ok {
		t.Errorf("UNKNOWN resource state, KNOWN path state: ok = %v, err = %v; want not current", ok, err)
	}
}
