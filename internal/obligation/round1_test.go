package obligation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func (f *evalFixture) newRun(t *testing.T) domain.ObservationRun {
	t.Helper()
	runN++
	run, err := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), fmt.Sprintf("exec-%d", runN), f.target))
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// G1 (SEC-1.1 = SPEC-1.1 = DUR-1.1): a proof applies only if no newer
// complete applicable FAIL exists for the subject at the evaluation
// snapshot, whatever the obligation's current status.
func TestG1StalePassAfterNewerFail(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	older, newer := f.newRun(t), f.newRun(t)
	f.report(t, newer, domain.OutcomeFail, hashOf("W1"), nil)
	f.report(t, older, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("older PASS satisfied after a newer complete FAIL: %+v", o)
	}
}

func TestG1StalePassAfterRejection(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	r1, r2, r3 := f.newRun(t), f.newRun(t), f.newRun(t)
	f.report(t, r1, domain.OutcomePass, hashOf("W1"), nil)
	f.report(t, r3, domain.OutcomeFail, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("newer FAIL did not reject: %+v", o)
	}
	f.report(t, r2, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("delayed PASS between PASS and FAIL re-satisfied: %+v", o)
	}
}

// DUR-1.1: a run has one terminal outcome; a contradictory second terminal
// observation of the same run is rejected and changes nothing.
func TestG1OneTerminalObservationPerRun(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	run := f.newRun(t)
	f.report(t, run, domain.OutcomeFail, hashOf("W1"), nil)
	runN++
	_, err := f.observe(t, f.harness, obsIntent(fmt.Sprintf("obs-%d", runN), run, f.evidence.ID, domain.OutcomePass, hashOf("W1")))
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("second terminal observation of one run: %v", err)
	}
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Errorf("same-run PASS after FAIL satisfied: %+v", o)
	}
}

// fileObligation declares a CURRENT_CONTENT file_read on docs/a.md for the
// USER pin, returning its reference.
func (f *evalFixture) fileObligation(t *testing.T, slot string) domain.ObligationRef {
	t.Helper()
	target := fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, "")
	in := domain.DeclareObligationIntent{RequestID: "d-file-" + slot, SourceItemID: "pu", DeclarationSlot: slot, Description: "read it",
		ExpectedSourceVersion: 1, Target: &target, Matcher: &FileReadV1}
	if _, err := f.s.declare(t, f.st, f.harness, in); err != nil {
		t.Fatal(err)
	}
	key, _ := f.item(t, "pu").CurrentKey()
	n, _ := harnessSlot(slot)
	return domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, n), Version: 1}
}

func (f *evalFixture) resourceReport(t *testing.T, fp string, resync, all bool, changed []string, contents ...domain.ResourcePathContent) {
	t.Helper()
	f.r.n++
	in := domain.ReportResourceChangeIntent{RequestID: fmt.Sprintf("rr-%d", f.r.n), ResourceID: "repo1", ExpectedRevision: f.r.rev,
		ExpectedAuthoritativeRevision: f.r.auth, ResultingAuthoritativeRevision: f.r.auth + 1, WorkspaceFingerprint: hashOf(fp),
		Resynchronization: resync, AllPaths: all, ChangedPaths: changed, PathContents: contents}
	if _, err := f.s.report(t, f.st, f.harness, in); err != nil {
		t.Fatal(err)
	}
	f.r.rev++
	f.r.auth++
}

func (f *evalFixture) assertPath(t *testing.T, ref domain.ObligationRef, rev uint64, content string) error {
	t.Helper()
	o := f.status(t, ref)
	loc := domain.ResourceLocator{ResourceID: "repo1", BaseDir: ".", Path: "docs/a.md"}
	in := intent(ref, o.Revision, domain.ObligationSatisfied)
	in.AssertionMode = domain.AssertionResourceBound
	in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyCurrentPath, ResourceID: "repo1", ResourceRevision: rev, Fingerprint: hashOf(content), Locator: &loc}}
	_, err := f.s.transition(t, f.st, f.system, in)
	return err
}

