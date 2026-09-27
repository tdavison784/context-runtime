package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// enqueue persists a SUPERSESSION request for task "task" (stored on first
// use); Phase 3 has no session-scoped requests (H4).
func enqueue(t *testing.T, db store.Store, s *Service, trigger string) {
	t.Helper()
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		if _, err := tx.Task("task"); errors.Is(err, domain.ErrNotFound) {
			if _, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
				return err
			}
		}
		_, err := s.EnqueueGC(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), domain.GCSupersession, domain.CollectTask, "task", trigger)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// DUR-1.6 / SPEC-1.16 / SEC-1.6: skipped and failing requests never block
// later ones; the queue is paged, and only executions count toward max.
func TestGCQueueNeverBlocksBehindSkippedOrFailingRequests(t *testing.T) {
	ctx := context.Background()
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	pick := func(domain.GCRequest) (domain.Principal, bool) { return harness, true }

	t.Run("disabled head", func(t *testing.T) {
		eachStore(t, func(t *testing.T, db store.Store) {
			pol := testPolicy()
			pol.GCTriggers = []domain.GCTrigger{domain.GCManual, domain.GCSupersession}
			s, _ := New(db, pol)
			seedCompletion(t, db, nil, "", false)
			if _, err := s.CompleteTaskStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}); err != nil {
				t.Fatal(err)
			}
			enqueue(t, db, s, "event-1")
			for pass := range 2 {
				n, err := s.CollectPending(ctx, "s", pick, 1)
				want := 1 - pass
				if n != want || err != nil {
					t.Fatalf("pass %d: n=%d err=%v, want %d", pass, n, err, want)
				}
			}
			if pending := pendingGC(t, db); len(pending) != 1 || pending[0].Trigger != domain.GCTaskCompletion {
				t.Fatalf("pending: %+v", pending)
			}
		})
	})

	t.Run("failing head", func(t *testing.T) {
		eachStore(t, func(t *testing.T, db store.Store) {
			s, _ := New(db, testPolicy())
			// A request recorded under another policy version can never
			// execute under this one.
			if err := db.Update(ctx, "s", func(tx store.Tx) error {
				sem, err := store.Semantic(tx)
				if err != nil {
					return err
				}
				return sem.InsertGCRequest(domain.GCRequest{SemanticMeta: domain.SemanticMeta{ID: "gcq_old", SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
					CollectIntent: domain.CollectIntent{RequestID: "gc_old", Scope: domain.CollectSession, Trigger: domain.GCSupersession},
					Origin:        storetest.NewPrincipal("s", domain.AuthoritySystem), PolicyVersion: "phase3-policy/v0"})
			}); err != nil {
				t.Fatal(err)
			}
			enqueue(t, db, s, "event-1")
			for pass := range 3 {
				n, err := s.CollectPending(ctx, "s", pick, 4)
				want := 1
				if pass > 0 {
					want = 0
				}
				if n != want || !errors.Is(err, domain.ErrUnsupportedSchema) {
					t.Fatalf("pass %d: n=%d err=%v, want %d and the head failure reported", pass, n, err, want)
				}
			}
			if pending := pendingGC(t, db); len(pending) != 1 || pending[0].ID != "gcq_old" {
				t.Fatalf("pending: %+v", pending)
			}
		})
	})

	t.Run("skips span pages", func(t *testing.T) {
		eachStore(t, func(t *testing.T, db store.Store) {
			pol := testPolicy()
			pol.MaxPageSize = 2
			s, _ := New(db, pol)
			for i := range 5 {
				enqueue(t, db, s, fmt.Sprintf("event-%d", i))
			}
			var last string
			for _, r := range pendingGC(t, db) {
				last = r.ID
			}
			only := func(r domain.GCRequest) (domain.Principal, bool) { return harness, r.ID == last }
			if n, err := s.CollectPending(ctx, "s", only, 1); n != 1 || err != nil {
				t.Fatalf("runnable request behind two pages of skips: n=%d err=%v", n, err)
			}
		})
	})
}

// SEC-1.6: never-collectible live items must not exhaust the collection's
// work bound; only possibly-archivable candidates pay for protection reads.
func TestLiveItemsDoNotExhaustCollectionBudget(t *testing.T) {
	const live = 600
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxTransactionWork, pol.MaxGCDecisions, pol.MaxReceiptBytes = 4096, 4096, 1<<20 // DefaultPhase3Policy limits
		s, _ := New(db, pol)
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			if _, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
				return err
			}
			for i := range live {
				if err := tx.InsertItem(storetest.NewItem("s", fmt.Sprintf("fact-%03d", i), tx.NextSeq(), "fact")); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		out, err := collect(newFacets(), db, s, storetest.NewPrincipal("s", domain.AuthoritySystem), domain.CollectIntent{RequestID: "c", Scope: domain.CollectSession, Trigger: domain.GCManual})
		if err != nil || len(out.Result.Collect.Decisions) != live || len(out.Result.Collect.ArchivedRefs) != 0 {
			t.Fatalf("session collection over %d live facts: %v", live, err)
		}
	})
}
