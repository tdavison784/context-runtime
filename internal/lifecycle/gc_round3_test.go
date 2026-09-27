package lifecycle

import (
	"context"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func enqueueScratch(t *testing.T, db store.Store, s *Service) string {
	t.Helper()
	var id string
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		var err error
		id, err = s.EnqueueGC(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), domain.GCSupersession, domain.CollectTask, "task", "scratch")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func runGC(t *testing.T, db store.Store, s *Service, id string, limit int) {
	t.Helper()
	for n := 0; n < limit; n++ {
		if r, ok := gcResult(t, db, id); ok {
			if r.Outcome != domain.GCCollected {
				t.Fatalf("terminal result: %+v", r)
			}
			return
		}
		if _, err := s.CollectPending(context.Background(), "s", func(domain.GCRequest) (domain.Principal, bool) {
			return storetest.NewPrincipal("s", domain.AuthoritySystem), true
		}, 1); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("collection did not finish")
}

// J1 / XREV-3.1 / SEC-3.2: exact budget boundaries never discard a candidate.
func TestJ1BudgetBoundaryCollectsEveryCandidate(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxTransactionWork = 16
		s, _ := New(db, pol)
		seedEphemeral(t, db, 10, 0)
		id := enqueueScratch(t, db, s)
		runGC(t, db, s, id, 20)
		if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
			for n := range 10 {
				it, err := tx.Item(fmt.Sprintf("eph-%03d", n))
				if err != nil {
					return err
				}
				if it.Residency != domain.ResidencyArchived {
					t.Errorf("%s skipped", it.ID)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// J2 / SPEC-3.6: later insertions cannot extend the first batch's snapshot.
func TestJ2SnapshotAndCursorStayFrozen(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxGCDecisions = 1
		s, _ := New(db, pol)
		seedEphemeral(t, db, 3, 0)
		id := enqueueScratch(t, db, s)
		var first MutationOutcome
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			var err error
			first, err = s.ExecuteGCRequest(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), id, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			it := storetest.NewItem("s", "late", tx.NextSeq(), "late")
			it.Generation = domain.GenerationEphemeral
			return tx.InsertItem(it)
		}); err != nil {
			t.Fatal(err)
		}
		runGC(t, db, s, id, 8)
		readSemantic(t, db, func(sem store.SemanticReader) error {
			r, err := sem.GCResult(id)
			if err != nil {
				return err
			}
			last, err := sem.CollectReceipt(r.CollectReceiptID)
			if last.SnapshotSeq != first.Result.Collect.SnapshotSeq {
				t.Errorf("snapshot moved: %d -> %d", first.Result.Collect.SnapshotSeq, last.SnapshotSeq)
			}
			return err
		})
		if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
			it, err := tx.Item("late")
			if it.Residency != domain.ResidencyResident {
				t.Error("later insertion collected by old snapshot")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// J3 / XREV-3.2: limits apply to the complete persisted receipt; large
// requests shrink their item-count batch and eventually finish.
func TestJ3CompleteReceiptFitsAndBatchAdapts(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxReceiptBytes = 4096
		s, _ := New(db, pol)
		id := completeLarge(t, db, s, 60)
		runGC(t, db, s, id, 100)
		readSemantic(t, db, func(sem store.SemanticReader) error {
			r, err := sem.GCResult(id)
			if err != nil {
				return err
			}
			c, err := sem.CollectReceipt(r.CollectReceiptID)
			if err != nil {
				return err
			}
			m, err := sem.MutationReceipt(domain.MutationCollection, c.RequestID)
			if err != nil {
				return err
			}
			_, err = domain.CanonicalSemanticArguments(m, pol.MaxReceiptBytes)
			return err
		})
	})
}

func TestJ3WorkExhaustionHalvesPersistedItemCount(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxGCDecisions, pol.MaxTransactionWork = 8, 16
		s, _ := New(db, pol)
		seedEphemeral(t, db, 10, 0)
		id := enqueueScratch(t, db, s)
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			_, err := s.ExecuteGCRequest(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), id, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		readSemantic(t, db, func(sem store.SemanticReader) error {
			p, err := sem.GCProgress(id)
			if p.BatchSize != 4 || p.Cursor.ID != "eph-000" || p.Batches != 1 {
				t.Errorf("progress: %+v", p)
			}
			return err
		})
		runGC(t, db, s, id, 20)
	})
}
