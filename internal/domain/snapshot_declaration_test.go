package domain

import "testing"

func TestSnapshotIdentityUsesOrderedDeclarations(t *testing.T) {
	s := SnapshotDeclaration{SemanticMeta: semanticMeta("s"), TaskID: "t", Authority: AuthorityUser, Access: AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t"}, PolicyVersion: "p", LegacyKnown: true, Members: []SnapshotDeclarationMember{{ItemID: "i", DeclarationID: "d", Signature: HashBytes([]byte("one"))}, {ItemID: "j", DeclarationID: "e", Signature: HashBytes([]byte("two"))}}}
	h, err := s.CanonicalSignature()
	if err != nil {
		t.Fatal(err)
	}
	copy := s.Clone()
	copy.Members[0].ItemID = "new"
	same, _ := copy.CanonicalSignature()
	if h != same {
		t.Fatal("occurrence identity changed declaration signature")
	}
	copy.Members[0], copy.Members[1] = copy.Members[1], copy.Members[0]
	other, _ := copy.CanonicalSignature()
	if h == other {
		t.Fatal("snapshot order omitted")
	}
}
