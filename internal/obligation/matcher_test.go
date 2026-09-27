package obligation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func knownState(resource, fp string, rev uint64) *domain.ResourceState {
	return &domain.ResourceState{ResourceID: resource, Freshness: domain.ResourceKnown, WorkspaceFingerprint: fp, AuthoritativeRevision: rev}
}

func testsInput(mod func(*EvalInput)) EvalInput {
	target := testsTarget(nil)
	in := EvalInput{
		Target:     target,
		SubjectKey: mustSubjectKey(target),
		Observation: domain.ObservationRecord{
			Family:                       domain.ObservationTests,
			SubjectKey:                   mustSubjectKey(target),
			ObservedWorkspaceFingerprint: hashOf("W1"),
			Outcome:                      domain.OutcomePass,
			Completeness:                 domain.ObservationComplete,
		},
		Ordinal:  10,
		Resource: knownState("repo1", hashOf("W1"), 3),
	}
	if mod != nil {
		mod(&in)
	}
	return in
}

func TestTestsPassVerdicts(t *testing.T) {
	m := testsPass{}
	pass := m.Evaluate(testsInput(nil))
	if pass.Kind != VerdictPass {
		t.Fatalf("applicable PASS = %+v", pass)
	}
	want := domain.ResourceClaim{Kind: domain.DependencyWorkspace, ResourceID: "repo1", ResourceRevision: 3, Fingerprint: hashOf("W1")}
	if pass.Dependency.Kind != want.Kind || pass.Dependency.ResourceID != want.ResourceID || pass.Dependency.ResourceRevision != want.ResourceRevision || pass.Dependency.Fingerprint != want.Fingerprint || pass.Dependency.Locator != nil {
		t.Errorf("dependency = %+v, want %+v", pass.Dependency, want)
	}
	if v := m.Evaluate(testsInput(func(in *EvalInput) { in.Observation.Outcome = domain.OutcomeFail })); v.Kind != VerdictFail {
		t.Errorf("applicable FAIL = %+v", v)
	}

	otherSubject := func(mod func(*domain.TestsTarget)) func(*EvalInput) {
		return func(in *EvalInput) { in.Observation.SubjectKey = mustSubjectKey(testsTarget(mod)) }
	}
	cases := map[string]struct {
		mod  func(*EvalInput)
		want InapplicableReason
	}{
		// T07 repeat cases: same command elsewhere or with less coverage.
		"other repository":  {otherSubject(func(v *domain.TestsTarget) { v.ResourceID = "repo2" }), InapplicableSubject},
		"other directory":   {otherSubject(func(v *domain.TestsTarget) { v.WorkingDir = "svc" }), InapplicableSubject},
		"other environment": {otherSubject(func(v *domain.TestsTarget) { v.EnvironmentSpec = "env2" }), InapplicableSubject},
		"other suite":       {otherSubject(func(v *domain.TestsTarget) { v.SuiteSpec = "unit" }), InapplicableSubject},
		"declared subset":   {otherSubject(func(v *domain.TestsTarget) { v.CoverageSpec = "subset" }), InapplicableSubject},
		"unbound subject":   {func(in *EvalInput) { in.SubjectKey = "" }, InapplicableSubject},
		"file family":       {func(in *EvalInput) { in.Observation.Family = domain.ObservationFileRead }, InapplicableFamily},
		"partial":           {func(in *EvalInput) { in.Observation.Completeness = domain.ObservationPartial }, InapplicableIncomplete},
		"timeout":           {func(in *EvalInput) { in.Observation.Outcome = domain.OutcomeTimeout }, InapplicableIncomplete},
		"error":             {func(in *EvalInput) { in.Observation.Outcome = domain.OutcomeError }, InapplicableIncomplete},
		"cancelled":         {func(in *EvalInput) { in.Observation.Outcome = domain.OutcomeCancelled }, InapplicableIncomplete},
		"older run":         {func(in *EvalInput) { in.Watermark = 11 }, InapplicableStaleRun},
		"no ordinal":        {func(in *EvalInput) { in.Ordinal = 0 }, InapplicableStaleRun},
		"no resource state": {func(in *EvalInput) { in.Resource = nil }, InapplicableUnknown},
		"unknown freshness": {func(in *EvalInput) {
			in.Resource = &domain.ResourceState{ResourceID: "repo1", Freshness: domain.ResourceUnknown, AuthoritativeRevision: 4}
		}, InapplicableUnknown},
		"other resource state": {func(in *EvalInput) { in.Resource = knownState("repo2", hashOf("W1"), 3) }, InapplicableUnknown},
		// T07: W2 reported before the next plan; the W1 run is stale.
		"stale fingerprint": {func(in *EvalInput) { in.Resource = knownState("repo1", hashOf("W2"), 4) }, InapplicableFingerprint},
		"stale FAIL": {func(in *EvalInput) {
			in.Observation.Outcome = domain.OutcomeFail
			in.Resource = knownState("repo1", hashOf("W2"), 4)
		}, InapplicableFingerprint},
	}
	for name, c := range cases {
		v := m.Evaluate(testsInput(c.mod))
		if v.Kind != VerdictNotApplicable || v.Reason != c.want || v.Dependency != (domain.ResourceClaim{}) {
			t.Errorf("%s: verdict %+v, want NOT_APPLICABLE/%s", name, v, c.want)
		}
	}
	// The same accepted run re-evaluated (ordinal == watermark) still applies.
	if v := m.Evaluate(testsInput(func(in *EvalInput) { in.Watermark = 10 })); v.Kind != VerdictPass {
		t.Errorf("reevaluating the accepted run = %+v", v)
	}
}

