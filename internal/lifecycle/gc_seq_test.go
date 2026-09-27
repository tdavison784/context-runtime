package lifecycle

import (
	"context"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func lastSeq(t *testing.T, db store.Store) uint64 {
	t.Helper()
	var seq uint64
	if err := db.View(context.Background(), "s", func(tx store.ReadTx) error { seq = tx.LastSeq(); return nil }); err != nil {
		t.Fatal(err)
	}
	return seq
}

// DUR-1.3: a replayed or already-collected GC request allocates no sequence
// (a prepared call must not go stale) and is not counted as executed.
func TestRepeatedAndConcurrentCollectionConsumesNoSequence(t *testing.T) {
	ctx := context.Background()
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	pick := func(domain.GCRequest) (domain.Principal, bool) { return harness, true }
	complete := func(t *testing.T, db store.Store) *Service {
		seedCompletion(t, db, nil, "", false)
		s, _ := New(db, testPolicy())
		if _, err := s.CompleteTaskStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}); err != nil {
			t.Fatal(err)
		}
		return s
	}
	eachStore(t, func(t *testing.T, db store.Store) {
		s := complete(t, db)
		if n, err := s.CollectPending(ctx, "s", pick, 4); n != 1 || err != nil {
			t.Fatalf("first pass: %d %v", n, err)
		}
		after := lastSeq(t, db)
		if n, err := s.CollectPending(ctx, "s", pick, 4); n != 0 || err != nil || lastSeq(t, db) != after {
			t.Fatalf("second pass: n=%d err=%v seq %d→%d", n, err, after, lastSeq(t, db))
		}
		// A direct replay with a deferred sequence allocates nothing either.
		req := pendingOrDone(t, db, s)
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			_, err := s.ExecuteGCRequest(tx, harness, req, 0)
			return err
		}); err != nil || lastSeq(t, db) != after {
			t.Fatalf("deferred replay: %v seq %d→%d", err, after, lastSeq(t, db))
		}
	})
	eachStore(t, func(t *testing.T, db store.Store) {
		s := complete(t, db)
		before := lastSeq(t, db)
		const workers = 8
		done := make([]int, workers)
		var wg sync.WaitGroup
		for i := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				done[i], _ = s.CollectPending(ctx, "s", pick, 4)
			}()
		}
		wg.Wait()
		total := 0
		for _, n := range done {
			total += n
		}
		single := lastSeq(t, db) - before
		if total != 1 {
			t.Fatalf("concurrent workers executed %d times, want 1", total)
		}
		// Collecting an empty task writes exactly a collect receipt, a GC
		// result link and a mutation receipt.
		if single != 3 {
			t.Fatalf("concurrent collection advanced LastSeq by %d, want 3", single)
		}
	})
}

// pendingOrDone returns the session's single task-completion GC request ID.
func pendingOrDone(t *testing.T, db store.Store, s *Service) string {
	t.Helper()
	var id string
	readSemantic(t, db, func(sem store.SemanticReader) error {
		id = gcRequestID("s", "gc_"+domain.NewCanonicalEncoder("context-runtime/gc-trigger/v1").String("s").String(string(domain.GCTaskCompletion)).String("task").Hash())
		_, err := sem.GCRequest(id)
		return err
	})
	return id
}
