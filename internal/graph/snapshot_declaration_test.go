package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"testing"
)

func TestWorkingSnapshotDeclarationPreservesWholeOrderedIdentity(t *testing.T) {
	a := workingItem("s", "a", 1, domain.AuthorityUser)
	b := workingItem("s", "b", 2, domain.AuthorityUser)
	a.Namespace, b.Namespace = domain.NamespaceDirective, domain.NamespaceDirective
	reader := &declarationFixture{declarations: map[string]domain.CreationDeclaration{a.ID: declarationForTest(a), b.ID: declarationForTest(b)}}
	tx := &retirementFixture{last: 2}
	first, err := planSnapshotDeclaration(tx, reader, []domain.ContextItem{a, b}, "event")
	if err != nil {
		t.Fatal(err)
	}
	ordered, err := planSnapshotDeclaration(tx, reader, []domain.ContextItem{b, a}, "other")
	if err != nil || first.Signature == ordered.Signature {
		t.Fatal("snapshot lost order", err)
	}
	a.Residency, b.Generation = domain.ResidencyArchived, domain.GenerationDurable
	same, err := planSnapshotDeclaration(tx, reader, []domain.ContextItem{a, b}, "later")
	if err != nil || same.Signature != first.Signature {
		t.Fatal("lifecycle state changed snapshot identity", err)
	}
	if _, err := planSnapshotDeclaration(tx, reader, []domain.ContextItem{a, a}, "duplicate"); err == nil {
		t.Fatal("repeated member accepted")
	}
	b.Access.AgentID = "other"
	if _, err := planSnapshotDeclaration(tx, reader, []domain.ContextItem{a, b}, "boundary"); err == nil {
		t.Fatal("mixed partition accepted")
	}
}
