package obligation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestP3_17_NewGrantAloneNeverSatisfies (P3-17, ADR 8 :1305): matcher
// evaluation is never triggered by the grant itself. A trusted typed PASS
// observation reported BEFORE any grant leaves the obligation UNRESOLVED —
// evidence without matcher authority proves nothing — and issuing a live
// SYSTEM matcher grant afterwards satisfies nothing on its own: no status
// change, no proof, no matcher transition, no revision bump. Only the
// explicit trusted reevaluation under that grant satisfies, and the proof it
// installs names the pre-grant observation. (TestReevaluateAfterGrant runs
// this sequence on the default store only and never checks which observation
// the installed proof names.)
func TestP3_17_NewGrantAloneNeverSatisfies(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)

		// A trusted PASS observation before any grant stays unsatisfied. The
		// no-grant-yet state is asserted explicitly, not implied by ordering:
		// no live grant may authorize asserting this obligation.
		_, obs := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved || o.CurrentProofID != "" || o.Revision != 1 {
			t.Fatalf("observation before any grant = %+v; evidence without matcher authority proved something", o)
		}
		if n := p17cMatcherCauses(f.history(t, f.sysTests)); n != 0 {
			t.Fatalf("matcher transition without a grant: %d in %+v", n, f.history(t, f.sysTests))
		}
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			live, err := r.LiveGrantsFor(domain.ActionAssertObligation, f.sysTests.Target(), tx.LastSeq(), 10)
			if err != nil {
				return err
			}
			if len(live) != 0 {
				t.Fatalf("no grant may exist yet, found %d live for the obligation", len(live))
			}
			return nil
		})

		// A new live grant ALONE never satisfies: not at issuance, not
		// silently afterwards.
		f.matcherGrant(t, "g-p17c", f.sysTests, TestsPassV1, f.system)
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved || o.CurrentProofID != "" || o.Revision != 1 {
			t.Fatalf("grant issuance satisfied by itself: %+v", o)
		}
		if n := p17cMatcherCauses(f.history(t, f.sysTests)); n != 0 {
			t.Fatalf("grant issuance wrote a matcher transition: %d in %+v", n, f.history(t, f.sysTests))
		}

		// Control: the explicit trusted reevaluation under that grant is what
		// satisfies — by selecting the EXISTING pre-grant observation.
		if _, err := f.reevaluate(t, f.harness, f.sysTests, 1); err != nil {
			t.Fatalf("reevaluation under the live grant: %v", err)
		}
		o := f.status(t, f.sysTests)
		if o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("reevaluation under the live grant = %+v", o)
		}
		if n := p17cMatcherCauses(f.history(t, f.sysTests)); n != 1 {
			t.Fatalf("want exactly one matcher transition, got %d in %+v", n, f.history(t, f.sysTests))
		}
		var p domain.ApplicabilityProof
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			p, _ = r.ApplicabilityProof(o.CurrentProofID)
			return nil
		})
		if p.ObservationID != obs.ID || p.RuleVersion != "tests_pass/1" {
			t.Fatalf("proof = %+v; want the pre-grant observation %s under tests_pass/1", p, obs.ID)
		}
	})
}

// p17cMatcherCauses counts matcher-caused transitions among trs.
func p17cMatcherCauses(trs []domain.ObligationTransition) int {
	n := 0
	for _, tr := range trs {
		if tr.Cause == domain.CauseMatcher {
			n++
		}
	}
	return n
}
