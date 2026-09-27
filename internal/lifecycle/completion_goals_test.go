package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestCompletionPlansAllGoalsBeforeEffects(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		mem := memory.New()
		t.Cleanup(func() { mem.Close() })
		s, _ := New(mem, testPolicy())
		p := storetest.NewPrincipal("s", domain.AuthoritySystem)
		err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
			var goals []domain.ContextItem
			for _, id := range []string{"first", "second"} {
				it := storetest.NewGoal("s", id, tx.NextSeq(), "goal")
				it.Scope, it.Access = domain.ScopeTask, storetest.DirectiveBoundary("s")
				if hidden && id == "second" {
					it.Access.AgentID = "other-agent"
					it.AgentID = "other-agent"
				}
				if err := tx.InsertItem(it); err != nil {
					return err
				}
				goals = append(goals, it)
			}
			seq := tx.NextSeq()
			b := workBudget{remaining: 64, pageSize: 2}
			plan, err := s.planCompletionGoals(tx, p, "request", goals, seq, &b)
			if hidden {
				if err != domain.ErrInvalidAuthorityPromotion || plan != nil {
					t.Fatalf("hidden goal: %+v %v", plan, err)
				}
			} else {
				if err != nil || len(plan) != 2 || plan[0].audit.Seq != seq || plan[1].audit.Seq <= seq {
					t.Fatalf("plan: %+v %v", plan, err)
				}
			}
			for _, it := range goals {
				current, err := tx.Item(it.ID)
				if err != nil || *current.GoalStatus != domain.GoalOpen || current.Version != 1 {
					t.Fatal("preflight mutated a goal")
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
