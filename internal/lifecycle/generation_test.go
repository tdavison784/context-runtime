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

func TestGenerationPairsFollowClosedPolicy(t *testing.T) {
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
			mem := memory.New()
			t.Cleanup(func() { mem.Close() })
			s, _ := New(mem, testPolicy())
			it := storetest.NewItem("s", "item", 0, "content")
			it.Kind, it.Generation = tc.kind, tc.from
			if tc.kind == domain.KindGoal {
				it = storetest.NewGoal("s", "item", 0, "content")
				it.Generation = tc.from
			}
			seedItem(t, mem, it)
			r, err := s.generationRun(ctx, p, domain.PromoteIntent{ItemMutationIntent: domain.ItemMutationIntent{RequestID: "r", ItemID: "item", ExpectedVersion: 1}, Generation: tc.to}, tc.action)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if tc.want == nil && (r.After.Generation != tc.to || r.Before.Authority != r.After.Authority) {
				t.Fatalf("result: %+v", r)
			}
			if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
				got, err := tx.Item("item")
				if tc.want == nil && (got.Retention != tc.retention || got.Kind != tc.kind || got.Access != it.Access) || tc.want != nil && got.Version != 1 {
					t.Fatalf("stored: %+v", got)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func (s *Service) generationRun(ctx context.Context, p domain.Principal, i domain.PromoteIntent, action domain.Action) (domain.ItemMutationResult, error) {
	if action == domain.ActionPromote {
		return s.PromoteStandalone(ctx, p, i)
	}
	return s.DemoteStandalone(ctx, p, i)
}

func TestGenerationExcludesObligationSourceAndNeedsAuthority(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	source := storetest.NewItem("s", "source", 0, "source")
	sys := storetest.NewItem("s", "sys", 0, "system fact")
	sys.Authority = domain.AuthoritySystem
	seedItem(t, mem, source)
	seedItem(t, mem, sys)
	if err := mem.Update(ctx, "s", func(tx store.Tx) error {
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
	grantTo(t, mem, "promote-grant", domain.ActionPromote, "sys", user)
	var out LifecycleOutcome
	if err := mem.Update(ctx, "s", func(tx store.Tx) error {
		var err error
		out, err = s.Promote(tx, user, promote("sys", "r2"), tx.NextSeq())
		return err
	}); err != nil || out.GrantID != "promote-grant" {
		t.Fatalf("granted promote: %+v %v", out, err)
	}
	// Replay returns the frozen result; a changed target generation conflicts.
	replay, err := s.PromoteStandalone(ctx, user, promote("sys", "r2"))
	if err != nil || replay.AuditID != out.Result.AuditID {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	changed := promote("sys", "r2")
	changed.Generation = domain.GenerationPinned
	if _, err := s.PromoteStandalone(ctx, user, changed); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Fatalf("changed arguments replayed: %v", err)
	}
}

// SPEC-1.8: PINNED→DURABLE is exclusively Unpin, so every item Promote can pin
// (including an unkeyed residual instruction) must be reachable by Unpin.
func TestPromotedUnkeyedPinCanBeUnpinned(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		it := storetest.NewItem("s", "residual", 0, "always cite sources")
		it.Kind, it.Generation, it.Retention = domain.KindInstruction, domain.GenerationDurable, domain.RetentionHigh
		seedItem(t, db, it)
		user := storetest.NewPrincipal("s", domain.AuthorityUser)
		pin := domain.PromoteIntent{ItemMutationIntent: domain.ItemMutationIntent{RequestID: "p", ItemID: "residual", ExpectedVersion: 1}, Generation: domain.GenerationPinned}
		if r, err := s.PromoteStandalone(ctx, user, pin); err != nil || r.After.Generation != domain.GenerationPinned {
			t.Fatalf("promote: %+v %v", r, err)
		}
		r, err := s.UnpinStandalone(ctx, user, domain.UnpinIntent{RequestID: "u", ItemID: "residual", ExpectedVersion: 2})
		if err != nil || r.After.Generation != domain.GenerationDurable || r.Before.Currentness != domain.ItemUnkeyed {
			t.Fatalf("unpin of promoted unkeyed pin: %+v %v", r, err)
		}
		// Resolve keeps its DIRECTIVE-only target rule.
		goal := storetest.NewGoal("s", "plain-goal", 0, "unkeyed goal")
		seedItem(t, db, goal)
		if _, err := s.ResolveStandalone(ctx, user, domain.ResolveIntent{RequestID: "r", ItemID: "plain-goal", ExpectedVersion: 1}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("resolve reached a non-directive goal: %v", err)
		}
	})
}
