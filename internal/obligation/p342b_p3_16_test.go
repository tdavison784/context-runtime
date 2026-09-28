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
