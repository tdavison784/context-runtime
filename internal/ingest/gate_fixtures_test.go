package ingest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Shared setup for the Phase 3 gate traces (docs/sdd-event-traces.md). Each
// builder is the trace's literal first step at span level; the gate suites
// add the Phase 3 operations on top.

// t02P1 and t02P2 are T02's pin replacement, with the OPEN goal and the
// obligation repeats in the same events.
func t02P1() domain.Event {
	return userEvent("t02-p1", "## Pinned\n- [dep] {obligation=tests_pass} Use dependency v2.\n## Goal [g]\nShip v2.\n", true)
}

func t02P2() domain.Event {
	return userEvent("t02-p2", "## Pinned\n- [dep] {obligation=tests_pass} Use dependency v3.\n## Goal [g]\nShip v3.\n", true)
}

// t05Goal is T05's OPEN, RESIDENT goal G.
func t05Goal() domain.Event {
	return sysEvent("t05-g", "## Goal [G]\nExplain earlier work.\n")
}

// t06Setup is T06 step 1: SYSTEM creates task-owned goal G and a pin whose
// obligation O starts UNRESOLVED.
func t06Setup() domain.Event {
	return sysEvent("t06-s1", "## Goal [G]\nShip the release.\n## Pinned\n- [O] {obligation=tests_pass} All tests must pass.\n")
}

// t17Setup is T17 step 1: USER pins P1 and opens goal G1; P1 carries the
// tests_pass obligation O1.
func t17Setup() domain.Event {
	return userEvent("t17-u1", "## Pinned\n- [P1] {obligation=tests_pass} All tests must pass.\n## Goal [G1]\nImplement the API.\n", true)
}

// requireAtomic runs fn, which must fail with want, and asserts that the
// whole observable state of the session is unchanged: sequence, items,
// relationships, obligations and their history, grants, task, lifecycle
// audit, and command records (INV-09, trace preamble: a rejected event
// must not partially mutate task state).
func (f *fixture) requireAtomic(want error, fn func() error) {
	f.t.Helper()
	before := snapshotPhase2(f.t, f.s)
	err := fn()
	if !errors.Is(err, want) {
		f.t.Fatalf("err = %v, want %v", err, want)
	}
	if after := snapshotPhase2(f.t, f.s); !reflect.DeepEqual(normGolden(after), normGolden(before)) {
		f.t.Fatalf("a rejected operation changed session state")
	}
}

// TestRequireAtomic_T06Denial exercises the helper on T06's Phase 2 half:
// a USER Resolve of a SYSTEM goal aborts the whole event, including the
// turn it would have opened.
func TestRequireAtomic_T06Denial(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.mustIngest(principal(domain.AuthoritySystem), t06Setup())
		user := principal(domain.AuthorityUser)
		f.requireAtomic(domain.ErrInvalidAuthorityPromotion, func() error {
			_, err := f.ingest(user, userEvent("t06-u1", "## Remember\n- noted\n## Resolve [G]\n", true))
			return err
		})
	})
}
