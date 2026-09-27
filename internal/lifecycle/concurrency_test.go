package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestConcurrentCompletionExecutesOnce(t *testing.T) {
	const n = 8
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		seedCompletion(t, db, []string{"g1"}, "", false)
		s, _ := New(db, testPolicy())
		user := storetest.NewPrincipal("s", domain.AuthorityUser)
		results := make([]domain.CompletionReceipt, 2*n)
		errs := make([]error, 2*n)
		var wg sync.WaitGroup
		for i := range 2 * n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Half retry one request identity; half race distinct ones.
				req := "same"
				if i%2 == 1 {
					req = fmt.Sprintf("distinct-%d", i)
				}
				results[i], errs[i] = s.CompleteTaskStandalone(ctx, user, domain.CompleteTaskIntent{RequestID: req, TaskID: "task"})
			}()
		}
		wg.Wait()
		winners, audit := 0, ""
		for i := range 2 * n {
			switch {
			case errs[i] == nil && i%2 == 0:
				if audit != "" && results[i].AuditID != audit {
					t.Fatalf("identical retries diverged: %s vs %s", results[i].AuditID, audit)
				}
				audit = results[i].AuditID
			case errs[i] == nil:
				winners++
			case !errors.Is(errs[i], domain.ErrInvalidTransition):
				t.Fatalf("request %d: %v", i, errs[i])
			}
		}
		// Exactly one request identity completed the task: either "same" (all
		// its retries share one result) or one distinct request.
		sameWon := audit != ""
		if sameWon && winners != 0 || !sameWon && winners != 1 {
			t.Fatalf("completion executed more than once: same=%v distinct winners=%d", sameWon, winners)
		}
		if pending := pendingGC(t, db); len(pending) != 1 {
			t.Fatalf("GC requests: %+v", pending)
		}
	})
}

func TestConcurrentIdenticalCollectFreezesOneReceipt(t *testing.T) {
	const n = 8
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		seedCollection(t, db)
		s, _ := New(db, testPolicy())
		harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
		intent := domain.CollectIntent{RequestID: "c", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual}
		ids := make([]string, n)
		errs := make([]error, n)
		var wg sync.WaitGroup
		for i := range n {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = db.Update(ctx, "s", func(tx store.Tx) error {
					out, err := s.Collect(tx, harness, intent, tx.NextSeq())
					if err == nil {
						ids[i] = out.Result.Collect.ID
					}
					return err
				})
			}()
		}
		wg.Wait()
		for i := range n {
			if errs[i] != nil || ids[i] != ids[0] {
				t.Fatalf("collector %d: %s %v, want %s", i, ids[i], errs[i], ids[0])
			}
		}
	})
}
