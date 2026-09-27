package lifecycle

import (
	"context"
	"errors"
	"fmt"
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

// SEC-1.5 / DUR-1.4 (G2, producer half): issuance never creates more live
// grants per (action, target) than authorization's bounded read accepts, so
// issued grants can never wedge authorization of the target.
func TestIssuanceCapsLiveGrantsAtTheAuthorizationReadLimit(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxTargets = 3
		s, _ := New(db, pol)
		sys := storetest.NewItem("s", "sys", 0, "system fact")
		sys.Authority = domain.AuthoritySystem
		seedItem(t, db, sys)
		system := storetest.NewPrincipal("s", domain.AuthoritySystem)
		grantee := func(n int) domain.Principal {
			p := storetest.NewPrincipal("s", domain.AuthorityHarness)
			p.AgentID = fmt.Sprintf("agent-%d", n)
			return p
		}
		for n := range pol.MaxTargets {
			if _, err := s.IssueGrantStandalone(ctx, system, archiveGrant(fmt.Sprintf("g%d", n), fmt.Sprintf("grant-%d", n), "sys", grantee(n))); err != nil {
				t.Fatalf("grant %d: %v", n, err)
			}
		}
		if _, err := s.IssueGrantStandalone(ctx, system, archiveGrant("over", "grant-over", "sys", grantee(99))); !errors.Is(err, domain.ErrResourceLimit) {
			t.Fatalf("live grant beyond the read limit issued: %v", err)
		}
		if _, err := s.ArchiveStandalone(ctx, grantee(0), domain.ArchiveIntent{RequestID: "a", ItemID: "sys", ExpectedVersion: 1}); err != nil {
			t.Fatalf("authorization with a full live set: %v", err)
		}
	})
}

// SEC-1.5 (G2): one target whose grant history exceeds the bounded read is
// not archived, but it cannot abort the rest of the collection.
func TestGrantHistoryOverflowDoesNotAbortCollection(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxTargets = 3
		s, _ := New(db, pol)
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			task := storetest.NewTask("s", "task")
			task.Turn, task.TurnID = 2, "turn-2"
			if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
				return err
			}
			for _, id := range []string{"sys", "plain"} {
				it := storetest.NewItem("s", id, tx.NextSeq(), id)
				it.Generation = domain.GenerationEphemeral // ended turn: collectible
				if id == "sys" {
					it.Authority = domain.AuthoritySystem
				}
				if err := tx.InsertItem(it); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		system, harness := storetest.NewPrincipal("s", domain.AuthoritySystem), storetest.NewPrincipal("s", domain.AuthorityHarness)
		for n := range pol.MaxTargets + 1 {
			id := fmt.Sprintf("grant-%d", n)
			if _, err := s.IssueGrantStandalone(ctx, system, archiveGrant("g-"+id, id, "sys", harness)); err != nil {
				t.Fatal(err)
			}
			if _, err := s.RevokeGrantStandalone(ctx, system, domain.RevokeGrantIntent{RequestID: "r-" + id, GrantID: id}); err != nil {
				t.Fatal(err)
			}
		}
		out, err := collect(newFacets(), db, s, harness, domain.CollectIntent{RequestID: "c", Scope: domain.CollectSession, Trigger: domain.GCManual})
		if err != nil {
			t.Fatalf("collection aborted by one target's grant history: %v", err)
		}
		got := map[string]domain.GCDecisionCode{}
		for _, d := range out.Result.Collect.Decisions {
			got[d.Target.ItemID] = d.Code
		}
		if got["sys"] != domain.GCIneligible || got["plain"] != domain.GCArchive {
			t.Fatalf("decisions: %v", got)
		}
	})
}

