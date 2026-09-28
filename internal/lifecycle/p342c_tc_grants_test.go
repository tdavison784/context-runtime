package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestTC_P3_11_ForgedIssuerFailsClosedOnBothStores closes the memory-only
// half of the P3-42 row "forged SYSTEM issuer rejected" (ADR 8 :1263).
// TestIssueGrantFailsClosed builds one memory store; the same scenario now
// runs on both backends: issuers without the target's authority, an agent
// issuer, a future or inaccessible target, an agent grantee and the reserved
// complete action are each refused, and the store holds no grant afterwards.
func TestTC_P3_11_ForgedIssuerFailsClosedOnBothStores(t *testing.T) {
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

// TestTC_P3_11_NewSourceThenVersionGrantOnBothStores closes the memory-only
// half of the P3-42 row "new source then version grant succeeds"
// (ADR 8 :1264). TestIssueGrantDerivesIssuerAndAuthorizesGrantee seeds the
// source in an earlier transaction on one memory store; the same scenario now
// runs on both backends: the issued grant derives its issuer from the actor
// and its allocated sequence, replays, refuses a changed payload, and the
// grantee's archive succeeds through it.
func TestTC_P3_11_NewSourceThenVersionGrantOnBothStores(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		sys := storetest.NewItem("s", "sys", 0, "system fact")
		sys.Authority = domain.AuthoritySystem
		seedItem(t, db, sys)
		system, harness := storetest.NewPrincipal("s", domain.AuthoritySystem), storetest.NewPrincipal("s", domain.AuthorityHarness)
		intent := archiveGrant("g1", "grant-1", "sys", harness)
		var out MutationOutcome
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			var err error
			out, err = s.IssueGrant(tx, system, intent, tx.NextSeq())
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if out.GrantID != "" || out.Result.Records == nil || out.Result.Records.Kind != "GRANT" || out.Result.Records.IDs[0] != "grant-1" || out.Result.Validate() != nil {
			t.Fatalf("issue outcome: %+v", out)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
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
	})
}
