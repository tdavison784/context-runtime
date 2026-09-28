package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// TestP3_16_RefusalsAreSpecificAndWriteNothing (P3-16, SPEC-6.8 hygiene):
// TestP3_16_BlockedWaitsForUnblockAndWaivedIsTerminal refuses its two
// negative probes with err == nil checks only, so any error would do. This
// companion test in a new p342c file names each refusal exactly and asserts
// the parity the clause implies — the refused call changed nothing: a USER
// unblock of a SYSTEM-sourced BLOCKED obligation is an authority refusal
// (ErrInvalidAuthorityPromotion), not a store or validation accident, and a
// direct satisfy on a WAIVED obligation is an invalid transition
// (ErrInvalidTransition), each leaving status, revision, proof and history
// untouched, on both stores.
func TestP3_16_RefusalsAreSpecificAndWriteNothing(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		f.matcherGrant(t, "g-p16c", f.sysTests, TestsPassV1, f.system)

		// BLOCKED via the authorized transition, then the USER unblock probe.
		if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, 1, domain.ObligationBlocked)); err != nil {
			t.Fatalf("block: %v", err)
		}
		blocked := f.status(t, f.sysTests)
		blockedHistory := len(f.history(t, f.sysTests))
		if blocked.Status != domain.ObligationBlocked || blocked.Revision != 2 {
			t.Fatalf("setup: blocked = %+v", blocked)
		}
		if _, err := f.s.transition(t, f.st, f.userP, intent(f.sysTests, 2, domain.ObligationUnresolved)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("USER unblock of a SYSTEM obligation: err = %v, want ErrInvalidAuthorityPromotion", err)
		}
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationBlocked || o.Revision != blocked.Revision || o.CurrentProofID != blocked.CurrentProofID {
			t.Fatalf("refused USER unblock changed the obligation: %+v (was %+v)", o, blocked)
		}
		if got := len(f.history(t, f.sysTests)); got != blockedHistory {
			t.Fatalf("refused USER unblock wrote history: %d -> %d", blockedHistory, got)
		}

		// Reach SATISFIED then WAIVED via authorized transitions, then the
		// direct-satisfy probe.
		if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, 2, domain.ObligationUnresolved)); err != nil {
			t.Fatalf("unblock: %v", err)
		}
		f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		sat := f.status(t, f.sysTests)
		if sat.Status != domain.ObligationSatisfied || sat.CurrentProofID == "" {
			t.Fatalf("setup: satisfied = %+v", sat)
		}
		if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, sat.Revision, domain.ObligationWaived)); err != nil {
			t.Fatalf("waive: %v", err)
		}
		waived := f.status(t, f.sysTests)
		waivedHistory := len(f.history(t, f.sysTests))
		if waived.Status != domain.ObligationWaived || waived.CurrentProofID != "" {
			t.Fatalf("setup: waived = %+v", waived)
		}
		if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, waived.Revision, domain.ObligationSatisfied)); !errors.Is(err, domain.ErrInvalidTransition) {
			t.Fatalf("direct satisfy on a WAIVED obligation: err = %v, want ErrInvalidTransition", err)
		}
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationWaived || o.Revision != waived.Revision || o.CurrentProofID != waived.CurrentProofID {
			t.Fatalf("refused direct satisfy changed the obligation: %+v (was %+v)", o, waived)
		}
		if got := len(f.history(t, f.sysTests)); got != waivedHistory {
			t.Fatalf("refused direct satisfy wrote history: %d -> %d", waivedHistory, got)
		}
	})
}
