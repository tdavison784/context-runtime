package graph

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"testing"
)

type sequenceFixture struct {
	store.Tx
	store.SemanticReader
	item      domain.ContextItem
	grant     domain.MutationGrant
	allocated uint64
}

func (f *sequenceFixture) SessionID() string                         { return "s" }
func (f *sequenceFixture) Allocated(seq uint64) bool                 { return seq == f.allocated }
func (f *sequenceFixture) Item(string) (domain.ContextItem, error)   { return f.item, nil }
func (f *sequenceFixture) SemanticReadBackend() store.SemanticReader { return f }
func (f *sequenceFixture) GrantsFor(domain.Action, domain.GrantTarget, int) ([]domain.MutationGrant, error) {
	return []domain.MutationGrant{f.grant}, nil
}
func TestAuthorizationUsesActualAllocatedGrantBoundary(t *testing.T) {
	actor := domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}
	issuer := actor
	issuer.Authority = domain.AuthoritySystem
	ref := domain.ItemGrantTarget("s", "i")
	f := &sequenceFixture{allocated: 7, item: domain.ContextItem{ID: "i", Authority: domain.AuthoritySystem, Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: "s"}}, grant: domain.MutationGrant{ID: "g", SessionID: "s", Action: domain.ActionResolve, Targets: []domain.GrantTarget{ref}, Issuer: issuer, Grantee: &actor, IssuedSeq: 1, ExpiresAtSeq: 7}}
	if _, err := AuthorizeAtSequence(f, actor, domain.ActionResolve, []domain.GrantTarget{ref}, nil, 7, 8); err != nil {
		t.Fatal(err)
	}
	if _, err := AuthorizeAtSequence(f, actor, domain.ActionResolve, []domain.GrantTarget{ref}, nil, 8, 8); !errors.Is(err, domain.ErrInvalidRecord) {
		t.Fatal("unallocated prediction accepted", err)
	}
	f.allocated = 8
	if _, err := AuthorizeAtSequence(f, actor, domain.ActionResolve, []domain.GrantTarget{ref}, nil, 8, 8); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatal("expired grant accepted", err)
	}
}
