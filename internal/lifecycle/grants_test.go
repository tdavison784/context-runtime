package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func archiveGrant(req, grant, item string, grantee domain.Principal) domain.GrantIntent {
	return domain.GrantIntent{RequestID: req, GrantID: grant, Action: domain.ActionArchive, Targets: []domain.GrantTarget{domain.ItemGrantTarget("s", item)}, Grantee: &grantee}
}

func TestIssueGrantDerivesIssuerAndAuthorizesGrantee(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	sys := storetest.NewItem("s", "sys", 0, "system fact")
	sys.Authority = domain.AuthoritySystem
	seedItem(t, mem, sys)
	system, harness := storetest.NewPrincipal("s", domain.AuthoritySystem), storetest.NewPrincipal("s", domain.AuthorityHarness)
	intent := archiveGrant("g1", "grant-1", "sys", harness)
	var out MutationOutcome
	if err := mem.Update(ctx, "s", func(tx store.Tx) error {
		var err error
		out, err = s.IssueGrant(tx, system, intent, tx.NextSeq())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if out.GrantID != "" || out.Result.Records == nil || out.Result.Records.Kind != "GRANT" || out.Result.Records.IDs[0] != "grant-1" || out.Result.Validate() != nil {
		t.Fatalf("issue outcome: %+v", out)
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		g, err := tx.Grant("grant-1")
		if err != nil || g.Issuer != system || g.IssuedSeq == 0 || g.RevokedSeq != 0 {
			t.Fatalf("stored grant: %+v %v", g, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IssueGrantStandalone(ctx, system, intent); err != nil {
		t.Fatalf("replay: %v", err)
	}
	changed := intent
	changed.ExpiresAtSeq = 100
	if _, err := s.IssueGrantStandalone(ctx, system, changed); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Fatalf("changed payload replayed: %v", err)
	}
	if _, err := s.ArchiveStandalone(ctx, harness, domain.ArchiveIntent{RequestID: "a", ItemID: "sys", ExpectedVersion: 1}); err != nil {
		t.Fatalf("granted archive: %v", err)
	}
}

func TestIssueGrantFailsClosed(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	sys := storetest.NewItem("s", "sys", 0, "system fact")
	sys.Authority = domain.AuthoritySystem
	private := storetest.NewItem("s", "private", 0, "private")
	private.Scope, private.AgentID, private.Access = domain.ScopeAgent, "other", domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "other"}
	seedItem(t, mem, sys)
	seedItem(t, mem, private)
	user, harness := storetest.NewPrincipal("s", domain.AuthorityUser), storetest.NewPrincipal("s", domain.AuthorityHarness)
	complete := archiveGrant("c", "grant-c", "sys", harness)
	complete.Action = domain.ActionCompleteTask
	for name, tc := range map[string]struct {
		actor  domain.Principal
		intent domain.GrantIntent
		want   error
	}{
		"issuer lacks target authority": {user, archiveGrant("r1", "g1", "sys", harness), domain.ErrInvalidAuthorityPromotion},
		"agent issuer":                  {storetest.NewPrincipal("s", domain.AuthorityAgent), archiveGrant("r2", "g2", "sys", harness), domain.ErrInvalidAuthorityPromotion},
		"future target":                 {user, archiveGrant("r3", "g3", "later", harness), domain.ErrNotFound},
		"inaccessible target":           {user, archiveGrant("r4", "g4", "private", harness), domain.ErrNotFound},
		"agent grantee":                 {user, archiveGrant("r5", "g5", "private", storetest.NewPrincipal("s", domain.AuthorityAgent)), domain.ErrInvalidAuthorityPromotion},
		"reserved complete action":      {user, complete, domain.ErrInvalidRecord},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := s.IssueGrantStandalone(ctx, tc.actor, tc.intent); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		grants, err := tx.Grants()
		if len(grants) != 0 {
			t.Fatalf("failed issuance stored grants: %+v", grants)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeGrantNeedsDirectAuthorityAndEndsAuthorization(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	sys := storetest.NewItem("s", "sys", 0, "system fact")
	sys.Authority = domain.AuthoritySystem
	seedItem(t, mem, sys)
	system, harness := storetest.NewPrincipal("s", domain.AuthoritySystem), storetest.NewPrincipal("s", domain.AuthorityHarness)
	if _, err := s.IssueGrantStandalone(ctx, system, archiveGrant("g", "grant", "sys", harness)); err != nil {
		t.Fatal(err)
	}
	revoke := domain.RevokeGrantIntent{RequestID: "rv", GrantID: "grant"}
	for _, a := range []domain.Authority{domain.AuthorityUser, domain.AuthorityHarness, domain.AuthorityAgent} {
		if _, err := s.RevokeGrantStandalone(ctx, storetest.NewPrincipal("s", a), revoke); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("%s revoked SYSTEM-target grant: %v", a, err)
		}
	}
	if _, err := s.RevokeGrantStandalone(ctx, system, domain.RevokeGrantIntent{RequestID: "rv", GrantID: "missing"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing grant: %v", err)
	}
	if r, err := s.RevokeGrantStandalone(ctx, system, revoke); err != nil || r.IDs[0] != "grant" {
		t.Fatalf("revoke: %+v %v", r, err)
	}
	if _, err := s.RevokeGrantStandalone(ctx, system, revoke); err != nil {
		t.Fatalf("revocation replay: %v", err)
	}
	if _, err := s.RevokeGrantStandalone(ctx, system, domain.RevokeGrantIntent{RequestID: "rv2", GrantID: "grant"}); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("second revocation: %v", err)
	}
	if _, err := s.ArchiveStandalone(ctx, harness, domain.ArchiveIntent{RequestID: "a", ItemID: "sys", ExpectedVersion: 1}); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatalf("revoked grant authorized archive: %v", err)
	}
}
