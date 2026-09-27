package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_32_UnrelatedOwnerCannotRead closes the P3-42 table row "unrelated
// owner cannot read": a principal outside an item's access boundary receives
// the uniform ErrNotFound refusal for lifecycle reads-through-mutations —
// indistinguishable from a missing item — leaves no receipt, while the same
// principal succeeds on an item it can read, proving the refusal is
// access-driven.
func TestP3_32_UnrelatedOwnerCannotRead(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		private := storetest.NewItem("s", "private", 0, "another agent's item")
		private.Scope, private.AgentID = domain.ScopeAgent, "other"
		private.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "other"}
		seedItem(t, db, private)
		seedItem(t, db, storetest.NewItem("s", "plain", 0, "readable fact"))
		s, _ := New(db, testPolicy())
		p := storetest.NewPrincipal("s", domain.AuthorityUser) // agent "agent", unrelated to "other"

		for _, tc := range []struct {
			name string
			call func() error
		}{
			{"archive private", func() error {
				_, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "ra", ItemID: "private", ExpectedVersion: 1})
				return err
			}},
			{"unarchive private", func() error {
				_, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "ru", ItemID: "private", ExpectedVersion: 1})
				return err
			}},
			{"archive missing", func() error {
				_, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "rm", ItemID: "missing", ExpectedVersion: 1})
				return err
			}},
		} {
			if err := tc.call(); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("%s: %v, want the uniform ErrNotFound", tc.name, err)
			}
		}
		readSemantic(t, db, func(sem store.SemanticReader) error {
			for _, id := range []string{"ra", "ru", "rm"} {
				if _, err := sem.MutationReceipt(domain.MutationLifecycle, id); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("refused attempt %s stored a receipt: %v", id, err)
				}
			}
			return nil
		})
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			it, err := tx.Item("private")
			if err != nil || it.Version != 1 || it.Residency != domain.ResidencyResident {
				t.Fatalf("refused attempt changed the item: %+v %v", it, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// Positive control: the same principal succeeds on an item inside its
		// boundary, so the refusals above were access-driven.
		out, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "rok", ItemID: "plain", ExpectedVersion: 1})
		if err != nil || out.After.Residency != domain.ResidencyArchived {
			t.Fatalf("authorized archive: %+v %v", out, err)
		}
	})
}

// TestP3_32_EndingTaskDoesNotArchiveBroadScopeProtectedSource closes the
// P3-42 table row "ending task does not archive broad-scope protected
// source": completing a task and draining its queued collection archives the
// ended task's own scoped content but never a broader-scoped current
// requirement (a session-scoped OPEN goal), which stays resident and open.
func TestP3_32_EndingTaskDoesNotArchiveBroadScopeProtectedSource(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			// A session-scoped OPEN goal: broader than the task, a current
			// requirement.
			if err := tx.InsertItem(storetest.NewGoal("s", "broad-goal", tx.NextSeq(), "session goal")); err != nil {
				return err
			}
			// A task-scoped working fact: owned by the ending task.
			taskFact := storetest.NewItem("s", "task-fact", tx.NextSeq(), "task content")
			taskFact.Scope, taskFact.Access = domain.ScopeTask, storetest.DirectiveBoundary("s")
			if err := tx.InsertItem(taskFact); err != nil {
				return err
			}
			_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		s, _ := New(db, testPolicy())
		out, err := s.CompleteTaskStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"})
		if err != nil || out.GCRequestID == "" {
			t.Fatalf("completion: %+v %v", out, err)
		}
		harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
		if n, err := s.CollectPending(ctx, "s", func(domain.GCRequest) (domain.Principal, bool) { return harness, true }, 4); n != 1 || err != nil {
			t.Fatalf("drain queued collection: n=%d err=%v", n, err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			task, _ := tx.Task("task")
			if task.Status != domain.TaskCompleted {
				t.Fatalf("task: %+v", task)
			}
			goal, err := tx.Item("broad-goal")
			if err != nil || goal.Residency != domain.ResidencyResident || goal.Version != 1 || goal.GoalStatus == nil || *goal.GoalStatus != domain.GoalOpen {
				t.Fatalf("broad-scope protected source archived or mutated: %+v %v", goal, err)
			}
			// The task's own scoped content did not survive: the collection ran.
			fact, _ := tx.Item("task-fact")
			if fact.Residency != domain.ResidencyArchived {
				t.Fatalf("task-scoped content not collected: %+v", fact)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
