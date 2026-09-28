package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_8_UnpinWithBlockedObligation closes the BLOCKED half of ADR 8
// :1243 (P3-8 "unpinning ... leaving every attached obligation unchanged"):
// TestP3_33_UnpinWithUnresolvedObligation covers an UNRESOLVED obligation
// only. A BLOCKED obligation is equally attached: unpinning its source
// executes (PINNED→DURABLE) without touching the obligation — still current
// and BLOCKED, still blocking task completion, and still protecting the
// unpinned source from collection.
func TestP3_8_UnpinWithBlockedObligation(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		f := newFacets("dir")
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			if err := tx.InsertItem(storetest.NewDirective("s", "dir", "d", tx.NextSeq(), "pinned instruction")); err != nil {
				return err
			}
			if err := tx.InsertObligationVersion(storetest.NewObligation("s", "o", 1, tx.NextSeq(), "dir")); err != nil {
				return err
			}
			if err := storetest.UncheckedSetCurrentVersion(tx, "dir"); err != nil {
				return err
			}
			_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		// A stored obligation starts UNRESOLVED; BLOCKED is reached by the
		// authorized semantic transition, exactly as production does.
		system := storetest.NewPrincipal("s", domain.AuthoritySystem)
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			o, err := tx.Obligation("o")
			if err != nil {
				return err
			}
			seq := tx.NextSeq()
			tr := domain.ObligationTransition{ID: "p38c-block", SessionID: "s", ObligationID: "o", Version: 1, Seq: seq,
				From: domain.ObligationUnresolved, To: domain.ObligationBlocked, Action: domain.ActionBlockObligation,
				Actor: system, Cause: domain.CauseBlock, RequestID: "p38c-block-req", ReasonCode: domain.ReasonAuthorizedTransition}
			d := domain.TransitionDetail{SemanticMeta: storetest.Meta("s", "p38c-detail", seq), Target: domain.ObligationRef{SessionID: "s", ObligationID: "o", Version: 1},
				TransitionID: "p38c-block", Cause: domain.CauseBlock, RuleVersion: "rule/1"}
			_, err = sem.AppendSemanticObligationTransition(tr, d, o.Revision)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		s, _ := New(db, testPolicy())
		user := storetest.NewPrincipal("s", domain.AuthorityUser)

		// Baseline: the BLOCKED obligation blocks completion while pinned.
		if _, err := s.CompleteTaskStandalone(ctx, user, domain.CompleteTaskIntent{RequestID: "r1", TaskID: "task"}); !errors.Is(err, domain.ErrUnfinishedObligations) {
			t.Fatalf("baseline completion: %v", err)
		}

		// Unpin executes.
		out, err := s.UnpinStandalone(ctx, user, domain.UnpinIntent{RequestID: "u", ItemID: "dir", ExpectedVersion: 1})
		if err != nil || out.After.Generation != domain.GenerationDurable {
			t.Fatalf("unpin: %+v %v", out, err)
		}

		// The obligation itself is untouched: still current, BLOCKED, at its
		// post-transition revision.
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			o, err := tx.Obligation("o")
			if err != nil || !o.Current || o.Status != domain.ObligationBlocked || o.SourceItemID != "dir" || o.Revision != 2 {
				t.Fatalf("unpin changed the BLOCKED obligation: %+v %v", o, err)
			}
			it, err := tx.Item("dir")
			if err != nil || it.Residency != domain.ResidencyResident {
				t.Fatalf("source: %+v %v", it, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// Attachment survives the unpin: completion is still blocked.
		if _, err := s.CompleteTaskStandalone(ctx, user, domain.CompleteTaskIntent{RequestID: "r2", TaskID: "task"}); !errors.Is(err, domain.ErrUnfinishedObligations) {
			t.Fatalf("completion after unpin: %v", err)
		}

		// And collection still protects the unpinned source as an open
		// (BLOCKED) obligation source.
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
				t.Fatalf("unpinned BLOCKED obligation source collected: %+v", d)
			}
			protected = true
		}
		if !protected {
			t.Fatal("no explicit PROTECTED decision for the unpinned BLOCKED obligation source")
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
