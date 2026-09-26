package graph

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
	"testing"
)

func TestReplacementGrantCannotChangeKeyOrOutliveEdgeSequence(t *testing.T) {
	actor := domain.Principal{SessionID: "s", TaskID: "task", WorkflowID: "wf", AgentID: "agent", Authority: domain.AuthorityUser}
	old := goalLike("s", "old", "key", 1, "old goal")
	old.Authority, old.Namespace = domain.AuthoritySystem, domain.NamespaceDirective
	fresh := old.Clone()
	fresh.ID = "fresh"
	issuer := actor
	issuer.Authority = domain.AuthoritySystem
	f := &sequenceFixture{allocated: 7, item: old, grant: domain.MutationGrant{ID: "g", SessionID: "s", Action: domain.ActionReplaceDirective, Targets: []domain.GrantTarget{domain.ItemGrantTarget("s", old.ID)}, Issuer: issuer, Grantee: &actor, IssuedSeq: 1, ExpiresAtSeq: 7}}
	if grant, err := authorizeReplacement(f, actor, fresh, old, 7); err != nil || grant != "g" {
		t.Fatalf("inclusive replacement = %q, %v", grant, err)
	}
	fresh.DirectiveID = "other-key"
	if _, err := authorizeReplacement(f, actor, fresh, old, 7); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatal("grant moved namespace key", err)
	}
	fresh.DirectiveID = "key"
	f.allocated = 8
	if _, err := authorizeReplacement(f, actor, fresh, old, 8); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatal("expired source grant accepted", err)
	}
}
