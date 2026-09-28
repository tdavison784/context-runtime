package obligation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// expiringMatcherGrant is matcherGrant with an explicit ExpiresAtSeq: the
// grant is valid through that sequence inclusive and dead afterwards.
func (f *evalFixture) expiringMatcherGrant(t *testing.T, id string, ref domain.ObligationRef, m domain.MatcherRef, issuer domain.Principal, expiresAt uint64) {
	t.Helper()
	mustUpdate(t, f.st, func(tx store.Tx) error {
		return tx.InsertGrant(domain.MutationGrant{
			ID: id, SessionID: testSession, Action: domain.ActionAssertObligation,
			Targets: []domain.GrantTarget{ref.Target()}, Issuer: issuer, Matcher: &m, IssuedSeq: tx.NextSeq(),
			ExpiresAtSeq: expiresAt,
		})
	})
}

func (f *evalFixture) lastSeqIs(t *testing.T) uint64 {
	t.Helper()
	var ls uint64
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		ls = tx.LastSeq()
		return nil
	})
	return ls
}

// TestP3_16_RefreshWithExpiredGrantRetainsOldProof: a refresh's positive
// step is preauthorized at its actual sequence, so an EXPIRED grant — unlike
// a revoked one, which TestProofRefresh covers — cannot replace a
// still-valid proof (P3-16 X3: "grant expiry cannot preserve an invalid
// proof"; conversely a dead grant never installs a new one). The grant's
// window covers the first PASS's sequences exactly; the second PASS lands
// past it. A live grant issued afterwards proves the same second-PASS
// mechanics would have refreshed, so the refusal is the expiry, not the
// setup.
func TestP3_16_RefreshWithExpiredGrantRetainsOldProof(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		// The first observeTests under this fixture consumes five sequences;
		// the grant must be live through all of them and dead one sequence
		// later, when the refresh attempt allocates.
		until := f.lastSeqIs(t) + 6
		f.expiringMatcherGrant(t, "g-exp", f.sysTests, TestsPassV1, f.system, until)
		f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		old := f.status(t, f.sysTests)
		if old.Status != domain.ObligationSatisfied || old.CurrentProofID == "" {
			t.Fatalf("setup: expired-before-use grant or broken window: %+v", old)
		}
		if ls := f.lastSeqIs(t); ls != until {
			t.Fatalf("setup: sequence window drifted (last %d, grant expires at %d) — retune the window", ls, until)
		}

		// The refresh attempt: a newer identical PASS under the expired grant
		// changes nothing — same proof, same revision, no PROOF_REFRESH pair.
		_, second := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		if second.ID == "" {
			t.Fatal("second PASS not recorded as evidence")
		}
		o := f.status(t, f.sysTests)
		if o.Status != domain.ObligationSatisfied || o.CurrentProofID != old.CurrentProofID || o.Revision != old.Revision {
			t.Fatalf("refresh under expired grant replaced the still-valid proof: %+v (was %+v)", o, old)
		}
		for _, tr := range f.history(t, f.sysTests) {
			if tr.Cause == domain.CauseProofRefresh {
				t.Fatalf("refresh pair written under an expired grant: %+v", tr)
			}
		}

		// Control: with a live grant the very next PASS refreshes, so the
		// refusal above is the expiry rule, not the fixture.
		f.matcherGrant(t, "g-live", f.sysTests, TestsPassV1, f.system)
		_, third := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		fresh := f.status(t, f.sysTests)
		if fresh.Status != domain.ObligationSatisfied || fresh.CurrentProofID == old.CurrentProofID || fresh.Revision != o.Revision+2 {
			t.Fatalf("control refresh under live grant did not replace the proof: %+v", fresh)
		}
		h := f.history(t, f.sysTests)
		rel, sat := h[len(h)-2], h[len(h)-1]
		if rel.Cause != domain.CauseProofRefresh || rel.PriorProofID != old.CurrentProofID ||
			sat.Cause != domain.CauseProofRefresh || sat.ProofID != fresh.CurrentProofID || sat.RequestID != third.ID {
			t.Fatalf("control refresh pair = %+v / %+v", rel, sat)
		}
	})
}

// TestP3_16_BlockedWaitsForUnblockAndWaivedIsTerminal: BLOCKED evidence
// waits for an authorized unblock — a PASS that would satisfy an UNRESOLVED
// obligation leaves a BLOCKED one untouched, and only the unblock
// (itself authority-checked) reopens it — while WAIVED is terminal: no
// later proof, matcher run, or transition leaves the waived state (P3-16).
func TestP3_16_BlockedWaitsForUnblockAndWaivedIsTerminal(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)

		// BLOCKED: the block itself is an authorized transition.
		if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, 1, domain.ObligationBlocked)); err != nil {
			t.Fatalf("block: %v", err)
		}
		// A complete applicable PASS under a live grant arrives while
		// blocked: recorded as evidence, but it cannot satisfy.
		_, blockedPASS := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		if blockedPASS.ID == "" {
			t.Fatal("PASS while blocked not recorded")
		}
		o := f.status(t, f.sysTests)
		if o.Status != domain.ObligationBlocked || o.CurrentProofID != "" || o.Revision != 2 {
			t.Fatalf("evidence satisfied a BLOCKED obligation: %+v", o)
		}
		// A lower-authority actor cannot unblock a SYSTEM-sourced obligation.
		if _, err := f.s.transition(t, f.st, f.userP, intent(f.sysTests, 2, domain.ObligationUnresolved)); err == nil {
			t.Fatal("USER unblocked a SYSTEM obligation")
		}
		// The authorized unblock reopens the obligation, and only then does
		// a further PASS satisfy it.
		if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, 2, domain.ObligationUnresolved)); err != nil {
			t.Fatalf("unblock: %v", err)
		}
		f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		if o = f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("unblocked obligation not satisfiable by later evidence: %+v", o)
		}

		// WAIVED: terminal. Waive the satisfied version, then feed a fresh
		// PASS and a direct satisfy transition: neither resurrects it.
		if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, o.Revision, domain.ObligationWaived)); err != nil {
			t.Fatalf("waive: %v", err)
		}
		waived := f.status(t, f.sysTests)
		atWaive := len(f.history(t, f.sysTests))
		if waived.Status != domain.ObligationWaived || waived.CurrentProofID != "" {
			t.Fatalf("waived = %+v", waived)
		}
		f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, waived.Revision, domain.ObligationSatisfied)); err == nil {
			t.Fatal("direct satisfy accepted on a waived obligation")
		}
		if o = f.status(t, f.sysTests); o.Status != domain.ObligationWaived || o.Revision != waived.Revision {
			t.Fatalf("waived obligation changed after waiver: %+v (was %+v)", o, waived)
		}
		if got := len(f.history(t, f.sysTests)); got != atWaive {
			t.Fatalf("history grew after waiver: %d -> %d", atWaive, got)
		}
	})
}
