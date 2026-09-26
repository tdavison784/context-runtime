package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

type retirementFixture struct {
	store.Tx
	store.SemanticReader
	version domain.ObligationVersion
	grant   domain.MutationGrant
	last    uint64
	written []domain.LifecycleEvent
}

func (f *retirementFixture) SessionID() string                         { return "s" }
func (f *retirementFixture) NextSeq() uint64                           { f.last++; return f.last }
func (f *retirementFixture) Allocated(seq uint64) bool                 { return seq > 0 && seq <= f.last }
func (f *retirementFixture) SemanticReadBackend() store.SemanticReader { return f }
func (f *retirementFixture) ObligationsBySource(string, int) ([]domain.ObligationVersion, error) {
	return []domain.ObligationVersion{f.version}, nil
}
func (f *retirementFixture) ExactObligation(ref domain.ObligationRef) (domain.ObligationVersion, error) {
	if ref.ObligationID != f.version.ObligationID || ref.Version != f.version.Version {
		return domain.ObligationVersion{}, domain.ErrNotFound
	}
	return f.version, nil
}
func (f *retirementFixture) GrantsFor(action domain.Action, ref domain.GrantTarget, limit int) ([]domain.MutationGrant, error) {
	if action != domain.ActionReplaceDirective || ref != domain.ObligationGrantTarget("s", f.version.ObligationID, f.version.Version) || limit <= 0 {
		panic("non-exact grant query")
	}
	return []domain.MutationGrant{f.grant}, nil
}
func (f *retirementFixture) RetireObligationVersion(id string, version, revision uint64, event domain.LifecycleEvent) (domain.ObligationVersion, error) {
	if id != f.version.ObligationID || version != f.version.Version || revision != f.version.Revision {
		return domain.ObligationVersion{}, domain.ErrVersionConflict
	}
	f.written = append(f.written, event)
	return f.version, nil
}

func TestRetirementAuthorizesAndWritesSameExactSequence(t *testing.T) {
	actor := domain.Principal{SessionID: "s", Authority: domain.AuthorityUser}
	ref := domain.ObligationGrantTarget("s", "o", 2)
	f := &retirementFixture{last: 10, version: domain.ObligationVersion{SessionID: "s", ObligationID: "o", Version: 2, Revision: 7, Current: true, SourceAuthority: domain.AuthoritySystem, Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: "s"}}}
	f.grant = domain.MutationGrant{ID: "g", SessionID: "s", Action: domain.ActionReplaceDirective, Targets: []domain.GrantTarget{ref}, Issuer: domain.Principal{SessionID: "s", Authority: domain.AuthoritySystem}, Grantee: &actor, IssuedSeq: 1, ExpiresAtSeq: 11}
	plan, err := planObligationRetirement(f, actor, "source")
	if err != nil || len(plan) != 1 || plan[0].seq != 11 || len(f.written) != 0 {
		t.Fatalf("plan = %v, %v", plan, err)
	}
	f.NextSeq()
	if err := retireObligations(f, actor, plan, "source", "new", "event"); err != nil {
		t.Fatal(err)
	}
	if f.written[0].Seq != 11 || f.written[0].GrantID != "g" {
		t.Fatal("authorization and audit diverged")
	}
	if _, err := planObligationRetirement(f, actor, "source"); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatal("expired grant accepted", err)
	}
	f.last, f.grant.ExpiresAtSeq = 10, 0
	f.grant.Targets = []domain.GrantTarget{domain.ObligationGrantTarget("s", "o", 1)}
	if _, err := planObligationRetirement(f, actor, "source"); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatal("grant followed latest version", err)
	}
	f.grant.Targets, f.grant.TargetIDs = nil, []string{"o"}
	if _, err := planObligationRetirement(f, actor, "source"); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatal("legacy stable-ID grant accepted", err)
	}
}
