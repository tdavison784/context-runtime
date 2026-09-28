package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// P3-9 (ADR 8 lines 1248 and 1255). Both cited tests run on memory.New()
// only: TestCompleteTaskFailsClosedWithoutPartialEffects's hidden-goal
// case (complete_test.go:158) and TestEveryConstituentWriteIsAtomic's
// complete-task walk (atomicity_test.go), whose walkWrites helper hardcodes
// memory.New(). These are the SQLite halves.
//
// 1248 — a hidden OPEN goal fails the completion closed: the USER cannot
// complete a task one of whose TASK-owned goals is invisible to it, and
// nothing partial survives — no sequence, the task stays ACTIVE, the
// visible goal stays OPEN, no receipt exists.
// 1255 — a crash at ANY constituent write of the completion (each goal
// update and change, the task write, the GC request, the receipt) is
// reported, aborts the whole transaction even when the caller ignores it,
// and commits nothing, not even a sequence number.

func TestP3_9_HiddenOpenGoalFailsCompletionClosedOnBothStores(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		seedCompletion(t, db, []string{"g1", "g2"}, "g2", false)
		var before uint64
		_ = db.View(ctx, "s", func(tx store.ReadTx) error { before = tx.LastSeq(); return nil })
		p := storetest.NewPrincipal("s", domain.AuthorityUser)
		if _, err := completeTask(newFacets("g1", "g2"), db, s, p, domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, true); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("got %v, want %v", err, domain.ErrInvalidAuthorityPromotion)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			task, _ := tx.Task("task")
			g, _ := tx.Item("g1")
			if tx.LastSeq() != before || task.Status != domain.TaskActive || g.GoalStatus == nil || *g.GoalStatus != domain.GoalOpen {
				t.Fatalf("failed completion left effects: seq %d, task %s, goal %+v", tx.LastSeq(), task.Status, g.GoalStatus)
			}
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			if _, err := sem.MutationReceipt(domain.MutationLifecycle, "r"); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("failed completion receipt: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// p342cWalkWrites is walkWrites over a store constructor, so the same
// fail-each-write walk runs on SQLite too: every failing write is reported
// as the injected error, an update that ignores it still commits nothing
// (not even a sequence), and the final full run commits.
func p342cWalkWrites(t *testing.T, newStore func() store.Store, seed func(store.Store), op func(*Service, store.Tx) error, minWrites int) {
	t.Helper()
	ctx := context.Background()
	for fail := 1; ; fail++ {
		for _, ignore := range []bool{false, true} {
			db := newStore()
			seed(db)
			s, _ := New(db, testPolicy())
			var before uint64
			_ = db.View(ctx, "s", func(tx store.ReadTx) error { before = tx.LastSeq(); return nil })
			writes := 0
			var opErr error
			err := db.Update(ctx, "s", func(tx store.Tx) error {
				opErr = op(s, faultTx{Tx: tx, writes: &writes, fail: fail})
				if ignore {
					return nil
				}
				return opErr
			})
			var after uint64
			_ = db.View(ctx, "s", func(tx store.ReadTx) error { after = tx.LastSeq(); return nil })
			db.Close()
			if writes < fail {
				if err != nil || opErr != nil || after == before {
					t.Fatalf("successful run (%d writes): op %v, commit %v, seq %d→%d", writes, opErr, err, before, after)
				}
				if writes < minWrites {
					t.Fatalf("only %d writes observed, want at least %d", writes, minWrites)
				}
				return
			}
			if !errors.Is(opErr, errInjected) {
				t.Fatalf("write %d: op returned %v, want injected failure", fail, opErr)
			}
			if err == nil || after != before {
				t.Fatalf("write %d (ignored=%v): commit %v, seq %d→%d", fail, ignore, err, before, after)
			}
		}
	}
}

func TestP3_9_CompletionCrashesAtEveryWriteAtomicallyOnBothStores(t *testing.T) {
	user := storetest.NewPrincipal("s", domain.AuthorityUser)
	for _, impl := range []struct {
		name      string
		newStore  func() store.Store
		minWrites int
	}{
		{"memory", func() store.Store { return memory.New() }, 7},
		// 2×(goal update + change), task, GC request, receipt — one more
		// than memory when the GC trigger enqueues a queue write.
		{"sqlite", func() store.Store { return sqlitetest.Open(t) }, 7},
	} {
		t.Run(impl.name, func(t *testing.T) {
			p342cWalkWrites(t, impl.newStore,
				func(db store.Store) { seedCompletion(t, db, []string{"g1", "g2"}, "", false) },
				func(s *Service, tx store.Tx) error {
					_, err := s.CompleteTask(tx, user, domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, tx.NextSeq())
					return err
				},
				impl.minWrites)
		})
	}
}
