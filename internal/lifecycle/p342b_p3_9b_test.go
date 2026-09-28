package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_9_UnresolvedBlockersSurviveRealUnpinAndMaterializationDisable closes
// the MISSING half of "unresolved obligation after Unpin/materialization
// disable" (ADR 8 :1091). TestCompletionRejectsAllOwnerBlockersBeforeLedger
// proves the blocker ordering through a stubbed reader with
// MaterializationDisabled set — no real store, no Unpin, and no plain
// unresolved obligation ever exercised the completion path. On both stores a
// pinned directive sources a current UNRESOLVED obligation (plain, or
// carrying the FR-OBL-003 materialization exception): completion is blocked
// while pinned, a REAL Unpin of the source executes, and completion is still
// blocked afterwards — the obligation stays current and unresolved and the
// task active. Only waiving the obligation unblocks completion, proving it
// was the obligation, not the pin, that blocked.
func TestP3_9_UnresolvedBlockersSurviveRealUnpinAndMaterializationDisable(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		disabled bool
	}{
		{"plain unresolved", false},
		{"materialization disabled", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				if err := db.Update(ctx, "s", func(tx store.Tx) error {
					if err := tx.InsertItem(storetest.NewDirective("s", "dir", "d", tx.NextSeq(), "pinned instruction")); err != nil {
						return err
					}
					o := storetest.NewObligation("s", "o", 1, tx.NextSeq(), "dir")
					o.MaterializationDisabled = c.disabled
					if err := tx.InsertObligationVersion(o); err != nil {
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
				s, _ := New(db, testPolicy())
				user := storetest.NewPrincipal("s", domain.AuthorityUser)
				system := storetest.NewPrincipal("s", domain.AuthoritySystem)

				blocked := func(req string) {
					t.Helper()
					if _, err := s.CompleteTaskStandalone(ctx, user, domain.CompleteTaskIntent{RequestID: req, TaskID: "task"}); !errors.Is(err, domain.ErrUnfinishedObligations) {
						t.Fatalf("completion %s: %v, want ErrUnfinishedObligations", req, err)
					}
				}
				checkState := func(wantStatus domain.TaskStatus) {
					t.Helper()
					if err := db.View(ctx, "s", func(tx store.ReadTx) error {
						o, err := tx.Obligation("o")
						if err != nil || !o.Current || o.Status != domain.ObligationUnresolved || o.MaterializationDisabled != c.disabled || o.Revision != 1 {
							t.Fatalf("obligation changed: %+v %v", o, err)
						}
						task, err := tx.Task("task")
						if err != nil || task.Status != wantStatus {
							t.Fatalf("task = %+v (%v), want %v", task, err, wantStatus)
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				}

				// Baseline: the unresolved obligation blocks completion while
				// the source is pinned — with or without the exception flag.
				blocked("r1")
				checkState(domain.TaskActive)

				// A REAL unpin of the source executes.
				out, err := s.UnpinStandalone(ctx, user, domain.UnpinIntent{RequestID: "u", ItemID: "dir", ExpectedVersion: 1})
				if err != nil || out.After.Generation != domain.GenerationDurable {
					t.Fatalf("unpin: %+v %v", out, err)
				}

				// The blocker survives the unpin: the work is still unfinished.
				blocked("r2")
				checkState(domain.TaskActive)

				// Control: waive the obligation — the same completion by the
				// same actor now goes through, so it was the obligation, never
				// the pin or the exception, that blocked.
				obl, err := obligation.New(w4Policy(), obligation.DefaultRegistry())
				if err != nil {
					t.Fatal(err)
				}
				if err := db.Update(ctx, "s", func(tx store.Tx) error {
					_, err := obl.ApplyTransitionTx(tx, system, domain.TransitionIntent{
						RequestID: "w", Target: domain.ObligationRef{SessionID: "s", ObligationID: "o", Version: 1},
						ExpectedRevision: 1, To: domain.ObligationWaived,
					}, tx.NextSeq())
					return err
				}); err != nil {
					t.Fatalf("waive: %v", err)
				}
				if _, err := s.CompleteTaskStandalone(ctx, user, domain.CompleteTaskIntent{RequestID: "r3", TaskID: "task"}); err != nil {
					t.Fatalf("completion after waiver: %v", err)
				}
				if err := db.View(ctx, "s", func(tx store.ReadTx) error {
					task, err := tx.Task("task")
					if err != nil || task.Status != domain.TaskCompleted {
						t.Fatalf("task after completion = %+v (%v)", task, err)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
