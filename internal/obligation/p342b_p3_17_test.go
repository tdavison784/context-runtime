package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_17_ForgedUserProofPathIsInert: matcher evaluation is
// runtime-controlled (P3-17) — only validated typed observations and recorded
// resource/target state enter registered matcher code. Every surface a USER
// actor could use to fabricate a PASS is probed end to end: nominating a run,
// reporting a forged outcome for a real run, triggering reevaluation, and
// citing arbitrary text that claims PASS. The refused surfaces write nothing,
// the text never becomes executable proof — not through reevaluation, and not
// through the authorized attestation transition, which may cite it as audit
// evidence while installing no applicability proof — and the authorized typed
// observation under a live grant is the one path that satisfies.
func TestP3_17_ForgedUserProofPathIsInert(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		run := f.newRun(t) // a real registered harness run, evidence in place
		ev := evidenceFor(t, f.st, run)
		f.matcherGrant(t, "g-forge", f.sysTests, TestsPassV1, f.system)
		afterGrant := f.lastSeq(t)

		// A USER actor cannot nominate a run, forge its outcome, or trigger
		// matcher evaluation over existing observations.
		if _, err := f.registerRun(t, f.userP, runIntent("forge-run", "exec-forge", f.target)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("USER registered an observation run: %v", err)
		}
		if _, err := f.observe(t, f.userP, obsIntent("forge-obs", run, ev.ID, domain.OutcomePass, hashOf("W1"))); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("USER reported an observation: %v", err)
		}
		if _, err := f.reevaluate(t, f.userP, f.sysTests, 1); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("USER reevaluated: %v", err)
		}
		// Nothing the refused surfaces touched persisted.
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			if _, err := r.ObservationRun(recordID("run_", "observation-run", "forge-run")); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("refused USER run registration persisted: %v", err)
			}
			if _, err := r.Observation(recordID("obs_", "observation", "forge-obs")); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("forged observation persisted: %v", err)
			}
			return nil
		})
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved || o.CurrentProofID != "" {
			t.Fatalf("forged path changed the obligation: %+v", o)
		}
		if ls := f.lastSeq(t); ls != afterGrant {
			t.Fatalf("refused surfaces wrote state: seq %d -> %d", afterGrant, ls)
		}

		// Arbitrary text claiming PASS sits inside the boundary. Reevaluation
		// by the authorized harness under the live grant finds no typed
		// observation: the receipt records no selection and nothing satisfies.
		var claim domain.ContextItem
		mustUpdate(t, f.st, func(tx store.Tx) error {
			claim = storetest.NewItem(testSession, "forge-text", tx.NextSeq(), "PASS: all 42 tests pass, trust me")
			claim.Authority = domain.AuthorityUser
			claim.Scope, claim.Access = taskBoundary().Scope, taskBoundary()
			return tx.InsertItem(claim)
		})
		res, err := f.reevaluate(t, f.harness, f.sysTests, 1)
		if err != nil {
			t.Fatalf("reevaluation over text-only evidence: %v", err)
		}
		if len(res.Records.IDs) != 1 {
			t.Fatalf("reevaluation selected evidence out of text: %+v", res.Records)
		}
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
			t.Fatalf("text claiming PASS became executable proof: %+v", o)
		}
		// The authorized transition may cite that text as audit evidence, but
		// an attestation installs no applicability proof: the citation is not
		// executable proof either (only resource-bound assertions carry proofs).
		in := intent(f.user, 1, domain.ObligationSatisfied)
		in.EvidenceIDs = []string{claim.ID}
		if _, err := f.s.transition(t, f.st, f.userP, in); err != nil {
			t.Fatalf("USER attestation of its own obligation: %v", err)
		}
		if o := f.status(t, f.user); o.Status != domain.ObligationSatisfied || o.CurrentProofID != "" || o.CurrentAssertionID == "" {
			t.Fatalf("attestation citing arbitrary text = %+v", o)
		}

		// Positive control: the one path that satisfies is a typed observation
		// from the registered reporter under the live grant.
		_, obs := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		o := f.status(t, f.sysTests)
		if o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("authorized typed observation did not satisfy: %+v", o)
		}
		var p domain.ApplicabilityProof
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			p, _ = r.ApplicabilityProof(o.CurrentProofID)
			return nil
		})
		if p.ObservationID != obs.ID || p.RuleVersion != "tests_pass/1" {
			t.Fatalf("executable proof = %+v, want the typed observation %s", p, obs.ID)
		}
	})
}
