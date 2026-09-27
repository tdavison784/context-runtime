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

// completeLarge completes task "task" owning n archivable scratch items and
// returns its durable GC request ID.
func completeLarge(t *testing.T, db store.Store, s *Service, n int) string {
	t.Helper()
	seedCompletion(t, db, nil, "", false)
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		for i := range n {
			it := storetest.NewItem("s", fmt.Sprintf("scratch-%03d", i), tx.NextSeq(), "scratch")
			it.Scope, it.Access, it.Generation = domain.ScopeTask, storetest.DirectiveBoundary("s"), domain.GenerationEphemeral
			if err := tx.InsertItem(it); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done, err := s.CompleteTaskStandalone(context.Background(), storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"})
	if err != nil {
		t.Fatal(err)
	}
	return done.GCRequestID
}

func gcResult(t *testing.T, db store.Store, id string) (domain.GCResult, bool) {
	t.Helper()
	var r domain.GCResult
	found := false
	readSemantic(t, db, func(sem store.SemanticReader) error {
		var err error
		r, err = sem.GCResult(id)
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		found = err == nil
		return err
	})
	return r, found
}

// H3 (SEC-2.4 / SPEC-2.4 / DUR-2.7): a collection larger than one batch
// proceeds across passes with a durable cursor, one receipt per batch, and
// finishes COLLECTED; it is never failed as a whole.
func TestLargeGCRequestCollectsAcrossBatches(t *testing.T) {
	const items = 60
	ctx := context.Background()
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	pick := func(domain.GCRequest) (domain.Principal, bool) { return harness, true }
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxTransactionWork = 200
		s, _ := New(db, pol)
		id := completeLarge(t, db, s, items)
		passes := 0
		for {
			if _, found := gcResult(t, db, id); found || passes > items {
				break
			}
			passes++
			if _, err := s.CollectPending(ctx, "s", pick, 1); err != nil {
				t.Fatalf("pass %d: %v", passes, err)
			}
		}
		res, found := gcResult(t, db, id)
		if !found || res.Outcome != domain.GCCollected || passes < 2 {
			t.Fatalf("after %d passes: %+v found=%v", passes, res, found)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			for i := range items {
				if it, _ := tx.Item(fmt.Sprintf("scratch-%03d", i)); it.Residency != domain.ResidencyArchived {
					t.Fatalf("scratch-%03d not collected", i)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if pending := pendingGC(t, db); len(pending) != 0 {
			t.Fatalf("collected request still pending: %+v", pending)
		}
	})
}

// H3: a deterministic failure is recorded FAILED with its reason on first
// failure and never retried; a transient failure retries a bounded number of
// times, then FAILED/ATTEMPTS_EXHAUSTED; neither blocks the queue.
func TestFailingGCRequestsAreQuarantined(t *testing.T) {
	ctx := context.Background()
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	pick := func(domain.GCRequest) (domain.Principal, bool) { return harness, true }

	t.Run("permanent", func(t *testing.T) {
		eachStore(t, func(t *testing.T, db store.Store) {
			s, _ := New(db, testPolicy())
			seedCompletion(t, db, nil, "", false)
			if err := db.Update(ctx, "s", func(tx store.Tx) error {
				sem, err := store.Semantic(tx)
				if err != nil {
					return err
				}
				return sem.InsertGCRequest(domain.GCRequest{SemanticMeta: domain.SemanticMeta{ID: "gcq_old", SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
					CollectIntent: domain.CollectIntent{RequestID: "gc_old", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCSupersession},
					Origin:        storetest.NewPrincipal("s", domain.AuthoritySystem), PolicyVersion: "phase3-policy/v0"})
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.CollectPending(ctx, "s", pick, 4); err != nil {
				t.Fatalf("quarantine pass: %v", err)
			}
			res, found := gcResult(t, db, "gcq_old")
			if !found || res.Outcome != domain.GCFailed || res.Reason != domain.GCFailurePolicyMismatch || res.CollectReceiptID != "" {
				t.Fatalf("quarantine record: %+v found=%v", res, found)
			}
			before := lastSeq(t, db)
			if n, err := s.CollectPending(ctx, "s", pick, 4); n != 0 || err != nil || lastSeq(t, db) != before || len(pendingGC(t, db)) != 0 {
				t.Fatalf("quarantined request retried: n=%d err=%v", n, err)
			}
		})
	})
	t.Run("transient exhausted", func(t *testing.T) {
		eachStore(t, func(t *testing.T, db store.Store) {
			pol := testPolicy()
			pol.MaxTransactionWork = 4 // no candidate fits a batch
			s, _ := New(db, pol)
			id := completeLarge(t, db, s, 1)
			for pass := 1; pass <= maxGCAttempts; pass++ {
				n, err := s.CollectPending(ctx, "s", pick, 1)
				if n != 0 {
					t.Fatalf("pass %d executed", pass)
				}
				res, found := gcResult(t, db, id)
				if pass < maxGCAttempts && (found || err == nil) {
					t.Fatalf("pass %d: quarantined early or silent: %+v %v", pass, res, err)
				}
				if pass == maxGCAttempts && (!found || res.Outcome != domain.GCFailed || res.Reason != domain.GCFailureAttemptsExhausted) {
					t.Fatalf("attempts exhausted: %+v found=%v err=%v", res, found, err)
				}
			}
		})
	})
	t.Run("cancelled", func(t *testing.T) {
		eachStore(t, func(t *testing.T, db store.Store) {
			s, _ := New(db, testPolicy())
			id := completeLarge(t, db, s, 1)
			cctx, cancel := context.WithCancel(ctx)
			cancel()
			if n, err := s.CollectPending(cctx, "s", pick, 4); n != 0 || !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled pass: n=%d err=%v", n, err)
			}
			if _, found := gcResult(t, db, id); found {
				t.Fatal("cancellation charged the request")
			}
		})
	})
}
