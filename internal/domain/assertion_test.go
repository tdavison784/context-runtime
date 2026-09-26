package domain

import "testing"

func TestAssertionModeIsExplicit(t *testing.T) {
	a := AssertionRecord{SemanticMeta: semanticMeta("a"), Target: ObligationRef{SessionID: "s", ObligationID: "o", Version: 1}, Actor: Principal{SessionID: "s", Authority: AuthorityUser}, TransitionID: "tr", Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}}
	if a.Validate() == nil {
		t.Fatal("implicit mode accepted")
	}
	a.Mode = AssertionAttestation
	a.EvidenceCoverageID = "citations"
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	a.Mode = AssertionResourceBound
	if a.Validate() == nil {
		t.Fatal("unbacked resource assertion accepted")
	}
}
