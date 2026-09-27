package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_11_ChangedGrantPayloadConflictsOnRetry: a committed issue_grant
// replays only its exact original intent. An identical retry returns the
// frozen receipt; every mutated payload field — action, target set, grantee,
// expiry — conflicts under the same request ID, so a retry can never
// reinterpret what was issued.
func TestP3_11_ChangedGrantPayloadConflictsOnRetry(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		ctx := context.Background()
		s, _ := New(db, testPolicy())
		sys := storetest.NewItem("s", "sys", 0, "system fact")
		sys.Authority = domain.AuthoritySystem
		other := storetest.NewItem("s", "other", 0, "other fact")
		other.Authority = domain.AuthoritySystem
		seedItem(t, db, sys)
		seedItem(t, db, other)
		system := storetest.NewPrincipal("s", domain.AuthoritySystem)
		harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
		intent := domain.GrantIntent{RequestID: "g", GrantID: "grant-1", Action: domain.ActionArchive,
			Targets: []domain.GrantTarget{domain.ItemGrantTarget("s", "sys")}, Grantee: &harness}
		if _, err := s.IssueGrantStandalone(ctx, system, intent); err != nil {
			t.Fatal(err)
		}
		if _, err := s.IssueGrantStandalone(ctx, system, intent); err != nil {
			t.Fatalf("identical replay: %v", err)
		}
		for name, mutate := range map[string]func(*domain.GrantIntent){
			"action":     func(i *domain.GrantIntent) { i.Action = domain.ActionResolve },
			"target set": func(i *domain.GrantIntent) { i.Targets = []domain.GrantTarget{domain.ItemGrantTarget("s", "other")} },
			"grantee":    func(i *domain.GrantIntent) { u := storetest.NewPrincipal("s", domain.AuthorityUser); i.Grantee = &u },
			"expiry":     func(i *domain.GrantIntent) { i.ExpiresAtSeq = 100 },
		} {
			t.Run(name, func(t *testing.T) {
				changed := intent
				mutate(&changed)
				if _, err := s.IssueGrantStandalone(ctx, system, changed); !errors.Is(err, domain.ErrEventIDConflict) {
					t.Fatalf("changed %s replayed: %v", name, err)
				}
			})
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			g, err := tx.Grant("grant-1")
			if err != nil || g.Action != domain.ActionArchive || len(g.Targets) != 1 || g.Targets[0] != domain.ItemGrantTarget("s", "sys") ||
				g.Grantee == nil || *g.Grantee != harness || g.ExpiresAtSeq != 0 {
				t.Fatalf("stored grant reinterpreted by a conflicting retry: %+v %v", g, err)
			}
			grants, err := tx.Grants()
			if err != nil || len(grants) != 1 {
				t.Fatalf("stored grants: %+v %v", grants, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
