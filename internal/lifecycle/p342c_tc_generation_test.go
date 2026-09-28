package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestTC_P3_10_GenerationAuthorityAndGrantHalvesOnBothStores closes the
// memory-only halves of the P3-42 row "authority/grant/access" (ADR 8 :1258).
// TestP3_10_PromoteDemoteRefuseInaccessibleTarget covers the access half on
// both stores; TestGenerationExcludesObligationSourceAndNeedsAuthority covers
// the authority and grant halves on one memory store. The same two halves now
// run on both backends: a current obligation source is never a generation
// target, USER and AGENT cannot promote a SYSTEM item, and a promote grant
// issued to the USER authorizes exactly the granted promote.
func TestTC_P3_10_GenerationAuthorityAndGrantHalvesOnBothStores(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		source := storetest.NewItem("s", "source", 0, "source")
		sys := storetest.NewItem("s", "sys", 0, "system fact")
		sys.Authority = domain.AuthoritySystem
		seedItem(t, db, source)
		seedItem(t, db, sys)
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			return tx.InsertObligationVersion(storetest.NewObligation("s", "o", 1, tx.NextSeq(), "source"))
		}); err != nil {
			t.Fatal(err)
		}
		user := storetest.NewPrincipal("s", domain.AuthorityUser)
		promote := func(id, req string) domain.PromoteIntent {
			return domain.PromoteIntent{ItemMutationIntent: domain.ItemMutationIntent{RequestID: req, ItemID: id, ExpectedVersion: 1}, Generation: domain.GenerationDurable}
		}
		if _, err := s.PromoteStandalone(ctx, user, promote("source", "r1")); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Fatalf("current obligation source promoted: %v", err)
		}
		if _, err := s.PromoteStandalone(ctx, user, promote("sys", "r2")); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("USER promoted SYSTEM item: %v", err)
		}
		if _, err := s.PromoteStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityAgent), promote("sys", "r3")); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("AGENT promoted: %v", err)
		}
		grantTo(t, db, "promote-grant", domain.ActionPromote, "sys", user)
		var out LifecycleOutcome
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			var err error
			out, err = s.Promote(tx, user, promote("sys", "r2"), tx.NextSeq())
			return err
		}); err != nil || out.GrantID != "promote-grant" {
			t.Fatalf("granted promote: %+v %v", out, err)
		}
	})
}

// TestTC_P3_10_RetentionDerivationOnBothStores closes the memory-only half of
// the P3-42 row "retention derivation" (ADR 8 :1259).
// TestGenerationPairsFollowClosedPolicy asserts the derived retention only on
// one memory store (the pure pair table is internal/policy's half); the same
// table now runs on both backends so the stored item's retention, kind and
// boundary are asserted against the real SQLite persistence.
func TestTC_P3_10_RetentionDerivationOnBothStores(t *testing.T) {
	ctx := context.Background()
	p := storetest.NewPrincipal("s", domain.AuthorityUser)
	for name, tc := range map[string]struct {
		kind      domain.Kind
		from, to  domain.Generation
		action    domain.Action
		retention domain.RetentionClass
		want      error
	}{
		"ephemeral to working":   {domain.KindFact, domain.GenerationEphemeral, domain.GenerationWorking, domain.ActionPromote, domain.RetentionNormal, nil},
		"working to durable":     {domain.KindFact, domain.GenerationWorking, domain.GenerationDurable, domain.ActionPromote, domain.RetentionHigh, nil},
		"durable constraint pin": {domain.KindConstraint, domain.GenerationDurable, domain.GenerationPinned, domain.ActionPromote, domain.RetentionProtected, nil},
		"durable fact pin":       {domain.KindFact, domain.GenerationDurable, domain.GenerationPinned, domain.ActionPromote, "", domain.ErrInvalidTransition},
		"durable to working":     {domain.KindFact, domain.GenerationDurable, domain.GenerationWorking, domain.ActionDemote, domain.RetentionNormal, nil},
		"working to ephemeral":   {domain.KindFact, domain.GenerationWorking, domain.GenerationEphemeral, domain.ActionDemote, domain.RetentionLow, nil},
		"pinned only via unpin":  {domain.KindFact, domain.GenerationPinned, domain.GenerationDurable, domain.ActionDemote, "", domain.ErrInvalidTransition},
		"skip a generation":      {domain.KindFact, domain.GenerationEphemeral, domain.GenerationDurable, domain.ActionPromote, "", domain.ErrInvalidTransition},
		"goal excluded":          {domain.KindGoal, domain.GenerationWorking, domain.GenerationDurable, domain.ActionPromote, "", domain.ErrInvalidTransition},
	} {
		t.Run(name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				s, _ := New(db, testPolicy())
				it := storetest.NewItem("s", "item", 0, "content")
				it.Kind, it.Generation = tc.kind, tc.from
				if tc.kind == domain.KindGoal {
					it = storetest.NewGoal("s", "item", 0, "content")
					it.Generation = tc.from
				}
				seedItem(t, db, it)
				r, err := s.generationRun(ctx, p, domain.PromoteIntent{ItemMutationIntent: domain.ItemMutationIntent{RequestID: "r", ItemID: "item", ExpectedVersion: 1}, Generation: tc.to}, tc.action)
				if !errors.Is(err, tc.want) {
					t.Fatalf("got %v, want %v", err, tc.want)
				}
				if tc.want == nil && (r.After.Generation != tc.to || r.Before.Authority != r.After.Authority) {
					t.Fatalf("result: %+v", r)
				}
				if err := db.View(ctx, "s", func(tx store.ReadTx) error {
					got, err := tx.Item("item")
					if tc.want == nil && (got.Retention != tc.retention || got.Kind != tc.kind || got.Access != it.Access) || tc.want != nil && got.Version != 1 {
						t.Fatalf("stored: %+v", got)
					}
					return err
				}); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
