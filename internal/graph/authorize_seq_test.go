package graph

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
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
func (f *sequenceFixture) LiveGrantsFor(_ domain.Action, _ domain.GrantTarget, seq uint64, _ int) ([]domain.MutationGrant, error) {
	if !store.GrantLiveAt(f.grant, seq) {
		return nil, nil
	}
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

// TestAuthorizationIgnoresDeadGrantHistory checks G2 (SEC-1.5, DUR-1.4):
// revoked grant history on a target never makes a live grant unusable,
// because authorization reads only the grants in force at its sequence.
func TestAuthorizationIgnoresDeadGrantHistory(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		actor := domain.Principal{SessionID: "s", Authority: domain.AuthorityHarness}
		issuer := domain.Principal{SessionID: "s", Authority: domain.AuthoritySystem}
		ref := domain.ItemGrantTarget("s", "i")
		grant := func(id string, seq uint64) domain.MutationGrant {
			return domain.MutationGrant{ID: id, SessionID: "s", Action: domain.ActionResolve, Targets: []domain.GrantTarget{ref}, Issuer: issuer, Grantee: &actor, IssuedSeq: seq}
		}
		update(t, s, "s", func(tx store.Tx) error {
			it := storetest.NewItem("s", "i", tx.NextSeq(), "system fact")
			it.Authority = domain.AuthoritySystem
			if err := tx.InsertItem(it); err != nil {
				return err
			}
			for _, id := range []string{"g1", "g2", "g3"} {
				if err := tx.InsertGrant(grant(id, tx.NextSeq())); err != nil {
					return err
				}
				if _, err := tx.RevokeGrant(id, storetest.NewLifecycleEvent("s", "revoke-"+id, tx.NextSeq(), domain.TargetGrant, id)); err != nil {
					return err
				}
			}
			return tx.InsertGrant(grant("g-live", tx.NextSeq()))
		})
		update(t, s, "s", func(tx store.Tx) error {
			auth, err := AuthorizeAtSequence(tx, actor, domain.ActionResolve, []domain.GrantTarget{ref}, nil, tx.NextSeq(), 2)
			if err != nil {
				t.Fatalf("live grant behind %d revoked ones: %v", 3, err)
			}
			if auth.GrantIDs[ref.AuthorizationKey] != "g-live" {
				t.Errorf("authorization = %+v, want grant g-live", auth)
			}
			return nil
		})
	})
}
