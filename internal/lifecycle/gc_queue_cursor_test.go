package lifecycle

import (
	"context"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/gcqueue"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// enqueueMany persists n requests of trigger for task "task" (stored first),
// then one more whose trigger identity is "target"; it returns that
// request's ID.
func enqueueMany(t *testing.T, db store.Store, pol domain.Phase3Policy, trigger domain.GCTrigger, n int, targetTrigger domain.GCTrigger) string {
	t.Helper()
	origin := storetest.NewPrincipal("s", domain.AuthoritySystem)
	var target string
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		if _, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
			return err
		}
		for i := range n {
			if _, err := gcqueue.Enqueue(tx, pol, origin, trigger, "task", fmt.Sprintf("prefix-%03d", i)); err != nil {
				return err
			}
		}
		var err error
		target, err = gcqueue.Enqueue(tx, pol, origin, targetTrigger, "task", "target")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return target
}

// DUR-3.2 (J6): queue continuation is durable. Requests behind a prefix
// longer than one call's page cap are reached by later calls, even through
// a new executor (restart), and disabled triggers never fill a page.
func TestGCQueueContinuesDurablyPastSkippedPrefix(t *testing.T) {
	const prefix = 129
	ctx := context.Background()
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	pol := testPolicy()
	pol.MaxPageSize = 2

	t.Run("skipped by collector", func(t *testing.T) {
		eachStore(t, func(t *testing.T, db store.Store) {
			target := enqueueMany(t, db, pol, domain.GCSupersession, prefix, domain.GCSupersession)
			only := func(r domain.GCRequest) (domain.Principal, bool) { return harness, r.ID == target }
			for pass := 1; pass <= 3; pass++ {
				s, _ := New(db, pol) // a fresh executor every call: no in-memory scan state
				if n, err := s.CollectPending(ctx, "s", only, 1); err != nil {
					t.Fatalf("pass %d: %v", pass, err)
				} else if n == 1 {
					if res, found := gcResult(t, db, target); !found || res.Outcome != domain.GCCollected {
						t.Fatalf("pass %d: %+v", pass, res)
					}
					return
				}
			}
			t.Fatalf("request behind %d skipped requests starved", prefix)
		})
	})
	t.Run("disabled trigger prefix", func(t *testing.T) {
		eachStore(t, func(t *testing.T, db store.Store) {
			completionOff := pol
			completionOff.GCTriggers = []domain.GCTrigger{domain.GCManual, domain.GCSupersession}
			// TASK_COMPLETION persists even when disabled (P3-39).
			target := enqueueMany(t, db, pol, domain.GCTaskCompletion, prefix, domain.GCSupersession)
			s, _ := New(db, completionOff)
			pick := func(domain.GCRequest) (domain.Principal, bool) { return harness, true }
			if n, err := s.CollectPending(ctx, "s", pick, 1); n != 1 || err != nil {
				t.Fatalf("enabled request behind %d disabled ones: n=%d err=%v", prefix, n, err)
			}
			if res, found := gcResult(t, db, target); !found || res.Outcome != domain.GCCollected {
				t.Fatalf("target: %+v", res)
			}
		})
	})
}
