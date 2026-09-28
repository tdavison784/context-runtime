package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestTC_P3_9_EmptyGoalSetStillRecordsReceiptAndGCOnBothStores closes the
// memory-only half of the P3-42 row "empty goal set" (ADR 8 :1251).
// TestCompleteTaskWithoutGoalsStillRecordsReceiptAndGC builds one memory
// store; the same scenario now runs on both backends: completing a task with
// no goals still records a validated receipt with no resolved goals, no
// grant, and the GC request.
func TestTC_P3_9_EmptyGoalSetStillRecordsReceiptAndGCOnBothStores(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		seedCompletion(t, db, nil, "", false)
		out, err := completeTask(newFacets(), db, s, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, true)
		if err != nil || out.MutationReceiptID == "" || out.GrantID != "" || out.Result.Completion == nil || len(out.Result.Completion.ResolvedGoals) != 0 || out.Result.Validate() != nil {
			t.Fatalf("empty completion: %+v %v", out, err)
		}
		if got := pendingGC(t, db); len(got) != 1 || got[0].Trigger != domain.GCTaskCompletion {
			t.Fatalf("empty completion GC request: %+v", got)
		}
	})
}

// TestTC_P3_9_ForeignWorkflowRejectedBeforeOwnerQueriesOnBothStores closes
// the memory-only half of the P3-42 row "wrong workflow" (ADR 8 :1252).
// TestCompleteTaskRejectsForeignWorkflowBeforeOwnerQueries builds one memory
// store; the same scenario now runs on both backends: a principal from
// another workflow gets the uniform ErrNotFound with no receipt and no
// completion, and the poisoned transaction surfaces the failure even though
// the caller swallows it.
func TestTC_P3_9_ForeignWorkflowRejectedBeforeOwnerQueriesOnBothStores(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		p := storetest.NewPrincipal("s", domain.AuthorityUser)
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		p.WorkflowID = "foreign"
		err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			out, err := s.CompleteTask(tx, p, domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, tx.NextSeq())
			if err != domain.ErrNotFound || out.MutationReceiptID != "" || out.Result.Completion != nil {
				t.Fatalf("completion: %+v %v", out, err)
			}
			return nil
		})
		if err != domain.ErrNotFound {
			t.Fatalf("ignored completion failure: %v", err)
		}
	})
}