// XREV-1.1: a CURRENT_PATH claim is validated through the same currentness
// rule as file_read, so a stale cached path state cannot restore a proof a
// resource report invalidated.
func TestXREV11StalePathClaim(t *testing.T) {
	f := newEvalFixture(t)
	ref := f.fileObligation(t, "7")
	f.resourceReport(t, "W1b", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	rev := f.r.auth
	if err := f.assertPath(t, ref, rev, "H1"); err != nil {
		t.Fatalf("current path claim: %v", err)
	}
	// An unrelated edit keeps the path content current.
	f.resourceReport(t, "W2", false, false, []string{"docs/b.md"})
	if err := f.assertPath(t, ref, rev, "H1"); err != nil {
		// Already satisfied: a second assertion is a transition error, not staleness.
		if errors.Is(err, domain.ErrUnknownApplicability) {
			t.Fatalf("unrelated edit made the path claim stale: %v", err)
		}
	}
	// The path changes without new content: the proof is invalidated and a
	// new assertion of the old revision/content is refused.
	f.resourceReport(t, "W3", false, false, []string{"docs/a.md"})
	if o := f.status(t, ref); o.Status != domain.ObligationUnresolved {
		t.Fatalf("path change kept proof: %+v", o)
	}
	if err := f.assertPath(t, ref, rev, "H1"); !errors.Is(err, domain.ErrUnknownApplicability) {
		t.Errorf("stale path claim after a changed-path report: %v", err)
	}
	// A resync that omits the path also leaves no current content.
	g := newEvalFixture(t)
	ref2 := g.fileObligation(t, "7")
	g.resourceReport(t, "W1b", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	rev2 := g.r.auth
	g.resourceReport(t, "W4", true, false, nil)
	if err := g.assertPath(t, ref2, rev2, "H1"); !errors.Is(err, domain.ErrUnknownApplicability) {
		t.Errorf("stale path claim after a resync omitting the path: %v", err)
	}
}

// SPEC-1.9 (C-4): reevaluation after an authorized revalidation, or after a
// revert to already-proven content, re-satisfies from the same observation.
func TestSPEC19ReevaluateAfterRevalidation(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	o := f.status(t, f.sysTests)
	if o.Status != domain.ObligationSatisfied {
		t.Fatalf("setup = %+v", o)
	}
	if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, o.Revision, domain.ObligationUnresolved)); err != nil {
		t.Fatal(err)
	}
	o = f.status(t, f.sysTests)
	if _, err := f.reevaluate(t, f.harness, f.sysTests, o.Revision); err != nil {
		t.Fatalf("reevaluate after revalidation: %v", err)
	}
	if o = f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Fatalf("not re-satisfied: %+v", o)
	}
	// W1 -> W2 (invalidates) -> W1 (revert) -> reevaluate the same PASS.
	f.r.set(t, f.fixture, hashOf("W2"), false)
	f.r.set(t, f.fixture, hashOf("W1"), false)
	o = f.status(t, f.sysTests)
	if _, err := f.reevaluate(t, f.harness, f.sysTests, o.Revision); err != nil {
		t.Fatalf("reevaluate after revert: %v", err)
	}
	if o = f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Errorf("not re-satisfied after revert: %+v", o)
	}
}

// SPEC-1.10 (P3-16): a newer complete applicable FAIL rejects the subject's
// current resource-bound satisfaction, including an authorized RESOURCE_BOUND
// assertion; a run registered before the assertion is not newer.
func TestSPEC110FailRejectsResourceBoundAssertion(t *testing.T) {
	f := newEvalFixture(t)
	earlier := f.newRun(t)
	o := f.status(t, f.sysTests)
	in := intent(f.sysTests, o.Revision, domain.ObligationSatisfied)
	in.AssertionMode = domain.AssertionResourceBound
	in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo1", ResourceRevision: f.r.auth, Fingerprint: hashOf("W1")}}
	if _, err := f.s.transition(t, f.st, f.system, in); err != nil {
		t.Fatal(err)
	}
	f.report(t, earlier, domain.OutcomeFail, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Fatalf("FAIL of a run registered before the assertion rejected it: %+v", o)
	}
	f.report(t, f.newRun(t), domain.OutcomeFail, hashOf("W1"), nil)
	o = f.status(t, f.sysTests)
	if o.Status != domain.ObligationUnresolved {
		t.Fatalf("newer complete FAIL kept the resource-bound assertion: %+v", o)
	}
	h := (&evalFixture{fixture: f.fixture}).history(t, f.sysTests)
	if last := h[len(h)-1]; last.Cause != domain.CauseProofRejected || last.GrantID != "" {
		t.Errorf("rejection = %+v", last)
	}
}

