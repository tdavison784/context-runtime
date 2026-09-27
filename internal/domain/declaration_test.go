package domain

import "testing"

func TestCreationDeclarationCloneAndOrigin(t *testing.T) {
	s := CreationSemantics{Key: CurrentKey{SessionID: "s", Namespace: NamespaceDirective, ID: "g", Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}}, Authority: AuthorityUser, Section: SectionGoal, Kind: KindGoal, ContentHash: HashBytes(nil), Generation: GenerationDurable, Retention: RetentionHigh, Residency: ResidencyResident, SupportIDs: []string{"a"}}
	h, err := s.Signature("p1")
	if err != nil {
		t.Fatal(err)
	}
	clone := s.Clone()
	clone.SupportIDs[0] = "b"
	h2, _ := clone.Signature("p1")
	if h == h2 || s.SupportIDs[0] != "a" {
		t.Fatal("support identity or clone broken")
	}
	h3, _ := s.Signature("p2")
	if h == h3 {
		t.Fatal("policy omitted")
	}
	d := CreationDeclaration{SemanticMeta: semanticMeta("d"), ItemID: "i", PolicyVersion: "p1", LegacyKnown: true, Signature: h, AcceptedSemantics: s}
	if !d.Same(d.Clone()) {
		t.Fatal("same creation rejected")
	}
	d.LegacyKnown = false
	d.Signature = ""
	if d.Same(d) {
		t.Fatal("legacy unknown matched")
	}
}

func TestDeclarationIgnoresTurnWithoutTemporalEligibility(t *testing.T) {
	s := CreationSemantics{Key: CurrentKey{SessionID: "s", Namespace: NamespaceDirective, ID: "r", Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}}, Authority: AuthorityUser, Section: SectionRemember, Kind: KindFact, ContentHash: HashBytes(nil), Generation: GenerationDurable, Retention: RetentionHigh, Residency: ResidencyResident}
	a, err := s.Signature("p")
	if err != nil {
		t.Fatal(err)
	}
	s.CreatedTurn = 9
	s.OriginTurnID = "new"
	b, _ := s.Signature("p")
	if a != b {
		t.Fatal("non-temporal restatement changed identity")
	}
}
