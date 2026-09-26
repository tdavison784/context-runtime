package ingest

import (
	"errors"
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
		if got := f.currentWorking(); !slices.Equal(got, []string{"Investigating internal/client.go."}) || len(r.Replacements) != 2 || len(semanticDups(r)) != 0 {
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
		if len(semanticDups(r)) != 2 || len(r.Replacements) != 0 || !slices.Equal(f.currentWorking(), []string{"a", "b"}) {
			t.Errorf("identical snapshot: dups %v repls %v current %v", semanticDups(r), r.Replacements, f.currentWorking())
		}

		// A refused trusted section's text survives as residual instruction
		// text (R20.3), never as Working members.
		r = f.mustIngest(sys, sysEvent("w3", "## Working\n- c\n- [bad id!] d\n"))
		if workingItems(r) != 0 || len(residuals(r)) != 1 || !slices.Equal(f.currentWorking(), []string{"a", "b"}) {
			t.Errorf("malformed snapshot replaced state: %v", f.currentWorking())
		}

		f.mustIngest(sys, sysEvent("p1", "## Pinned\n- [status] pinned status\n"))
		r = f.mustIngest(sys, sysEvent("w4", "## Working\n- [status] {scope=TURN} c\n- e\n"))
		if !hasDiag(r, domain.ErrMalformedDirective, domain.ReasonBoundaryConflict) || workingItems(r) != 0 || len(residuals(r)) != 1 || !slices.Equal(f.currentWorking(), []string{"a", "b"}) {
			t.Errorf("boundary-conflict snapshot: items %v current %v diags %+v", kinds(semantic(r)), f.currentWorking(), r.Diagnostics)
		}
	})
}

// TestLifecycle_ExecutesInOrder_P335 is D1's scenario under Phase 3: the
// commands of a new event execute at their position in event order as the
// span's source actor (Resolve on the goal just declared), a target of the
// wrong kind is MISMATCH and an unknown one NOT_FOUND, each with its
// diagnostic; a retry returns the identical receipt. The frozen D1
// guarantee for recorded v1 commands (PARSED_NOT_EXECUTED forever) is
// TestPhase2FixtureReplay's.
func TestLifecycle_ExecutesInOrder_P335(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := userEvent("u1", "## Goal [ship]\nShip it.\n## Resolve [ship]\n## Unpin [ship]\n## Resolve [nope]\n", true)
		r := f.mustIngest(user, e)
		if len(r.Lifecycle) != 3 {
			t.Fatalf("commands = %+v", r.Lifecycle)
		}
		goal, _ := byDirective(r, "ship")
		want := []struct {
			res    domain.TargetResolution
			status domain.CommandStatus
		}{{domain.TargetResolved, domain.CommandExecuted}, {domain.TargetMismatch, domain.CommandNotExecuted}, {domain.TargetNotFound, domain.CommandNotExecuted}}
		for i, c := range r.Lifecycle {
			if c.Resolution != want[i].res || c.Status != want[i].status || c.Actor.Authority != domain.AuthorityUser {
				t.Errorf("command %d = %+v", i, c)
			}
		}
		if r.Lifecycle[0].ResolvedItemID != goal.ID {
			t.Errorf("Resolve target = %q, want %q", r.Lifecycle[0].ResolvedItemID, goal.ID)
		}
		if !hasDiag(r, domain.DiagnosticNotFound, domain.ReasonTargetMismatch) || !hasDiag(r, domain.DiagnosticNotFound, domain.ReasonUnknownTarget) {
			t.Errorf("diagnostics = %+v", r.Diagnostics)
		}
		f.view(func(tx store.ReadTx) error {
			g, err := tx.Item(goal.ID)
			if err != nil || *g.GoalStatus != domain.GoalResolved || g.Version != 2 {
				t.Errorf("goal not resolved in order: %+v %v", g, err)
			}
			return nil
		})
		if again := f.mustIngest(user, e); !reflect.DeepEqual(normReceipt(again), normReceipt(r)) {
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

func workingItems(r domain.IngestReceipt) int {
	n := 0
	for _, it := range r.Items {
		if it.Section == domain.SectionWorking {
			n++
		}
	}
	return n
}

// TestWorking_DerivedIDAcrossAuthorities_DUR15 is DUR-1.5 (ruling option
// A): identical Working text under two authorities shares a derived ID. A
// same-or-higher authority supersedes by ID; a lower authority's section is
// refused R13-style with a diagnostic while the rest of its event applies;
// explicit IDs keep by-ID replacement, so a lower-authority explicit
// restatement still aborts.
func TestWorking_DerivedIDAcrossAuthorities_DUR15(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, userEvent("u0", "hi", false))
		f.mustIngest(sys, sysEvent("s1", "## Working\n- a\n- b\n"))

		r := f.mustIngest(sys, userEvent("u1", "## Working\n- a\n- c\n## Remember\n- kept\n", true))
		if !hasDiag(r, domain.ErrMalformedDirective, domain.ReasonBoundaryConflict) || workingItems(r) != 0 {
			t.Errorf("lower-authority Working: items %d diags %+v", workingItems(r), r.Diagnostics)
		}
		if _, ok := byDirective(r, domain.DerivedDirectiveID("remember", domain.ContentHash([]domain.ContentPart{{Type: domain.PartText, Text: "kept"}}))); !ok {
			t.Errorf("the rest of the event was not applied")
		}
		if got := f.currentWorking(); !slices.Equal(got, []string{"a", "b"}) {
			t.Errorf("SYSTEM snapshot disturbed: %v", got)
		}

		// A higher authority supersedes the lower one's identical line.
		f.mustIngest(sys, userEvent("u2", "## Working\n- x\n- y\n", true))
		f.mustIngest(sys, sysEvent("s2", "## Working\n- x\n"))
		if got := f.currentWorking(); !slices.Equal(got, []string{"y", "x"}) {
			t.Errorf("after SYSTEM restates x: current %v, want [y x]", got)
		}

		// Explicit IDs keep by-ID replacement: a lower-authority explicit
		// restatement of a SYSTEM member aborts.
		f.mustIngest(sys, sysEvent("s3", "## Working\n- [status] green\n"))
		if _, err := f.ingest(sys, userEvent("u3", "## Working\n- [status] red\n", true)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Errorf("explicit lower-authority restatement: err = %v", err)
		}
	})
}
