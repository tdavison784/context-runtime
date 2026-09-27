package obligation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// SEC-3.10 (P3-20, FR-AUTH-002): a workspace binding version can be
// appended only by an actor whose authority is at least the previous
// version's reporter's and who may read the previous version. A lower
// authority never overrides a higher-authority binding, in its own context
// or by moving it to another; a hidden previous version answers as absent.
func TestSEC310BindingVersionNeedsReporterAuthority(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	harness := actorOf(domain.AuthorityHarness)
	system := actorOf(domain.AuthoritySystem)
	seedTask(t, st, "task")
	seedResource(t, st, "repo1", harness)
	sysPin := seedPinned(t, st, "p-sys", "sys", domain.AuthoritySystem, "All tests must pass.")
	task := domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"}
	if _, err := s.bindWS(t, st, system, bindIntent("ws-sys", 1, task)); err != nil {
		t.Fatal(err)
	}
	resolved := func(step string) {
		t.Helper()
		if ws := resolveFor(t, s, st, sysPin); ws.Binding == nil || ws.Binding.ID != "ws-sys" || ws.Binding.Version != 1 {
			t.Errorf("%s: SYSTEM pin resolves %+v, want ws-sys v1", step, ws)
		}
	}
	resolved("setup")
	same := bindIntent("ws-sys", 2, task)
	same.RequestID = "harness-v2-same"
	if _, err := s.bindWS(t, st, harness, same); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Errorf("HARNESS v2 of a SYSTEM binding, same context: %v", err)
	}
	resolved("same context")
	cross := bindIntent("ws-sys", 2, domain.WorkspaceSourceContext{Kind: domain.WorkspaceSource, ID: sysPin.ID})
	cross.RequestID = "harness-v2-cross"
	if _, err := s.bindWS(t, st, harness, cross); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Errorf("HARNESS v2 of a SYSTEM binding, other context: %v", err)
	}
	resolved("other context")
	// Equal or higher authority may append.
	if _, err := s.bindWS(t, st, harness, bindIntent("ws-h", 1, task)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.bindWS(t, st, harness, bindIntent("ws-h", 2, task)); err != nil {
		t.Errorf("HARNESS v2 of its own HARNESS binding: %v", err)
	}
	if _, err := s.bindWS(t, st, system, bindIntent("ws-h", 3, task)); err != nil {
		t.Errorf("SYSTEM v3 of a HARNESS binding: %v", err)
	}
	// A previous version the actor cannot read is absent, not refused.
	agentB := domain.Principal{SessionID: testSession, WorkflowID: system.WorkflowID, TaskID: "task", AgentID: "b", Authority: domain.AuthoritySystem}
	private := bindIntent("ws-b", 1, task)
	private.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task", AgentID: "b"}
	if _, err := s.bindWS(t, st, agentB, private); err != nil {
		t.Fatal(err)
	}
	if _, err := s.bindWS(t, st, system, bindIntent("ws-b", 2, task)); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("v2 over a previous version hidden from the actor: %v, want ErrNotFound", err)
	}
}

// DUR-3.1 (G2/H2): a resource report reads only what it can affect. More
// live file states than the work budget, on paths a report never touches,
// must not wedge an unrelated-path report or an ALL-paths report, and the
// ALL-paths report still invalidates the satisfied file proof it affects.
func TestDUR31ReportsIgnoreUntouchedLiveState(t *testing.T) {
	f := newEvalFixture(t)
	ref := f.fileObligation(t, "31")
	f.matcherGrant(t, "g-file", ref, FileReadV1, f.userP)
	const files = 70
	path := func(i int) string {
		if i == 0 {
			return "docs/a.md" // the obligation's target
		}
		return fmt.Sprintf("docs/f%02d.md", i)
	}
	for start := 0; start < files; start += 10 {
		var contents []domain.ResourcePathContent
		var changed []string
		for i := start; i < start+10; i++ {
			contents = append(contents, domain.ResourcePathContent{Path: path(i), ContentHash: hashOf(path(i))})
			changed = append(changed, path(i))
		}
		f.resourceReport(t, fmt.Sprintf("W-rec%d", start), false, false, changed, contents...)
	}
	for i := range files {
		runN++
		run, err := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), fmt.Sprintf("exec-%d", runN), fileTarget("repo1", path(i), domain.FileCurrentContent, "")))
		if err != nil {
			t.Fatal(err)
		}
		f.report(t, run, domain.OutcomePass, hashOf(path(i)), nil)
	}
	if o := f.status(t, ref); o.Status != domain.ObligationSatisfied {
		t.Fatalf("setup: file obligation = %+v", o)
	}
	f.r.n++
	unrelated := domain.ReportResourceChangeIntent{RequestID: "dur31-unrelated", ResourceID: "repo1", ExpectedRevision: f.r.rev,
		ExpectedAuthoritativeRevision: f.r.auth, ResultingAuthoritativeRevision: f.r.auth + 1, WorkspaceFingerprint: hashOf("W-unrelated"), ChangedPaths: []string{"other/z.md"}}
	if _, err := f.s.report(t, f.st, f.harness, unrelated); err != nil {
		t.Fatalf("unrelated-path report with %d live file states: %v", files, err)
	}
	f.r.rev++
	f.r.auth++
	if o := f.status(t, ref); o.Status != domain.ObligationSatisfied {
		t.Errorf("unrelated-path report invalidated the file proof: %+v", o)
	}
	f.r.n++
	all := domain.ReportResourceChangeIntent{RequestID: "dur31-all", ResourceID: "repo1", ExpectedRevision: f.r.rev,
		ExpectedAuthoritativeRevision: f.r.auth, ResultingAuthoritativeRevision: f.r.auth + 1, WorkspaceFingerprint: hashOf("W-all"), AllPaths: true}
	if _, err := f.s.report(t, f.st, f.harness, all); err != nil {
		t.Fatalf("ALL-paths report with %d live file states: %v", files, err)
	}
	if o := f.status(t, ref); o.Status != domain.ObligationUnresolved {
		t.Errorf("ALL-paths report kept the file proof: %+v", o)
	}
}

