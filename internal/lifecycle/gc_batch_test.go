package lifecycle

import (
	"context"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// seedEphemeral commits task "task" at turn 2 and n ended-turn ephemeral
// (collectible) items, plus obligations on "heavy" beyond MaxTargets.
func seedEphemeral(t *testing.T, db store.Store, n, heavyObligations int) {
	t.Helper()
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		task := storetest.NewTask("s", "task")
		task.Turn, task.TurnID = 2, "turn-2"
		if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
			return err
		}
		for i := range n {
			it := storetest.NewItem("s", fmt.Sprintf("eph-%03d", i), tx.NextSeq(), "scratch")
			it.Generation = domain.GenerationEphemeral
			if err := tx.InsertItem(it); err != nil {
				return err
			}
		}
		if heavyObligations > 0 {
			it := storetest.NewItem("s", "heavy", tx.NextSeq(), "heavy")
			it.Generation = domain.GenerationEphemeral
			if err := tx.InsertItem(it); err != nil {
				return err
			}
			for i := range heavyObligations {
				if err := tx.InsertObligationVersion(storetest.NewObligation("s", fmt.Sprintf("o%d", i), 1, tx.NextSeq(), "heavy")); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func planOnce(t *testing.T, db store.Store, s *Service, after domain.GCCursor) batchPlan {
	t.Helper()
	var plan batchPlan
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		collector := storetest.NewPrincipal("s", domain.AuthoritySystem)
		plan, err = s.planBatch(tx, sem, collector, collector,
			domain.CollectIntent{RequestID: "c", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual}, tx.NextSeq(), domain.GCProgress{Cursor: after})
		if err != nil {
			return err
		}
		i := domain.CollectIntent{RequestID: "c", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual}
		args, err := domain.CanonicalSemanticArguments(i, s.policy.MaxMetadataBytes)
		if err != nil {
			return err
		}
		plan, err = s.fitCollectionPlan(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), i, args, "", plan)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return plan
}

// H3: a collection larger than one transaction's work bound is planned in
// bounded batches over a durable cursor, never failed as a whole; batches
// neither overlap nor skip candidates.
func TestCollectionPlansInBoundedBatches(t *testing.T) {
	const items = 40
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxTransactionWork = 120
		s, _ := New(db, pol)
		seedEphemeral(t, db, items, 0)
		seen := map[string]bool{}
		var after domain.GCCursor
		for batch := 1; ; batch++ {
			plan := planOnce(t, db, s, after)
			if len(plan.receipt.Decisions) == 0 || batch > items {
				t.Fatalf("batch %d made no progress: %+v", batch, plan)
			}
			for _, d := range plan.receipt.Decisions {
				if seen[d.Target.ItemID] || d.Code != domain.GCArchive {
					t.Fatalf("batch %d: %s repeated or not archivable (%s)", batch, d.Target.ItemID, d.Code)
				}
				seen[d.Target.ItemID] = true
			}
			if len(plan.effects) != len(plan.receipt.Decisions) {
				t.Fatalf("batch %d: %d effects for %d decisions", batch, len(plan.effects), len(plan.receipt.Decisions))
			}
			if !plan.more {
				if batch == 1 {
					t.Fatal("the budget was large enough for one batch; the test needs several")
				}
				break
			}
			after = plan.next
		}
		if len(seen) != items {
			t.Fatalf("batches covered %d of %d candidates", len(seen), items)
		}
	})
}

// H3: a batch whose frozen receipt would exceed MaxReceiptBytes is cut to
// a prefix that fits, continuing from its last candidate.
func TestBatchFitsTheReceiptLimit(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxReceiptBytes = 4096
		s, _ := New(db, pol)
		seedEphemeral(t, db, 60, 0)
		plan := planOnce(t, db, s, domain.GCCursor{})
		if !plan.more || len(plan.receipt.Decisions) == 0 || len(plan.receipt.Decisions) >= 60 {
			t.Fatalf("receipt-bounded batch: more=%v decisions=%d", plan.more, len(plan.receipt.Decisions))
		}
		last := plan.receipt.Decisions[len(plan.receipt.Decisions)-1].Target.ItemID
		if plan.next.ID != last {
			t.Fatalf("cursor %+v does not follow the last kept candidate %s", plan.next, last)
		}
	})
}
