package obligation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
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
