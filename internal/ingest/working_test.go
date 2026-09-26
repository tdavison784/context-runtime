package ingest

import (
	"reflect"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// currentWorking returns the texts of the current Working items.
func (f *fixture) currentWorking() []string {
	var out []string
	f.view(func(tx store.ReadTx) error {
		items, err := tx.Items(store.ItemFilter{TaskID: "T"})
		if err != nil {
			return err
		}
		for _, it := range items {
			if it.Section != domain.SectionWorking {
				continue
			}
			if ok, err := graph.IsCurrent(tx, it.ID); err != nil {
				return err
			} else if ok {
				out = append(out, it.Parts[0].Text)
			}
		}
		return nil
	})
	return out
}

// TestWorking_T18 is T18 step 2: W1 = {a, b} then W2 = {a}. W2's item
// supersedes both W1 items and is the only current task_state. The
// repeated text is a fresh version in a changed snapshot, never a
// duplicate that could leave b current.
func TestWorking_T18(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("w1", "## Working\n- Investigating internal/client.go.\n- Current issue is TestLegacyClient.\n", true))
		r := f.mustIngest(user, userEvent("w2", "## Working\n- Investigating internal/client.go.\n", true))
		if got := f.currentWorking(); !slices.Equal(got, []string{"Investigating internal/client.go."}) || len(r.Replacements) != 2 || len(r.Duplicates) != 0 {
			t.Errorf("current = %v, repls %v, dups %v", got, r.Replacements, r.Duplicates)
		}
		if sem := semantic(r); len(sem) != 1 || sem[0].Kind != domain.KindTaskState || sem[0].Authority != domain.AuthorityUser {
			t.Errorf("W2 items = %+v", sem)
		}
	})
}

// TestWorking_DuplicateAndMalformed: an identical snapshot is a duplicate
// snapshot (nothing retired); a malformed section or one with a boundary
// conflict replaces nothing and writes no member.
func TestWorking_DuplicateAndMalformed(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, sysEvent("w1", "## Working\n- a\n- b\n"))
		r := f.mustIngest(sys, sysEvent("w2", "## Working\n- a\n- b\n"))
		if len(r.Duplicates) != 2 || len(r.Replacements) != 0 || !slices.Equal(f.currentWorking(), []string{"a", "b"}) {
			t.Errorf("identical snapshot: dups %v repls %v current %v", r.Duplicates, r.Replacements, f.currentWorking())
		}

		r = f.mustIngest(sys, sysEvent("w3", "## Working\n- c\n- [bad id!] d\n"))
		if len(semantic(r)) != 0 || !slices.Equal(f.currentWorking(), []string{"a", "b"}) {
			t.Errorf("malformed snapshot replaced state: %v", f.currentWorking())
		}

		f.mustIngest(sys, sysEvent("p1", "## Pinned\n- [status] pinned status\n"))
		r = f.mustIngest(sys, sysEvent("w4", "## Working\n- [status] {scope=TURN} c\n- e\n"))
		if !hasDiag(r, domain.ErrMalformedDirective, domain.ReasonBoundaryConflict) || len(semantic(r)) != 0 || !slices.Equal(f.currentWorking(), []string{"a", "b"}) {
			t.Errorf("boundary-conflict snapshot: items %v current %v diags %+v", kinds(semantic(r)), f.currentWorking(), r.Diagnostics)
		}
	})
}

// TestLifecycle_ParsedNotExecuted_D1: Resolve/Unpin are recorded, resolved
// read-only at their position in event order, and never executed; targets
// that are unknown, ambiguous, or of the wrong kind are diagnostics; a
// retry returns the identical records.
func TestLifecycle_ParsedNotExecuted_D1(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := userEvent("u1", "## Goal [ship]\nShip it.\n## Resolve [ship]\n## Unpin [ship]\n## Resolve [nope]\n", true)
		r := f.mustIngest(user, e)
		if len(r.Lifecycle) != 3 {
			t.Fatalf("commands = %+v", r.Lifecycle)
		}
		goal, _ := byDirective(r, "ship")
		want := []domain.TargetResolution{domain.TargetResolved, domain.TargetMismatch, domain.TargetNotFound}
		for i, c := range r.Lifecycle {
			if c.Resolution != want[i] || c.Status != domain.CommandParsedNotExecuted || c.Actor.Authority != domain.AuthorityUser {
				t.Errorf("command %d = %+v", i, c)
			}
		}
		if r.Lifecycle[0].ResolvedItemID != goal.ID {
			t.Errorf("Resolve target = %q, want %q", r.Lifecycle[0].ResolvedItemID, goal.ID)
		}
		if !hasDiag(r, domain.ErrUnsupportedDirective, domain.ReasonTargetMismatch) || !hasDiag(r, domain.DiagnosticNotFound, domain.ReasonUnknownTarget) {
			t.Errorf("diagnostics = %+v", r.Diagnostics)
		}
		f.view(func(tx store.ReadTx) error {
			g, err := tx.Item(goal.ID)
			if err != nil || *g.GoalStatus != domain.GoalOpen || g.Version != 1 {
				t.Errorf("goal executed: %+v %v", g, err)
			}
			return nil
		})
		if again := f.mustIngest(user, e); !reflect.DeepEqual(again, r) {
			t.Errorf("retry differs")
		}
	})
}

// TestLifecycle_SourceActor_R7: a USER span carried by a SYSTEM caller
// cannot resolve a SYSTEM goal; unauthorized aborts the whole event.
func TestLifecycle_SourceActor_R7(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, sysEvent("s1", "## Goal [g]\nSystem goal.\n"))
		before := f.lastSeq()
		if _, err := f.ingest(sys, userEvent("u1", "hello\n## Resolve [g]\n", true)); err != domain.ErrInvalidAuthorityPromotion {
			t.Errorf("err = %v, want ErrInvalidAuthorityPromotion", err)
		}
		if f.lastSeq() != before {
			t.Errorf("aborted event wrote state")
		}
		r := f.mustIngest(sys, sysEvent("s2", "## Resolve [g]\n"))
		if r.Lifecycle[0].Resolution != domain.TargetResolved {
			t.Errorf("SYSTEM Resolve = %+v", r.Lifecycle[0])
		}
	})
}