// DUR-3.1 (B), commander ruling: subject-state applicability is derived
// exactly at read from authoritative resource state, never marked by
// reports. A file state stays CURRENT across unrelated edits, is STALE once
// its path changes and CURRENT again when the path's content returns; a
// tests state follows the workspace fingerprint; lost freshness is UNKNOWN.
func TestDUR31SubjectApplicabilityIsDerived(t *testing.T) {
	f := newEvalFixture(t)
	f.resourceReport(t, "W-a", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	file := fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, "")
	f.observeTests(t, file, domain.OutcomePass, hashOf("H1"), nil)
	tests := f.target
	f.observeTests(t, tests, domain.OutcomePass, hashOf("W-a"), nil)
	want := func(step string, target domain.TargetSpec, a domain.ApplicabilityState) {
		t.Helper()
		if st, ok := f.subject(t, target); !ok || st.Applicability != a {
			t.Errorf("%s: applicability = %s (found %v), want %s", step, st.Applicability, ok, a)
		}
	}
	want("filed", file, domain.ApplicabilityCurrent)
	want("filed", tests, domain.ApplicabilityCurrent)
	f.resourceReport(t, "W-b", false, false, []string{"docs/b.md"})
	want("unrelated edit", file, domain.ApplicabilityCurrent)
	want("fingerprint moved", tests, domain.ApplicabilityStale)
	f.resourceReport(t, "W-a", false, false, []string{"docs/a.md"})
	want("path changed without content", file, domain.ApplicabilityStale)
	want("fingerprint reverted", tests, domain.ApplicabilityCurrent)
	f.resourceReport(t, "W-c", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	want("content restored", file, domain.ApplicabilityCurrent)
	// A skipped revision loses freshness (P3-19).
	f.r.n++
	gap := domain.ReportResourceChangeIntent{RequestID: "dur31-gap", ResourceID: "repo1", ExpectedRevision: f.r.rev,
		ExpectedAuthoritativeRevision: f.r.auth, ResultingAuthoritativeRevision: f.r.auth + 2, WorkspaceFingerprint: hashOf("W-c"), ChangedPaths: []string{"docs/b.md"}}
	if _, err := f.s.report(t, f.st, f.harness, gap); err != nil {
		t.Fatal(err)
	}
	want("freshness lost", file, domain.ApplicabilityUnknown)
	want("freshness lost", tests, domain.ApplicabilityUnknown)
}

// DUR-3.8 (H2): the history SATISFIES view is returned in cursor pages of
// bounded work, never failed because a version's history outgrew one
// transaction's budget.
func TestDUR38HistorySatisfiesPages(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	fp := "W1"
	for i := range h2History / 2 {
		f.report(t, f.newRun(t), domain.OutcomePass, hashOf(fp), nil)
		fp = fmt.Sprintf("C%d", i)
		f.r.set(t, f.fixture, hashOf(fp), false)
	}
	v, err := f.satisfies(t, f.harness, false)
	if err != nil || len(v.Relations) != h2History/2 {
		t.Fatalf("history SATISFIES = %d relations, %v; want %d", len(v.Relations), err, h2History/2)
	}
	for _, rel := range v.Relations {
		if rel.Current {
			t.Errorf("invalidated proof presented as current: %+v", rel)
		}
	}
}

// DUR-3.1 (A), commander ruling: a KNOWN, non-ALL report reads only what it
// can affect — live CURRENT_PATH proofs at or below each changed path, and
// WORKSPACE proofs only when the fingerprint changes — never the resource's
// whole live proof set. Directory changes still reach the files below them.
func TestDUR31ReportsReadOnlyAffectedProofs(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	file := f.fileObligation(t, "32")
	f.matcherGrant(t, "g-file", file, FileReadV1, f.userP)
	f.resourceReport(t, "W1", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	runN++
	run, err := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), fmt.Sprintf("exec-%d", runN), fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, "")))
	if err != nil {
		t.Fatal(err)
	}
	f.report(t, run, domain.OutcomePass, hashOf("H1"), nil)
	for _, ref := range []domain.ObligationRef{f.sysTests, file} {
		if o := f.status(t, ref); o.Status != domain.ObligationSatisfied {
			t.Fatalf("setup %s = %+v", ref.ObligationID, o)
		}
	}
	f.st.counting.Store(true)
	step := func(name string, fp string, changed []string, wantWorkspace bool, tests, fileStatus domain.ObligationStatus) {
		t.Helper()
		f.st.wholeReads.Store(0)
		f.st.workspaceReads.Store(0)
		f.resourceReport(t, fp, false, false, changed)
		if n := f.st.wholeReads.Load(); n != 0 {
			t.Errorf("%s: %d whole-resource proof reads", name, n)
		}
		if got := f.st.workspaceReads.Load() > 0; got != wantWorkspace {
			t.Errorf("%s: workspace proofs read = %v, want %v", name, got, wantWorkspace)
		}
		if o := f.status(t, f.sysTests); o.Status != tests {
			t.Errorf("%s: tests obligation = %s, want %s", name, o.Status, tests)
		}
		if o := f.status(t, file); o.Status != fileStatus {
			t.Errorf("%s: file obligation = %s, want %s", name, o.Status, fileStatus)
		}
	}
	step("unrelated path, same fingerprint", "W1", []string{"other/z.md"}, false, domain.ObligationSatisfied, domain.ObligationSatisfied)
	step("containing directory, same fingerprint", "W1", []string{"docs"}, false, domain.ObligationSatisfied, domain.ObligationUnresolved)
	step("new fingerprint", "W2", []string{"other/z.md"}, true, domain.ObligationUnresolved, domain.ObligationUnresolved)
}

