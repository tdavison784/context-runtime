package graph

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestReplaceDirective_ObligationFanOut (R9, D13, D17, SPEC-1.10): replacing
// a source bound to exactly maxBoundObligations obligation versions retires
// all of them; one more makes the replacement fail whole with
// store.ErrLimitExceeded, writing nothing and retiring none, never some.
func TestReplaceDirective_ObligationFanOut(t *testing.T) {
	for _, n := range []int{maxBoundObligations, maxBoundObligations + 1} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			eachStore(t, func(t *testing.T, s store.Store) {
				sess := fmt.Sprintf("sess-fanout-%d", n)
				actor := principal(sess, domain.AuthorityUser)
				var old domain.ContextItem
				update(t, s, sess, func(tx store.Tx) error {
					old = fileGoal(t, tx, actor, "g1", "d", "Ship it")
					for i := range n {
						if err := tx.InsertObligationVersion(storetest.NewObligation(sess, fmt.Sprintf("ob%03d", i), 1, tx.NextSeq(), old.ID)); err != nil {
							return err
						}
					}
					return nil
				})
				replaceErr := s.Update(ctx, sess, func(tx store.Tx) error {
					g2 := storetest.NewGoal(sess, "g2", tx.NextSeq(), "Ship it faster")
					g2.DirectiveID, g2.Section, g2.Scope, g2.Access = "d", domain.SectionGoal, domain.ScopeTask, storetest.DirectiveBoundary(sess)
					mustInsert(t, tx, g2)
					_, err := ReplaceDirective(tx, actor, g2.TaskID, "d", g2.ID, "evt-g2")
					return err
				})
				view(t, s, sess, func(tx store.ReadTx) error {
					current := 0
					for i := range n {
						v, err := tx.Obligation(fmt.Sprintf("ob%03d", i))
						if err != nil {
							return err
						}
						if v.Current {
							current++
						}
					}
					oldCurrent, err := IsCurrent(tx, old.ID)
					if err != nil {
						return err
					}
					switch {
					case n <= maxBoundObligations && (replaceErr != nil || current != 0 || oldCurrent):
						t.Errorf("n=%d: err %v, %d still current, old current %v; want all retired", n, replaceErr, current, oldCurrent)
					case n > maxBoundObligations && (!errors.Is(replaceErr, store.ErrLimitExceeded) || current != n || !oldCurrent):
						t.Errorf("n=%d: err %v, %d current, old current %v; want ErrLimitExceeded and nothing changed", n, replaceErr, current, oldCurrent)
					}
					return nil
				})
			})
		})
	}
}
