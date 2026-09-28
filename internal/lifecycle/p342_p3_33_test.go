package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_33_UnpinWithUnresolvedObligation closes the P3-42 table row "Unpin
// with unresolved obligation": unpinning the source of a current unresolved
// obligation executes (PINNED→DURABLE) but does not disable the obligation's
// mandatory status — the obligation stays current and unresolved, still
// blocks task completion, and still protects the unpinned source from
// collection.
//
// TEST-6.1: the seed puts the task three turns past the directive's expired
// 2-turn TTL window (item turn "turn-1", task turn 3 "turn-3"), so after the
// unpin the item is collectible by the cheap facts alone (EXPIRED_TTL) and
// the ActiveTurn protection cannot fire. The open-obligation-source
// protection (policy.MayArchive's ARCHIVE keeps gcProtection's
// OpenObligationSource read in play; gc.go's ExpiryLive&&OpenObligationSource
// case) is therefore the ONLY protection making the decision PROTECTED —
// removing either its flag-setter or its policy case must fail this test.
func TestP3_33_UnpinWithUnresolvedObligation(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		f := newFacets("dir")
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			it := storetest.NewDirective("s", "dir", "d", tx.NextSeq(), "pinned instruction")
			two := 2
			it.TTLTurns, it.CreatedTurn = &two, 1
			if err := tx.InsertItem(it); err != nil {
				return err
			}
			if err := tx.InsertObligationVersion(storetest.NewObligation("s", "o", 1, tx.NextSeq(), "dir")); err != nil {
				return err
			}
			if err := storetest.UncheckedSetCurrentVersion(tx, "dir"); err != nil {
				return err
			}
			task := storetest.NewTask("s", "task")
			task.Turn, task.TurnID = 3, "turn-3"
			_, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		s, _ := New(db, testPolicy())
		user := storetest.NewPrincipal("s", domain.AuthorityUser)

		// Baseline: the unresolved obligation blocks completion while pinned.
		if _, err := s.CompleteTaskStandalone(ctx, user, domain.CompleteTaskIntent{RequestID: "r1", TaskID: "task"}); !errors.Is(err, domain.ErrUnfinishedObligations) {
			t.Fatalf("baseline completion: %v", err)
		}

		// Unpin executes.
		out, err := s.UnpinStandalone(ctx, user, domain.UnpinIntent{RequestID: "u", ItemID: "dir", ExpectedVersion: 1})
		if err != nil || out.After.Generation != domain.GenerationDurable {
			t.Fatalf("unpin: %+v %v", out, err)
		}

		// The obligation itself is untouched: still current, unresolved, v1.
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			o, err := tx.Obligation("o")
			if err != nil || !o.Current || o.Status != domain.ObligationUnresolved || o.SourceItemID != "dir" || o.Revision != 1 {
				t.Fatalf("unpin changed the obligation: %+v %v", o, err)
			}
			it, err := tx.Item("dir")
			if err != nil || it.Residency != domain.ResidencyResident {
				t.Fatalf("source: %+v %v", it, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// Mandatory status survives the unpin: completion is still blocked.
		if _, err := s.CompleteTaskStandalone(ctx, user, domain.CompleteTaskIntent{RequestID: "r2", TaskID: "task"}); !errors.Is(err, domain.ErrUnfinishedObligations) {
			t.Fatalf("completion after unpin: %v", err)
		}

		// And collection still protects the unpinned source as an open
		// obligation source.
		collected, err := collect(f, db, s, storetest.NewPrincipal("s", domain.AuthoritySystem), domain.CollectIntent{RequestID: "c", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual})
		if err != nil || collected.Result.Collect == nil {
			t.Fatalf("collect: %+v %v", collected, err)
		}
		protected := false
		for _, d := range collected.Result.Collect.Decisions {
			if d.Target.ItemID != "dir" {
				continue
			}
			if d.Code != domain.GCProtected {
				t.Fatalf("unpinned obligation source collected: %+v", d)
			}
			protected = true
		}
		if !protected {
			t.Fatal("no explicit PROTECTED decision for the unpinned obligation source")
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			it, err := tx.Item("dir")
			if err != nil || it.Residency != domain.ResidencyResident {
				t.Fatalf("unpinned source archived: %+v %v", it, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