// DUR-3.1 (C), commander ruling: live non-FIXED proof dependency rows per
// resource are capped (MaxLiveProofDependents) so an ALL/UNKNOWN/resync
// report can always invalidate them within its budget. An explicit
// RESOURCE_BOUND assertion past the cap is refused with ErrResourceLimit and
// changes nothing; room returns as proofs are invalidated.
func TestDUR31AssertionRespectsDependentCap(t *testing.T) {
	f := newResourceFixture(t)
	limit := testPolicy().MaxLiveProofDependents
	refs := []domain.ObligationRef{f.user, f.sysTests}
	for i := 0; len(refs) <= limit; i++ {
		src := seedPinned(t, f.st, fmt.Sprintf("cap%d", i), fmt.Sprintf("capdir%d", i), domain.AuthorityUser, "Keep the suite green.")
		refs = append(refs, f.repo2Obligation(t, src.ID, f.harness))
	}
	for _, ref := range refs[:limit] {
		f.assertBound(t, ref, f.system)
	}
	last := refs[limit]
	o := f.status(t, last)
	rs := f.state(t)
	in := intent(last, o.Revision, domain.ObligationSatisfied)
	in.AssertionMode = domain.AssertionResourceBound
	in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo2", ResourceRevision: rs.AuthoritativeRevision, Fingerprint: rs.WorkspaceFingerprint}}
	if _, err := f.s.transition(t, f.st, f.system, in); !errors.Is(err, domain.ErrResourceLimit) {
		t.Fatalf("assertion past the dependent cap: %v, want ErrResourceLimit", err)
	}
	if o := f.status(t, last); o.Status != domain.ObligationUnresolved {
		t.Errorf("refused assertion changed status: %+v", o)
	}
	// An ALL-paths change invalidates every live proof; room returns.
	f.resync(t, f.auth+1, hashOf("W-cap"))
	for _, ref := range refs[:limit] {
		if o := f.status(t, ref); o.Status != domain.ObligationUnresolved {
			t.Fatalf("resync kept %s: %+v", ref.ObligationID, o)
		}
	}
	f.assertBound(t, last, f.system)
}

// DUR-3.1 (C): a matcher satisfaction past the cap leaves the obligation
// UNRESOLVED with the observation kept as evidence (the report succeeds, as
// with a missing grant), and a trusted reevaluation at the cap reports
// ErrResourceLimit.
func TestDUR31MatcherRespectsDependentCap(t *testing.T) {
	f := newEvalFixture(t)
	f.resourceReport(t, "W1", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	rev := f.r.auth
	limit := testPolicy().MaxLiveProofDependents
	for i := range limit {
		ref := f.fileObligation(t, fmt.Sprint(40+i))
		if err := f.assertPath(t, ref, rev, "H1"); err != nil {
			t.Fatalf("path assertion %d within the cap: %v", i, err)
		}
	}
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	obs := f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("matcher satisfied past the dependent cap: %+v", o)
	}
	var stored bool
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		_, err := r.Observation(obs.ID)
		stored = err == nil
		return nil
	})
	if !stored {
		t.Errorf("observation at the cap was not kept as evidence")
	}
	o := f.status(t, f.sysTests)
	if _, err := f.reevaluate(t, f.harness, f.sysTests, o.Revision); !errors.Is(err, domain.ErrResourceLimit) {
		t.Errorf("reevaluation at the cap: %v, want ErrResourceLimit", err)
	}
}
