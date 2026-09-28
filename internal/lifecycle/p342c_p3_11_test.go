package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// P3-11 (ADR 8 line 1267). The cited TestIssueGrantFailsClosed
// (grants_test.go:62) and TestRevokeGrantNeedsDirectAuthorityAndEnds-
// Authorization (grants_test.go:105) run on memory.New() only; these are
// the same denial matrices on both stores, memory and sqlitetest.
//
// Issuance fails closed for a lower-authority or agent issuer, a future or
// inaccessible target, an agent grantee and the reserved complete action —
// and stores nothing. Revocation needs the grant's own issuing authority:
// USER/HARNESS/AGENT cannot revoke a SYSTEM-target grant, a missing grant
// is ErrNotFound, the real revocation works and replays, a second
// revocation with a fresh request is ErrInvalidTransition, and the revoked
// grant no longer authorizes the archive it used to.

func TestP3_11_IssueGrantFailsClosedOnBothStores(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		sys := storetest.NewItem("s", "sys", 0, "system fact")
		sys.Authority = domain.AuthoritySystem
		private := storetest.NewItem("s", "private", 0, "private")
		private.Scope, private.AgentID, private.Access = domain.ScopeAgent, "other", domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "other"}
		seedItem(t, db, sys)
		seedItem(t, db, private)
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
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			grants, err := tx.Grants()
			if len(grants) != 0 {
				t.Fatalf("failed issuance stored grants: %+v", grants)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestP3_11_RevokeGrantNeedsDirectAuthorityOnBothStores(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		sys := storetest.NewItem("s", "sys", 0, "system fact")
		sys.Authority = domain.AuthoritySystem
		seedItem(t, db, sys)
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
		if _, err := s.RevokeGrantStandalone(ctx, system, domain.RevokeGrantIntent{RequestID: "rv-m", GrantID: "missing"}); !errors.Is(err, domain.ErrNotFound) {
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
	})
}
