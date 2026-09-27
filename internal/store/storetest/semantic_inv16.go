package storetest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// testSemanticSatisfactionBacking checks INV-16 at commit (P3-41,
// DUR-1.9): a SATISFIED transition must be backed by exactly what its
// mode requires. An attestation names a stored ATTESTATION assertion of
// its own transition; a matcher satisfaction names a matcher proof and no
// assertion.
func testSemanticSatisfactionBacking(t *testing.T, s store.Store) {
	var o domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(SemanticDirective(sessA, "src", "dep", tx.NextSeq(), "Keep the build green")))
		o = NewObligation(sessA, "o1", 1, tx.NextSeq(), "src")
		o.Matcher = nil
		return tx.InsertObligationVersion(o)
	})
	user := NewPrincipal(sessA, domain.AuthorityUser)
	attest := func(seq uint64, assertion string) (domain.ObligationTransition, domain.TransitionDetail) {
		tr := domain.ObligationTransition{ID: "tr1", SessionID: sessA, ObligationID: "o1", Version: 1, Seq: seq, From: domain.ObligationUnresolved,
			To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation, Actor: user, Cause: domain.CauseAssertion,
			AssertionMode: domain.AssertionAttestation, RequestID: "req-tr1", ReasonCode: domain.ReasonAuthorizedTransition}
		d := domain.TransitionDetail{SemanticMeta: Meta(sessA, "td-tr1", seq), Target: Ref(o), TransitionID: "tr1", Cause: domain.CauseAssertion,
			AssertionID: assertion, RuleVersion: "rule/1"}
		return tr, d
	}
	// No assertion at all: SATISFIED with neither proof nor assertion.
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		tr, d := attest(tx.NextSeq(), "")
		_, err := semantic(t, tx).AppendSemanticObligationTransition(tr, d, 1)
		return err
	})
	if !errors.Is(err, domain.ErrInvalidRecord) {
		t.Errorf("attestation without an assertion: error = %v, want ErrInvalidRecord", err)
	}
	// An assertion whose mode is not the transition's.
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		tr, d := attest(seq, "a1")
		sem := semantic(t, tx)
		if _, err := sem.AppendSemanticObligationTransition(tr, d, 1); err != nil {
			return err
		}
		return sem.InsertAssertion(domain.AssertionRecord{SemanticMeta: Meta(sessA, "a1", seq), Target: Ref(o), Mode: domain.AssertionLegacy,
			Actor: user, TransitionID: "tr1", Access: o.Access})
	})
	if err == nil {
		t.Errorf("attestation backed by a %s assertion committed", domain.AssertionLegacy)
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := readSemantic(t, tx).ExactObligation(Ref(o))
		noErr(t, err)
		if got.Status != domain.ObligationUnresolved || got.Revision != 1 {
			t.Errorf("rejected satisfactions changed the version: %+v", got)
		}
		return nil
	})
}