func fileInput(mode domain.FileContentMode, required string, mod func(*EvalInput)) EvalInput {
	target := fileTarget("repo1", "docs/a.md", mode, required)
	loc := target.File.Locator
	in := EvalInput{
		Target:     target,
		SubjectKey: mustSubjectKey(target),
		Observation: domain.ObservationRecord{
			Family:              domain.ObservationFileRead,
			SubjectKey:          mustSubjectKey(target),
			ObservedContentHash: hashOf("v1"),
			Outcome:             domain.OutcomePass,
			Completeness:        domain.ObservationComplete,
		},
		Ordinal:  5,
		Resource: knownState("repo1", hashOf("W1"), 3),
		Path:     &domain.ResourcePathState{Locator: loc, ContentHash: hashOf("v1"), ResourceRevision: 2, Freshness: domain.ResourceKnown},
	}
	if mod != nil {
		mod(&in)
	}
	return in
}

func TestFileReadCurrentContent(t *testing.T) {
	m := fileRead{}
	v := m.Evaluate(fileInput(domain.FileCurrentContent, "", nil))
	if v.Kind != VerdictPass || v.Dependency.Kind != domain.DependencyCurrentPath || v.Dependency.ResourceRevision != 2 || v.Dependency.Fingerprint != hashOf("v1") || v.Dependency.Locator == nil || v.Dependency.Locator.Path != "docs/a.md" {
		t.Fatalf("current read = %+v", v)
	}
	for name, c := range map[string]struct {
		mod  func(*EvalInput)
		want InapplicableReason
	}{
		"edited since read": {func(in *EvalInput) { in.Path.ContentHash = hashOf("v2") }, InapplicableContent},
		"no path state":     {func(in *EvalInput) { in.Path = nil }, InapplicableUnknown},
		"unknown path": {func(in *EvalInput) {
			in.Path.Freshness, in.Path.ContentHash = domain.ResourceUnknown, ""
		}, InapplicableUnknown},
		"other path state": {func(in *EvalInput) { in.Path.Locator.Path = "docs/b.md" }, InapplicableUnknown},
		"resource unknown": {func(in *EvalInput) {
			in.Resource = &domain.ResourceState{ResourceID: "repo1", Freshness: domain.ResourceUnknown, AuthoritativeRevision: 9}
		}, InapplicableUnknown},
		"path ahead of resource": {func(in *EvalInput) { in.Path.ResourceRevision = 4 }, InapplicableUnknown},
		"partial read":           {func(in *EvalInput) { in.Observation.Completeness = domain.ObservationPartial }, InapplicableIncomplete},
		"other path": {func(in *EvalInput) {
			in.Observation.SubjectKey = mustSubjectKey(fileTarget("repo1", "docs/b.md", domain.FileCurrentContent, ""))
		}, InapplicableSubject},
	} {
		if v := m.Evaluate(fileInput(domain.FileCurrentContent, "", c.mod)); v.Kind != VerdictNotApplicable || v.Reason != c.want {
			t.Errorf("%s: %+v, want %s", name, v, c.want)
		}
	}
}

