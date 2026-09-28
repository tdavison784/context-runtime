package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_10_PromoteDemoteRefuseInaccessibleTarget closes the MISSING half of
// the P3-42 row "authority/grant/access" (ADR 8 :1099) for generation changes.
// TestGenerationExcludesObligationSourceAndNeedsAuthority covers the authority
// and grant halves but never an inaccessible target, and asserts access
// preservation only through the closed policy table. Through both stores, a
// SYSTEM actor — which outranks every target, so authority can never be the
// reason — is refused on a target whose access boundary belongs to another
// agent exactly as it is on a target that does not exist, for Promote and
// Demote alike. The same actor, the same generation change, and a target it
// can access succeed and carry the item's access boundary across unchanged.
func TestP3_10_PromoteDemoteRefuseInaccessibleTarget(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		visible := storetest.NewItem("s", "visible", 0, "visible fact")
		private := storetest.NewItem("s", "private", 0, "private fact")
		private.AgentID, private.Access = "other", domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "other"}
		private.Scope = domain.ScopeAgent
		seedItem(t, db, visible)
		seedItem(t, db, private)
		system := storetest.NewPrincipal("s", domain.AuthoritySystem)
		promote := func(id, req string) domain.PromoteIntent {
			return domain.PromoteIntent{ItemMutationIntent: domain.ItemMutationIntent{RequestID: req, ItemID: id, ExpectedVersion: 1}, Generation: domain.GenerationDurable}
		}
		demote := func(id, req string) domain.PromoteIntent {
			return domain.PromoteIntent{ItemMutationIntent: domain.ItemMutationIntent{RequestID: req, ItemID: id, ExpectedVersion: 2}, Generation: domain.GenerationWorking}
		}

		for _, id := range []string{"private", "missing"} {
			if _, err := s.PromoteStandalone(ctx, system, promote(id, "pr-"+id)); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("promote %s by full authority: %v, want ErrNotFound", id, err)
			}
		}
		// Control: the same actor and the same change reach a target they can
		// access, and the boundary is carried across unchanged.
		if r, err := s.PromoteStandalone(ctx, system, promote("visible", "pr-visible")); err != nil || r.After.Generation != domain.GenerationDurable {
			t.Fatalf("accessible promote: %+v %v", r, err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			got, err := tx.Item("visible")
			if err != nil {
				return err
			}
			if got.Access != visible.Access || got.Authority != visible.Authority || got.Version != 2 {
				t.Errorf("promote changed the target's boundary: %+v", got)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		for _, id := range []string{"private", "missing"} {
			if _, err := s.DemoteStandalone(ctx, system, demote(id, "de-"+id)); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("demote %s by full authority: %v, want ErrNotFound", id, err)
			}
		}
		if r, err := s.DemoteStandalone(ctx, system, demote("visible", "de-visible")); err != nil || r.After.Generation != domain.GenerationWorking {
			t.Fatalf("accessible demote: %+v %v", r, err)
		}

		// The refused attempts left the private target exactly as seeded.
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			got, err := tx.Item("private")
			if err != nil {
				return err
			}
			if got.Version != 1 || got.Generation != domain.GenerationWorking || got.Access != private.Access || got.AgentID != "other" {
				t.Errorf("refused generation attempts changed the target: %+v", got)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
