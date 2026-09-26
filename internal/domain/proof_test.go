package domain

import "testing"

func TestProofRequiresBackedDependencyIdentity(t *testing.T) {
	p := ApplicabilityProof{SemanticMeta: semanticMeta("p"), Target: ObligationRef{SessionID: "s", ObligationID: "o", Version: 1}, TargetSpecHash: HashBytes(nil), TransitionID: "tr", EvidenceCoverageID: "coverage", Matcher: &MatcherRef{Name: "tests_pass", Version: "1"}, RuleVersion: "rule", ObservationID: "obs", DependencyIDs: []string{"dep"}, Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	copy := p.Clone()
	copy.DependencyIDs[0] = "other"
	copy.Matcher.Version = "2"
	if p.DependencyIDs[0] != "dep" || p.Matcher.Version != "1" {
		t.Fatal("proof clone aliases")
	}
	p.DependencyIDs = nil
	if p.Validate() == nil {
		t.Fatal("empty resource proof accepted")
	}
}
