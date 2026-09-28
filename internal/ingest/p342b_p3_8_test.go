package ingest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// P3-8 (ADR 8 :1087/:1088): Resolve and Unpin are narrow. A request distinct
// from the one that changed the target's state is a wrong-state request
// (MISMATCH, not executed), while a retry of the same request replays; and a
// command sequence is all-or-nothing — one unauthorized command rolls back
// the whole event, including commands that had already executed.

// TestP3_8_DistinctResolveAfterResolvedIsMismatch closes the MISSING half of
// "repeated request versus distinct wrong-state request" (ADR 8 :1087).
// TestLifecycle_ExecutesInOrder_P335 covers the retry half and a wrong-KIND
// MISMATCH; the missing probe is a DISTINCT event resolving an already
// RESOLVED goal: it must be MISMATCH/NOT_EXECUTED with a diagnostic and
// change nothing, while retrying the original event replays identically.
func TestP3_8_DistinctResolveAfterResolvedIsMismatch(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := userEvent("u1", "## Goal [ship]\nShip it.\n## Resolve [ship]\n", true)
		r := f.mustIngest(user, e)
		if len(r.Lifecycle) != 1 || r.Lifecycle[0].Resolution != domain.TargetResolved || r.Lifecycle[0].Status != domain.CommandExecuted {
			t.Fatalf("setup resolve = %+v", r.Lifecycle)
		}
		goal := mustDirective(t, r, "ship")
		var resolvedVersion uint64
		f.view(func(tx store.ReadTx) error {
			g, err := tx.Item(goal.ID)
			if err != nil || g.GoalStatus == nil || *g.GoalStatus != domain.GoalResolved {
				t.Fatalf("setup: goal %+v (%v)", g, err)
			}
			resolvedVersion = g.Version
			return nil
		})

		// Repeated request: the same event replays the identical receipt.
		if again := f.mustIngest(user, e); !reflect.DeepEqual(normReceipt(again), normReceipt(r)) {
			t.Errorf("retry of the resolving event differs")
		}

		// Distinct wrong-state request: a NEW event resolving the now-RESOLVED
		// goal is MISMATCH, not executed, diagnosed, and changes nothing.
		distinct := userEvent("u2", "## Resolve [ship]\n", true)
		r2 := f.mustIngest(user, distinct)
		if len(r2.Lifecycle) != 1 {
			t.Fatalf("distinct resolve commands = %+v", r2.Lifecycle)
		}
		c := r2.Lifecycle[0]
		if c.Resolution != domain.TargetMismatch || c.Status != domain.CommandNotExecuted || c.ResolvedItemID != goal.ID {
			t.Errorf("distinct resolve = %+v, want MISMATCH NOT_EXECUTED naming the resolved goal", c)
		}
		if !hasDiag(r2, domain.DiagnosticNotFound, domain.ReasonTargetMismatch) {
			t.Errorf("distinct resolve diagnostics = %+v", r2.Diagnostics)
		}
		f.view(func(tx store.ReadTx) error {
			g, err := tx.Item(goal.ID)
			if err != nil || g.GoalStatus == nil || *g.GoalStatus != domain.GoalResolved || g.Version != resolvedVersion {
				t.Errorf("distinct resolve changed the goal: %+v (%v)", g, err)
			}
			return nil
		})
	})
}

// TestP3_8_UnauthorizedLaterCommandRollsBackEarlierOnes closes the MISSING
// half of "all-or-nothing command sequence" (ADR 8 :1088).
// TestLifecycle_SourceActor_R7 aborts a single unauthorized command; the
// missing probe is a multi-command event whose FIRST command is authorized
// and executes, and whose LATER command is not: the whole event aborts, the
// earlier command's effect is rolled back, and nothing is written. The
// split-event control shows each command alone would have executed.
func TestP3_8_UnauthorizedLaterCommandRollsBackEarlierOnes(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, sysEvent("s0", "## Goal [sysb]\nSystem goal.\n"))
		f.mustIngest(sys, userEvent("u0", "## Goal [usera]\nUser goal.\n", true))
		before := f.lastSeq()

		// Resolve [usera] is authorized for the USER source actor, Resolve
		// [sysb] is not; the later refusal must void the earlier execution.
		if _, err := f.ingest(sys, userEvent("u1", "## Resolve [usera]\n## Resolve [sysb]\n", true)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("mixed-authority event: err = %v, want ErrInvalidAuthorityPromotion", err)
		}
		if f.lastSeq() != before {
			t.Errorf("aborted sequence wrote state: seq %d -> %d", before, f.lastSeq())
		}
		f.view(func(tx store.ReadTx) error {
			for _, id := range []string{"usera", "sysb"} {
				it, ok := currentDirective(t, tx, id)
				if !ok {
					t.Fatalf("no current directive %s after abort", id)
				}
				if it.GoalStatus == nil || *it.GoalStatus != domain.GoalOpen {
					t.Errorf("%s = %v after abort; the earlier command was not rolled back", id, it.GoalStatus)
				}
			}
			evs, err := tx.LifecycleEvents(store.LifecycleFilter{MinSeq: before + 1})
			if err != nil {
				return err
			}
			if len(evs) != 0 {
				t.Errorf("aborted sequence left %d lifecycle events after seq %d", len(evs), before)
			}
			return nil
		})

		// Control: each command alone, from an authorized actor, executes.
		r := f.mustIngest(sys, userEvent("u2", "## Resolve [usera]\n", true))
		if len(r.Lifecycle) != 1 || r.Lifecycle[0].Resolution != domain.TargetResolved || r.Lifecycle[0].Status != domain.CommandExecuted {
			t.Fatalf("user Resolve alone = %+v", r.Lifecycle)
		}
		r2 := f.mustIngest(sys, sysEvent("s1", "## Resolve [sysb]\n"))
		if len(r2.Lifecycle) != 1 || r2.Lifecycle[0].Resolution != domain.TargetResolved || r2.Lifecycle[0].Status != domain.CommandExecuted {
			t.Fatalf("system Resolve alone = %+v", r2.Lifecycle)
		}
	})
}

// currentDirective finds the current version of directive id in task T.
func currentDirective(t *testing.T, tx store.ReadTx, id string) (domain.ContextItem, bool) {
	t.Helper()
	items, err := tx.Items(store.ItemFilter{TaskID: "T"})
	if err != nil {
		t.Fatalf("Items: %v", err)
	}
	for _, it := range items {
		if it.DirectiveID == id {
			return it, true
		}
	}
	return domain.ContextItem{}, false
}
