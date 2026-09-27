package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// flakyBatches fails the next batch transaction (one that executes a GC
// request) with a transient storage error when armed; the failure's own
// settling transaction proceeds.
type flakyBatches struct {
	store.Store
	armed *bool
}

var errFlaky = errors.New("disk I/O error (transient)")

func (f flakyBatches) Update(ctx context.Context, session string, fn func(store.Tx) error) error {
	return f.Store.Update(ctx, session, func(tx store.Tx) error {
		if err := fn(tx); err != nil {
			return err
		}
		if *f.armed && tx.LastSeq() > 0 && batchWrote(tx) {
			*f.armed = false
			return errFlaky
		}
		return nil
	})
}

// batchWrote reports whether this transaction allocated sequences, which a
// batch does and an attempt-only settling transaction does not.
func batchWrote(tx store.Tx) bool { return tx.Allocated(tx.LastSeq()) }

// DUR-3.4 (J4): the attempt counter counts consecutive failures without
// progress, so a large collection that keeps progressing between transient
// failures finishes COLLECTED.
func TestAttemptsResetWhenABatchProgresses(t *testing.T) {
	const items = 60
	ctx := context.Background()
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	pick := func(domain.GCRequest) (domain.Principal, bool) { return harness, true }
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxTransactionWork = 200
		base, _ := New(db, pol)
		id := completeLarge(t, db, base, items)
		armed := false
		s, _ := New(flakyBatches{Store: db, armed: &armed}, pol)
		for pass := 0; pass < 4*items; pass++ {
			if res, found := gcResult(t, db, id); found {
				if res.Outcome != domain.GCCollected {
					t.Fatalf("collection ended %s/%s after progressing", res.Outcome, res.Reason)
				}
				return
			}
			armed = pass%2 == 0 // every other pass fails transiently
			n, _ := s.CollectPending(ctx, "s", pick, 1)
			if _, finished := gcResult(t, db, id); finished {
				continue // the final batch records the result, not progress
			}
			if pass%2 == 1 && n == 1 { // a non-final batch ran and committed
				var progress domain.GCProgress
				readSemantic(t, db, func(sem store.SemanticReader) error {
					var err error
					progress, err = sem.GCProgress(id)
					if errors.Is(err, domain.ErrNotFound) {
						return nil
					}
					return err
				})
				if progress.Attempts != 0 {
					t.Fatalf("pass %d: attempts %d survived a progressing batch", pass, progress.Attempts)
				}
			}
		}
		t.Fatal("collection never finished")
	})
}