// SPEC-1.11 (P3-15/23): a RESOURCE_BOUND assertion must declare a dependency
// that covers the obligation's own target.
func TestSPEC111ClaimsMustCoverTarget(t *testing.T) {
	f := newEvalFixture(t)
	f.resourceReport(t, "W1c", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	rev := f.r.auth
	unrelated := domain.ResourceLocator{ResourceID: "repo1", BaseDir: ".", Path: "unrelated.txt"}
	other := domain.ResourceLocator{ResourceID: "repo1", BaseDir: ".", Path: "docs/b.md"}
	assert := func(ref domain.ObligationRef, claims ...domain.ResourceClaim) error {
		o := f.status(t, ref)
		in := intent(ref, o.Revision, domain.ObligationSatisfied)
		in.AssertionMode = domain.AssertionResourceBound
		in.Resources = claims
		_, err := f.s.transition(t, f.st, f.system, in)
		return err
	}
	// tests_pass: a fixed-content claim on an unrelated file is not a
	// workspace dependency of the suite.
	if err := assert(f.sysTests, domain.ResourceClaim{Kind: domain.DependencyFixedContent, ResourceID: "repo1", Fingerprint: hashOf("anything"), Locator: &unrelated}); !errors.Is(err, domain.ErrUnknownApplicability) {
		t.Errorf("tests target with unrelated fixed-content claim: %v", err)
	}
	// CURRENT_CONTENT read: a current claim on another path does not cover.
	file := f.fileObligation(t, "8")
	f.resourceReport(t, "W1d", false, false, []string{"docs/b.md"}, domain.ResourcePathContent{Path: "docs/b.md", ContentHash: hashOf("B1")})
	if err := assert(file, domain.ResourceClaim{Kind: domain.DependencyCurrentPath, ResourceID: "repo1", ResourceRevision: f.r.auth, Fingerprint: hashOf("B1"), Locator: &other}); !errors.Is(err, domain.ErrUnknownApplicability) {
		t.Errorf("file target with another path's claim: %v", err)
	}
	loc := domain.ResourceLocator{ResourceID: "repo1", BaseDir: ".", Path: "docs/a.md"}
	if err := assert(file, domain.ResourceClaim{Kind: domain.DependencyCurrentPath, ResourceID: "repo1", ResourceRevision: rev, Fingerprint: hashOf("H1"), Locator: &loc}); err != nil {
		t.Errorf("covering current-path claim: %v", err)
	}
	// Covering workspace claim for the tests target.
	if err := assert(f.sysTests, domain.ResourceClaim{Kind: domain.DependencyWorkspace, ResourceID: "repo1", ResourceRevision: f.r.auth, Fingerprint: hashOf("W1d")}); err != nil {
		t.Errorf("covering workspace claim: %v", err)
	}
}

func TestSPEC111FixedHashTarget(t *testing.T) {
	f := newEvalFixture(t)
	target := fileTarget("repo1", "docs/a.md", domain.FileFixedHash, hashOf("REQ"))
	in := domain.DeclareObligationIntent{RequestID: "d-fixed", SourceItemID: "pu", DeclarationSlot: "9", Description: "read it",
		ExpectedSourceVersion: 1, Target: &target, Matcher: &FileReadV1}
	if _, err := f.s.declare(t, f.st, f.harness, in); err != nil {
		t.Fatal(err)
	}
	key, _ := f.item(t, "pu").CurrentKey()
	ref := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, 9), Version: 1}
	loc := domain.ResourceLocator{ResourceID: "repo1", BaseDir: ".", Path: "docs/a.md"}
	assert := func(hash string) error {
		o := f.status(t, ref)
		in := intent(ref, o.Revision, domain.ObligationSatisfied)
		in.AssertionMode = domain.AssertionResourceBound
		in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyFixedContent, ResourceID: "repo1", Fingerprint: hashOf(hash), Locator: &loc}}
		_, err := f.s.transition(t, f.st, f.system, in)
		return err
	}
	if err := assert("OTHER"); !errors.Is(err, domain.ErrUnknownApplicability) {
		t.Errorf("fixed-content claim with a hash other than the required one: %v", err)
	}
	if err := assert("REQ"); err != nil {
		t.Errorf("fixed-content claim of the required hash: %v", err)
	}
}

// SPEC-1.18 (P3-23): a changed directory conservatively intersects every
// file under it, for proof invalidation and for path-content currency.
func TestSPEC118DirectoryChangeIntersectsFiles(t *testing.T) {
	f := newEvalFixture(t)
	ref := f.fileObligation(t, "11")
	f.resourceReport(t, "W1e", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	rev := f.r.auth
	if err := f.assertPath(t, ref, rev, "H1"); err != nil {
		t.Fatal(err)
	}
	f.resourceReport(t, "W2e", false, false, []string{"docs"})
	if o := f.status(t, ref); o.Status != domain.ObligationUnresolved {
		t.Fatalf("directory change kept a proof on a file under it: %+v", o)
	}
	if err := f.assertPath(t, ref, rev, "H1"); !errors.Is(err, domain.ErrUnknownApplicability) {
		t.Errorf("path content under a changed directory still current: %v", err)
	}
	// A sibling-prefix name is not under the directory.
	g := newEvalFixture(t)
	ref2 := g.fileObligation(t, "11")
	g.resourceReport(t, "W1e", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	if err := g.assertPath(t, ref2, g.r.auth, "H1"); err != nil {
		t.Fatal(err)
	}
	g.resourceReport(t, "W2e", false, false, []string{"doc"})
	if o := g.status(t, ref2); o.Status != domain.ObligationSatisfied {
		t.Errorf("sibling-prefix change invalidated: %+v", o)
	}
}
