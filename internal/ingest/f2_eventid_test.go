package ingest

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestEventIDSquatIsBareConflict_F2 verifies the F2 mitigation for SEC-1.5.
// EventIDs stay session-scoped (FR-ING-006), so another principal in the
// session who used an EventID first blocks it. The victim must learn nothing
// beyond that: the error is the bare ErrEventIDConflict sentinel (no wrapped
// cause, no text naming the other principal, its task, or its content), the
// receipt is empty, and nothing is written. The accepted residual risk is
// recorded in ADR 19: harness EventIDs must be unguessable and
// principal-prefixed.
func TestEventIDSquatIsBareConflict_F2(t *testing.T) {
	attacker := domain.Principal{SessionID: sess, WorkflowID: "wf2", TaskID: "T2", AgentID: "Z", Authority: domain.AuthorityUser}
	squat := domain.Event{EventID: "turn-2", Kind: domain.EventUser, Spans: []domain.Span{{
		Authority: domain.AuthorityUser, Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "T2"},
		Parts: []domain.InputPart{{Type: domain.PartText, MediaType: "text/plain", Text: "attacker-secret"}},
	}}}
	victim := principal(domain.AuthorityUser)
	leaks := []string{"turn-2", "wf2", "T2", "Z", "attacker-secret", "itm_", "evc_"}

	check := func(t *testing.T, how string, r domain.IngestReceipt, err error) {
		t.Helper()
		if err != domain.ErrEventIDConflict || errors.Unwrap(err) != nil {
			t.Fatalf("%s: err = %#v, want the bare ErrEventIDConflict sentinel", how, err)
		}
		if err.Error() != domain.ErrEventIDConflict.Error() {
			t.Errorf("%s: error text %q carries details", how, err.Error())
		}
		for _, leak := range leaks {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: error text leaks %q", how, leak)
			}
		}
		if !reflect.DeepEqual(r, domain.IngestReceipt{}) {
			t.Errorf("%s: conflict returned a receipt: %+v", how, r)
		}
	}

	eachStore(t, func(t *testing.T, f *fixture) {
		f.mustIngest(attacker, squat)
		before := f.snapshot()

		r, err := f.ingest(victim, userEvent("turn-2", "victim text", false))
		check(t, "Ingest", r, err)

		err = f.s.Update(ctx, sess, func(tx store.Tx) error {
			r, err := f.in.Apply(tx, victim, userEvent("turn-2", "victim text", false), "")
			check(t, "Apply", r, err)
			return err
		})
		if err != domain.ErrEventIDConflict {
			t.Errorf("Update returned %v, want the bare sentinel", err)
		}

		if after := f.snapshot(); !reflect.DeepEqual(before, after) {
			t.Error("a conflicting EventID wrote state")
		}
	})
}
