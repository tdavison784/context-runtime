package domain

import "testing"

func TestObservationCannotFabricateEvidenceOrPass(t *testing.T) {
	o := ObservationRecord{SemanticMeta: semanticMeta("o"), Family: ObservationTests, RunID: "run", ExecutionID: "exec", SubjectKey: "subject", EvidenceItemID: "evidence", Binding: WorkspaceBindingRef{ID: "w", Version: 1}, Reporter: Principal{SessionID: "s", Authority: AuthorityHarness}, Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}, ObservedWorkspaceFingerprint: HashBytes(nil), Outcome: OutcomePass, Completeness: ObservationComplete, Passed: 1, Total: 1, ReportingMatcher: MatcherRef{Name: "tests_pass", Version: "1"}}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	o.EvidenceItemID = ""
	if o.Validate() == nil {
		t.Fatal("missing evidence accepted")
	}
	o.EvidenceItemID = "evidence"
	o.Failed = 1
	if o.Validate() == nil {
		t.Fatal("PASS with failures accepted")
	}
	o.Failed = 0
	o.Completeness = ObservationPartial
	if o.TerminalComplete() {
		t.Fatal("partial PASS eligible")
	}
}