func TestFileReadFixedHash(t *testing.T) {
	m := fileRead{}
	// A path edit alone does not change the required snapshot: a read of the
	// required content applies even though the current path differs.
	v := m.Evaluate(fileInput(domain.FileFixedHash, hashOf("v1"), func(in *EvalInput) {
		in.Path.ContentHash = hashOf("v2")
		in.Resource = nil
	}))
	if v.Kind != VerdictPass || v.Dependency.Kind != domain.DependencyFixedContent || v.Dependency.Fingerprint != hashOf("v1") || v.Dependency.ResourceRevision != 0 {
		t.Fatalf("fixed read = %+v", v)
	}
	if v := m.Evaluate(fileInput(domain.FileFixedHash, hashOf("v0"), nil)); v.Kind != VerdictNotApplicable || v.Reason != InapplicableContent {
		t.Errorf("read of other content = %+v", v)
	}
}

// FuzzTestsPassVerdict checks tests_pass/1's safety property over arbitrary
// inputs: a PASS or FAIL verdict implies the same subject, a terminal complete
// run not older than the watermark, a KNOWN state of the target resource, and
// an observed fingerprint equal to the current one; the dependency always
// names exactly that state.
func FuzzTestsPassVerdict(f *testing.F) {
	f.Add(uint8(0), true, true, uint64(5), uint64(3), true, uint8(0), uint8(0), true)
	f.Add(uint8(1), true, true, uint64(5), uint64(5), true, uint8(1), uint8(1), true)
	f.Add(uint8(3), false, false, uint64(1), uint64(9), false, uint8(2), uint8(0), false)
	outcomes := []domain.ObservationOutcome{domain.OutcomePass, domain.OutcomeFail, domain.OutcomeError, domain.OutcomeTimeout, domain.OutcomeCancelled}
	fps := []string{hashOf("W1"), hashOf("W2"), ""}
	f.Fuzz(func(t *testing.T, outcome uint8, complete, sameSubject bool, ordinal, watermark uint64, known bool, obsFP, curFP uint8, sameResource bool) {
		in := testsInput(nil)
		in.Observation.Outcome = outcomes[int(outcome)%len(outcomes)]
		if !complete {
			in.Observation.Completeness = domain.ObservationPartial
		}
		if !sameSubject {
			in.Observation.SubjectKey = mustSubjectKey(testsTarget(func(v *domain.TestsTarget) { v.SuiteSpec = "other" }))
		}
		in.Ordinal, in.Watermark = ordinal, watermark
		in.Observation.ObservedWorkspaceFingerprint = fps[int(obsFP)%len(fps)]
		rs := &domain.ResourceState{ResourceID: "repo1", AuthoritativeRevision: 7, WorkspaceFingerprint: fps[int(curFP)%len(fps)], Freshness: domain.ResourceKnown}
		if !known {
			rs.Freshness, rs.WorkspaceFingerprint = domain.ResourceUnknown, ""
		}
		if !sameResource {
			rs.ResourceID = "repo2"
		}
		in.Resource = rs
		v := testsPass{}.Evaluate(in)
		if v.Kind == VerdictNotApplicable {
			if v.Dependency != (domain.ResourceClaim{}) || v.Reason == "" {
				t.Fatalf("inapplicable verdict carries data: %+v", v)
			}
			return
		}
		o := in.Observation
		ok := sameSubject && o.TerminalComplete() && ordinal > 0 && ordinal >= watermark && known && sameResource &&
			rs.WorkspaceFingerprint != "" && o.ObservedWorkspaceFingerprint == rs.WorkspaceFingerprint
		if !ok {
			t.Fatalf("applicable verdict %+v for inapplicable input %+v / %+v", v, o, rs)
		}
		if (v.Kind == VerdictPass) != (o.Outcome == domain.OutcomePass) || v.Dependency.Fingerprint != rs.WorkspaceFingerprint || v.Dependency.ResourceRevision != 7 || v.Dependency.Kind != domain.DependencyWorkspace {
			t.Fatalf("verdict %+v disagrees with input", v)
		}
	})
}
