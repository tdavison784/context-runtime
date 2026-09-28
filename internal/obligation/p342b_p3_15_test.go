package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// P3-15 (ADR 8 :1132/:1133): the assertion mode is explicit and the two
// attestation shapes behave differently. The cited test
// (TestAssertionModeIsExplicit, internal/domain) only validates intents with
// citations already set — a bare attestation is never validated, and nothing
// drives either shape through the service.

// TestP3_15_BareAttestationCommitsAssertionOnly closes the MISSING half of
// "bare attestation" (ADR 8 :1132). Through the real transition path on both
// stores, a satisfaction with neither citations nor resource claims commits:
// the version turns SATISFIED carrying an assertion record of mode
// ATTESTATION attributed to the direct authority, with NO applicability
// proof, NO evidence, and NO SATISFIES relation — nothing is fabricated
// around the bare word of the asserting authority. The same intent carrying
// resource claims is refused by validation before anything is written.
func TestP3_15_BareAttestationCommitsAssertionOnly(t *testing.T) {
	p3_14BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		in := intent(f.sysTests, 1, domain.ObligationSatisfied)
		in.RequestID = "tr-p315-bare"
		if _, err := f.s.transition(t, f.st, f.system, in); err != nil {
			t.Fatalf("bare attestation: %v", err)
		}
		o := f.status(t, f.sysTests)
		if o.Status != domain.ObligationSatisfied || o.Revision != 2 || o.CurrentAssertionID == "" || o.CurrentProofID != "" {
			t.Fatalf("bare attestation: version = %+v, want SATISFIED rev 2 with an assertion and no proof", o)
		}
		h := f.history(t, f.sysTests)
		if len(h) != 1 {
			t.Fatalf("bare attestation: history = %+v", h)
		}
		tr := h[0]
		if tr.AssertionMode != domain.AssertionAttestation || tr.ProofID != "" || len(tr.EvidenceIDs) != 0 ||
			tr.GrantID != "" || tr.Cause != domain.CauseAssertion || tr.Actor != f.system {
			t.Errorf("bare attestation: transition = %+v", tr)
		}
		var a domain.AssertionRecord
		if err := f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			a, err = r.Assertion(o.CurrentAssertionID)
			return err
		}); err != nil {
			t.Fatalf("assertion record: %v", err)
		}
		if a.Mode != domain.AssertionAttestation || a.ProofID != "" || a.GrantID != "" || a.TransitionID != tr.ID || a.Actor != f.system {
			t.Errorf("assertion record = %+v", a)
		}
		// No proof-backed relation exists to view: the SATISFIES view of a
		// bare attestation is empty, current or historical.
		for _, current := range []bool{true, false} {
			v, err := f.satisfiesOf(t, f.harness, f.sysTests, current)
			if err != nil || len(v.Relations) != 0 || v.Truncated {
				t.Errorf("SATISFIES (current=%v) after a bare attestation = %+v (%v)", current, v.Relations, err)
			}
		}

		// An attestation may not carry resource claims: validation refuses
		// it before anything is written.
		impure := intent(f.sysTests, o.Revision, domain.ObligationSatisfied)
		impure.RequestID = "tr-p315-impure"
		impure.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo1", Fingerprint: hashOf("W1")}}
		if _, err := f.s.transition(t, f.st, f.system, impure); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("attestation with resource claims: %v, want ErrInvalidRecord", err)
		}
		if after := f.status(t, f.sysTests); after.Revision != o.Revision || after.Status != domain.ObligationSatisfied || len(f.history(t, f.sysTests)) != 1 {
			t.Errorf("refused impure attestation changed state: %+v", after)
		}
	})
}

// TestP3_15_CitedAttestationRecordsCitationsAndRefusesBogusOnes closes the
// MISSING half of "attestation with citations" (ADR 8 :1133). A satisfaction
// citing real evidence occurrences commits with the citations recorded on
// the transition and cached on the version — still an ATTESTATION: no
// applicability proof, no fabricated SATISFIES edge for the citations. A
// citation naming no stored occurrence is refused whole with the fixed
// ErrNotFound and leaves no status, history or cache change; the same
// request shape with the real citation then commits.
func TestP3_15_CitedAttestationRecordsCitationsAndRefusesBogusOnes(t *testing.T) {
	p3_14BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		ev := seedEvidence(t, f.st, "ev-p315-cite", taskBoundary())

		bogus := intent(f.sysTests, 1, domain.ObligationSatisfied)
		bogus.RequestID = "tr-p315-bogus"
		bogus.EvidenceIDs = []string{"ev-p315-never-stored"}
		if _, err := f.s.transition(t, f.st, f.system, bogus); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("bogus citation: %v, want ErrNotFound", err)
		}
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved || o.Revision != 1 || o.CurrentAssertionID != "" {
			t.Errorf("bogus citation changed the version: %+v", o)
		}
		if h := f.history(t, f.sysTests); len(h) != 0 {
			t.Errorf("bogus citation left transitions: %+v", h)
		}

		in := intent(f.sysTests, 1, domain.ObligationSatisfied)
		in.RequestID = "tr-p315-cited"
		in.EvidenceIDs = []string{ev.ID}
		if _, err := f.s.transition(t, f.st, f.system, in); err != nil {
			t.Fatalf("cited attestation: %v", err)
		}
		o := f.status(t, f.sysTests)
		if o.Status != domain.ObligationSatisfied || o.Revision != 2 || o.CurrentAssertionID == "" || o.CurrentProofID != "" {
			t.Fatalf("cited attestation: version = %+v", o)
		}
		if !equalIDs(o.EvidenceIDs, []string{ev.ID}) {
			t.Errorf("cited attestation: version evidence cache = %v, want [%s]", o.EvidenceIDs, ev.ID)
		}
		h := f.history(t, f.sysTests)
		if len(h) != 1 || !equalIDs(h[0].EvidenceIDs, []string{ev.ID}) || h[0].AssertionMode != domain.AssertionAttestation || h[0].ProofID != "" {
			t.Errorf("cited attestation: transition = %+v", h)
		}
		// Citations do not turn an attestation into a proof-backed
		// satisfaction: the SATISFIES view stays empty.
		if v, err := f.satisfiesOf(t, f.harness, f.sysTests, true); err != nil || len(v.Relations) != 0 {
			t.Errorf("cited attestation fabricated a current SATISFIES edge: %+v (%v)", v.Relations, err)
		}
	})
}
