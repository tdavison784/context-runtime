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

type rejectGC struct {
	store.SemanticTx
	check func(domain.GCRequest) error
}

func (f rejectGC) InsertGCRequest(r domain.GCRequest) error { return f.check(r) }

func TestCompletionGCFailureRollsBackGoalsAndTask(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	p := storetest.NewPrincipal("s", domain.AuthorityUser)
	if err := mem.Update(ctx, "s", func(tx store.Tx) error {
		goal := storetest.NewGoal("s", "goal", tx.NextSeq(), "goal")
		goal.Scope, goal.Access = domain.ScopeTask, storetest.DirectiveBoundary("s")
		if err := tx.InsertItem(goal); err != nil {
			return err
		}
		_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("injected GC request failure")
	err := mem.Update(ctx, "s", func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		goal, _ := tx.Item("goal")
		task, _ := tx.Task("task")
		seq := tx.NextSeq()
		b := workBudget{remaining: 64, pageSize: 2}
		plan, err := s.planCompletionGoals(tx, p, "request", []domain.ContextItem{goal}, seq, &b)
		if err != nil {
			return err
		}
		fault := rejectGC{SemanticTx: sem, check: func(r domain.GCRequest) error {
			if err := r.Validate(); err != nil {
				t.Fatal(err)
			}
			g, _ := tx.Item("goal")
			done, _ := tx.Task("task")
			if *g.GoalStatus != domain.GoalResolved || done.Status != domain.TaskCompleted || r.Origin != p || r.Trigger != domain.GCTaskCompletion {
				t.Fatal("missing atomic completion constituent")
			}
			return failure
		}}
		out, err := s.writeCompletion(tx, fault, p, domain.CompleteTaskIntent{RequestID: "request", TaskID: "task"}, task, plan, seq)
		if err != failure || out.TaskID != "" {
			t.Fatalf("failed result: %+v %v", out, err)
		}
		return nil
	})
	if !errors.Is(err, failure) {
		t.Fatalf("ignored failure committed: %v", err)
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		g, _ := tx.Item("goal")
		task, _ := tx.Task("task")
		if tx.LastSeq() != 2 || g.Version != 1 || *g.GoalStatus != domain.GoalOpen || task.Status != domain.TaskActive {
			t.Fatal("partial completion escaped rollback")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