// testSemanticStaleProof checks the commit-time half of G1 (INV-16,
// P3-16/22, DUR-1.1/1.9): a matcher proof resting on a run older than the
// CURRENT accepted ordinal of its subject partition, here a newer complete
// FAIL, is stale and cannot satisfy, however the transition was built.
func testSemanticStaleProof(t *testing.T, s store.Store) {
	o := proofWorld(t, s)
	var run2 domain.ObservationRun
	state := func(seq uint64, r domain.ObservationRun, obs string, a domain.ApplicabilityState) domain.SubjectState {
		return domain.SubjectState{SemanticMeta: Meta(sessA, "ss", seq), SubjectKey: r.SubjectKey, TaskID: "task", CurrentItemID: "ev-" + obs,
			ObservationID: obs, Access: r.Access, AcceptedOrdinal: r.Ordinal, Revision: 1, Applicability: a}
	}
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		run2 = NewObservationRun(t, sessA, "run2", "repo", "wb", tx.NextSeq())
		noErr(t, sem.InsertObservationRun(run2))
		noErr(t, tx.InsertItem(ToolEvidence(sessA, "ev-obs2", tx.NextSeq())))
		fail := NewObservation(run2, "obs2", "ev-obs2", tx.NextSeq(), fpA)
		fail.Outcome, fail.Passed, fail.Failed = domain.OutcomeFail, 2, 1
		noErr(t, sem.InsertObservation(fail))
		_, err := sem.PutSubjectState(state(tx.NextSeq(), run2, "obs2", domain.ApplicabilityCurrent), 0, "obs2")
		return err
	})
	satisfy := func(tx store.Tx) error {
		sem := semantic(t, tx)
		seq := tx.NextSeq()
		proof, deps := MatcherProof(t, o, "tr1", "obs1", "ev1", "evcov", seq)
		if err := sem.InsertApplicabilityProof(proof, deps); err != nil {
			return err
		}
		tr, d := MatcherTransition(o, "tr1", seq, proof, "g-m")
		_, err := sem.AppendSemanticObligationTransition(tr, d, 1)
		return err
	}
	err := s.Update(ctx, sessA, satisfy)
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("older PASS after a newer CURRENT FAIL: error = %v, want ErrInvalidTransition", err)
	}
	// Ordering is by run ordinal, not applicability (H1): the newer
	// complete FAIL still outranks the older PASS once its state is STALE.
	update(t, s, sessA, func(tx store.Tx) error {
		_, err := semantic(t, tx).PutSubjectState(state(tx.NextSeq(), run2, "obs2", domain.ApplicabilityStale), 1, "obs2")
		return err
	})
	if err := s.Update(ctx, sessA, satisfy); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("older PASS after a newer STALE FAIL: error = %v, want ErrInvalidTransition", err)
	}
}

// satisfyO1 satisfies proofWorld's o1 with its matcher proof on obs1.
func satisfyO1(t *testing.T, o domain.ObligationVersion) func(tx store.Tx) error {
	return func(tx store.Tx) error {
		sem := semantic(t, tx)
		seq := tx.NextSeq()
		proof, deps := MatcherProof(t, o, "tr1", "obs1", "ev1", "evcov", seq)
		if err := sem.InsertApplicabilityProof(proof, deps); err != nil {
			return err
		}
		tr, d := MatcherTransition(o, "tr1", seq, proof, "g-m")
		_, err := sem.AppendSemanticObligationTransition(tr, d, 1)
		return err
	}
}

// newerFail records a complete FAIL on a new run of o1's subject in
// boundary access, observed on fingerprint fp, filing no subject state.
func newerFail(t *testing.T, s store.Store, access domain.AccessBoundary, fp string) {
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		run := NewObservationRun(t, sessA, "run2", "repo", "wb", tx.NextSeq())
		run.Access = access
		noErr(t, sem.InsertObservationRun(run))
		ev := ToolEvidence(sessA, "ev-obs2", tx.NextSeq())
		ev.Access = access
		if access.AgentID != "" {
			ev.AgentID = access.AgentID
		}
		noErr(t, tx.InsertItem(ev))
		fail := NewObservation(run, "obs2", "ev-obs2", tx.NextSeq(), fp)
		fail.Outcome, fail.Passed, fail.Failed = domain.OutcomeFail, 2, 1
		return sem.InsertObservation(fail)
	})
}

// testSemanticStaleProofWithoutState checks H1 at commit: a newer complete
// FAIL outranks an older PASS even when it observed another fingerprint
// and so filed no subject state.
func testSemanticStaleProofWithoutState(t *testing.T, s store.Store) {
	o := proofWorld(t, s)
	newerFail(t, s, o.Access, fpB)
	if err := s.Update(ctx, sessA, satisfyO1(t, o)); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("older PASS after a newer inapplicable FAIL: error = %v, want ErrInvalidTransition", err)
	}
}

// testSemanticStaleProofPrivateFail checks H1's applicability boundary
// (SEC-2.9) at commit: a newer FAIL private to another agent does not
// cover a TASK-wide obligation, so it never rejects its proof.
func testSemanticStaleProofPrivateFail(t *testing.T, s store.Store) {
	o := proofWorld(t, s)
	private := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sessA, TaskID: "task", AgentID: "b"}
	newerFail(t, s, private, fpA)
	update(t, s, sessA, satisfyO1(t, o))
}
