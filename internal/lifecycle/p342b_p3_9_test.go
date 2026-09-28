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

var p39ctx = context.Background()

// TestP3_9_GrantExpiringBetweenGoalsFailsWholeBothStores closes the MISSING
// SQLite half of "grant expires between goals" (ADR 8 :1095).
// TestCompletionGrantExpiringBetweenGoalsFailsWhole proves the
// fails-whole rollback on memory only; a rollback property must hold on the
// real store. The same scenario runs on both backends: a USER completes two
// SYSTEM goals through grants, the second grant lapsing one sequence before
// its goal's own mutation. The control (no expiry) completes both goals; the
// expiring grant aborts the whole completion with
// ErrInvalidAuthorityPromotion and commits nothing — lastSeq unchanged, the
// first goal still OPEN, the task still ACTIVE.
func TestP3_9_GrantExpiringBetweenGoalsFailsWholeBothStores(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprintf("expire=%v", expire), func(t *testing.T) {
			eachStore(t, func(t *testing.T, mem store.Store) {
				s, _ := New(mem, testPolicy())
				if err := mem.Update(p39ctx, "s", func(tx store.Tx) error {
					for _, id := range []string{"g1", "g2"} {
						g := storetest.NewGoal("s", id, tx.NextSeq(), id)
						g.Scope, g.Access, g.Authority = domain.ScopeTask, storetest.DirectiveBoundary("s"), domain.AuthoritySystem
						if err := tx.InsertItem(g); err != nil {
							return err
						}
					}
					_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
					return err
				}); err != nil {
					t.Fatal(err)
				}
				user := storetest.NewPrincipal("s", domain.AuthorityUser)
				var last uint64
				if err := mem.Update(p39ctx, "s", func(tx store.Tx) error {
					issuer := storetest.NewPrincipal("s", domain.AuthoritySystem)
					seq := tx.NextSeq()
					for n, id := range []string{"g1", "g2"} {
						// g1 is authorized at the completion's first seq; g2's
						// grant is valid through exactly that seq, so it lapses
						// before g2's own.
						g := domain.MutationGrant{ID: fmt.Sprintf("grant-%d", n), SessionID: "s", Action: domain.ActionResolve, Targets: []domain.GrantTarget{domain.ItemGrantTarget("s", id)},
							Issuer: issuer, Grantee: &user, IssuedSeq: seq}
						if n == 1 && expire {
							g.ExpiresAtSeq = seq + 1
						}
						if err := tx.InsertGrant(g); err != nil {
							return err
						}
					}
					last = seq
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				f := newFacets("g1", "g2")
				out, err := completeTask(f, mem, s, user, domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, true)
				if !expire {
					if err != nil || len(out.Result.Completion.ResolvedGoals) != 2 {
						t.Fatalf("control completion: %+v %v", out, err)
					}
					return
				}
				if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
					t.Fatalf("expired mid-completion grant: %v", err)
				}
				if err := mem.View(p39ctx, "s", func(tx store.ReadTx) error {
					g1, err := tx.Item("g1")
					if err != nil {
						return err
					}
					task, err := tx.Task("task")
					if err != nil {
						return err
					}
					if tx.LastSeq() != last || g1.GoalStatus == nil || *g1.GoalStatus != domain.GoalOpen || task.Status != domain.TaskActive {
						t.Fatal("partial completion committed")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
