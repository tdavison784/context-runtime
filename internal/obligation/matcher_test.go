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