// SEC-2.3 / DUR-2.4 (H2): dead grant history never blocks issuance; only
// grants live at the issuance seq count, and authorization precedes the
// capacity check.
func TestDeadGrantHistoryNeverBlocksIssuance(t *testing.T) {
	ctx := context.Background()
	for _, dead := range []string{"revoked", "expired"} {
		t.Run(dead, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				pol := testPolicy()
				pol.MaxTargets = 3
				s, _ := New(db, pol)
				sys := storetest.NewItem("s", "sys", 0, "system fact")
				sys.Authority = domain.AuthoritySystem
				seedItem(t, db, sys)
				system, harness := storetest.NewPrincipal("s", domain.AuthoritySystem), storetest.NewPrincipal("s", domain.AuthorityHarness)
				for n := range pol.MaxTargets + 1 {
					id := fmt.Sprintf("old-%d", n)
					g := archiveGrant("g-"+id, id, "sys", harness)
					if dead == "expired" {
						g.ExpiresAtSeq = lastSeq(t, db) + 1 // valid only through its own issuance seq
					}
					if _, err := s.IssueGrantStandalone(ctx, system, g); err != nil {
						t.Fatalf("history grant %d: %v", n, err)
					}
					if dead == "revoked" {
						if _, err := s.RevokeGrantStandalone(ctx, system, domain.RevokeGrantIntent{RequestID: "r-" + id, GrantID: id}); err != nil {
							t.Fatal(err)
						}
					}
				}
				if _, err := s.IssueGrantStandalone(ctx, system, archiveGrant("fresh", "fresh", "sys", harness)); err != nil {
					t.Fatalf("issuance after %d %s grants: %v", pol.MaxTargets+1, dead, err)
				}
				if _, err := s.ArchiveStandalone(ctx, harness, domain.ArchiveIntent{RequestID: "a", ItemID: "sys", ExpectedVersion: 1}); err != nil {
					t.Fatalf("authorization through the fresh grant: %v", err)
				}
			})
		})
	}
	t.Run("authority before capacity", func(t *testing.T) {
		eachStore(t, func(t *testing.T, db store.Store) {
			pol := testPolicy()
			pol.MaxTargets = 3
			s, _ := New(db, pol)
			sys := storetest.NewItem("s", "sys", 0, "system fact")
			sys.Authority = domain.AuthoritySystem
			seedItem(t, db, sys)
			system := storetest.NewPrincipal("s", domain.AuthoritySystem)
			for n := range pol.MaxTargets {
				p := storetest.NewPrincipal("s", domain.AuthorityHarness)
				p.AgentID = fmt.Sprintf("a%d", n)
				if _, err := s.IssueGrantStandalone(ctx, system, archiveGrant(fmt.Sprintf("g%d", n), fmt.Sprintf("grant-%d", n), "sys", p)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.IssueGrantStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), archiveGrant("u", "user-grant", "sys", storetest.NewPrincipal("s", domain.AuthorityHarness))); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
				t.Fatalf("unauthorized issuer against a full cap: %v", err)
			}
		})
	})
}

// SEC-2.7 (H2): one issuer cannot fill the live-grant cap, and issuers below
// SYSTEM cannot take the slots reserved for SYSTEM.
func TestLiveGrantCapIsSharedFairly(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxTargets = 4 // one live grant per issuer, one slot reserved for SYSTEM
		s, _ := New(db, pol)
		seedItem(t, db, storetest.NewItem("s", "shared", 0, "shared USER fact"))
		user := func(n int) domain.Principal {
			p := storetest.NewPrincipal("s", domain.AuthorityUser)
			p.AgentID = fmt.Sprintf("user-%d", n)
			return p
		}
		issue := func(issuer domain.Principal, id string) error {
			_, err := s.IssueGrantStandalone(ctx, issuer, archiveGrant("r-"+id, id, "shared", issuer))
			return err
		}
		if err := issue(user(0), "u0-a"); err != nil {
			t.Fatal(err)
		}
		if err := issue(user(0), "u0-b"); !errors.Is(err, domain.ErrResourceLimit) {
			t.Fatalf("one USER exceeded its share: %v", err)
		}
		for n := 1; n <= 2; n++ {
			if err := issue(user(n), fmt.Sprintf("u%d", n)); err != nil {
				t.Fatalf("user %d: %v", n, err)
			}
		}
		if err := issue(user(3), "u3"); !errors.Is(err, domain.ErrResourceLimit) {
			t.Fatalf("lower authority took the SYSTEM reserve: %v", err)
		}
		if err := issue(storetest.NewPrincipal("s", domain.AuthoritySystem), "sys"); err != nil {
			t.Fatalf("SYSTEM denied by lower-authority grants: %v", err)
		}
	})
}
