package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_9_BroadScopeSourceWithOriginatingTaskIsNotTaskOwned: the owning
// scope comes from the source, never merely from its originating TaskID. A
// WORKFLOW-scope OPEN goal that originated in task T — even one the
// completing principal cannot access — is not a member of T's completion:
// CompleteTask(T) succeeds without enumerating, authorizing, resolving, or
// waiving it, while T's own TASK-scope goal is resolved as usual. The broad
// goal stays OPEN and untouched.
func TestP3_9_BroadScopeSourceWithOriginatingTaskIsNotTaskOwned(t *testing.T) {
	eachStore(t, func(t *testing.T, mem store.Store) {
		s, _ := New(mem, testPolicy())
		p := storetest.NewPrincipal("s", domain.AuthorityUser)
		if err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
			// T's own requirement: TASK scope, session-visible.
			owned := storetest.NewGoal("s", "task-goal", tx.NextSeq(), "owned by the task")
			owned.Scope, owned.Access = domain.ScopeTask, storetest.DirectiveBoundary("s")
			if err := tx.InsertItem(owned); err != nil {
				return err
			}
			// A broad-scope source that merely originated in T, kept private
			// to another agent so an accidental enumeration would surface it
			// as a hidden requirement and fail the completion.
			broad := storetest.NewGoal("s", "wf-goal", tx.NextSeq(), "workflow-wide requirement")
			broad.Scope = domain.ScopeWorkflow
			broad.WorkflowID = "other-wf"
			broad.Access = domain.AccessBoundary{Scope: domain.ScopeWorkflow, SessionID: "s", WorkflowID: "other-wf"}
			if err := tx.InsertItem(broad); err != nil {
				return err
			}
			_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
			return err
		}); err != nil {
			t.Fatal(err)
		}

		res, err := completeTask(newFacets("task-goal"), mem, s, p, domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, true)
		if err != nil {
			t.Fatalf("completion over a broad-scope originating goal: %v", err)
		}
		rec := res.Result.Completion
		if rec == nil || len(rec.ResolvedGoals) != 1 || rec.ResolvedGoals[0].ItemID != "task-goal" {
			t.Fatalf("receipt resolved %v; want exactly [task-goal]", rec.ResolvedGoals)
		}
		if err := mem.View(context.Background(), "s", func(tx store.ReadTx) error {
			task, err := tx.Task("task")
			if err != nil || task.Status != domain.TaskCompleted {
				t.Fatalf("task after completion: %+v %v", task, err)
			}
			owned, err := tx.Item("task-goal")
			if err != nil || *owned.GoalStatus != domain.GoalResolved || owned.Version != 2 {
				t.Fatalf("task-owned goal not resolved: %+v %v", owned, err)
			}
			broad, err := tx.Item("wf-goal")
			if err != nil || *broad.GoalStatus != domain.GoalOpen || broad.Version != 1 || broad.TaskID != "task" {
				t.Fatalf("broad-scope goal changed by another task's completion: %+v %v", broad, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
