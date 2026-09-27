package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// satisfiedBoundObligation inserts obligation id bound to source, stored
// SATISFIED by a RESOURCE_BOUND assertion resting on a proof with one
// WORKSPACE dependency, and returns the stored version.
func satisfiedBoundObligation(t *testing.T, tx store.Tx, sess, id, source string) domain.ObligationVersion {
	t.Helper()
	sem, err := store.Semantic(tx)
	if err != nil {
		t.Fatal(err)
	}
	if err := sem.InsertResourceBinding(storetest.NewResourceBinding(sess, "repo", tx.NextSeq())); err != nil {
		t.Fatal(err)
	}
	if err := sem.InsertWorkspaceBinding(storetest.NewWorkspaceBinding(sess, "wb", "repo", 1, tx.NextSeq())); err != nil {
		t.Fatal(err)
	}
	o := storetest.BoundObligation(t, sess, id, 1, tx.NextSeq(), source)
	if err := tx.InsertObligationVersion(o); err != nil {
		t.Fatal(err)
	}
	seq, trID := tx.NextSeq(), "tr-"+id
	proofID, err := domain.ApplicabilityProofID(storetest.Ref(o), trID)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := o.TargetSpec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	fp := domain.HashBytes([]byte("W1"))
	dep := domain.ProofDependency{SemanticMeta: storetest.Meta(sess, "dep-"+id, seq), ProofID: proofID, ResourceID: "repo",
		Kind: domain.DependencyWorkspace, ResourceRevision: 1, Fingerprint: fp, Access: o.Access}
	user := storetest.NewPrincipal(sess, domain.AuthorityUser)
	p := domain.ApplicabilityProof{ResourceID: "repo", Fingerprint: fp, ResourceRevision: 1, SemanticMeta: storetest.Meta(sess, proofID, seq),
		Target: storetest.Ref(o), TargetSpecHash: spec, TransitionID: trID, RuleVersion: "rule/1", AssertionID: "asr-" + id, DependencyIDs: []string{dep.ID}, Access: o.Access}
	if err := sem.InsertApplicabilityProof(p, []domain.ProofDependency{dep}); err != nil {
		t.Fatal(err)
	}
	if err := sem.InsertAssertion(domain.AssertionRecord{SemanticMeta: storetest.Meta(sess, "asr-"+id, seq), Target: storetest.Ref(o), Mode: domain.AssertionResourceBound,
		Actor: user, TransitionID: trID, ProofID: proofID, Access: o.Access}); err != nil {
		t.Fatal(err)
	}
	tr := domain.ObligationTransition{ID: trID, SessionID: sess, ObligationID: id, Version: 1, Seq: seq, From: domain.ObligationUnresolved,
		To: domain.ObligationSatisfied, Action: domain.ActionAssertObligation, Actor: user, Cause: domain.CauseAssertion,
		AssertionMode: domain.AssertionResourceBound, ProofID: proofID, RequestID: "req-" + trID, ReasonCode: domain.ReasonAuthorizedTransition}
	d := domain.TransitionDetail{SemanticMeta: storetest.Meta(sess, "td-"+trID, seq), Target: storetest.Ref(o), TransitionID: trID, Cause: domain.CauseAssertion,
		ProofID: proofID, AssertionID: "asr-" + id, RuleVersion: "rule/1"}
	o, err = sem.AppendSemanticObligationTransition(tr, d, 1)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// recordingSettler stands in for the obligation service's settler: it
// records each call and, when settle is set, bumps the version's revision in
// the caller's transaction (as the real RESOURCE_INVALIDATION write does),
// so retirement must re-read the version after settling.
type recordingSettler struct {
	t       *testing.T
	calls   []domain.ObligationRef
	current []bool
	settle  bool
}

func (s *recordingSettler) SettleBeforeRetireTx(tx store.Tx, target domain.ObligationRef) error {
	o, err := tx.Obligation(target.ObligationID)
	if err != nil {
		return err
	}
	s.calls = append(s.calls, target)
	s.current = append(s.current, o.Current)
	if !s.settle {
		return nil
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return err
	}
	seq, trID := tx.NextSeq(), "settle-"+o.ObligationID
	tr := domain.ObligationTransition{ID: trID, SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version, Seq: seq, From: domain.ObligationSatisfied,
		To: domain.ObligationWaived, Action: domain.ActionWaiveObligation, Actor: storetest.NewPrincipal(o.SessionID, domain.AuthorityUser), Cause: domain.CauseWaive,
		PriorProofID: o.CurrentProofID, RequestID: "req-" + trID, ReasonCode: domain.ReasonAuthorizedTransition}
	d := domain.TransitionDetail{SemanticMeta: storetest.Meta(o.SessionID, "td-"+trID, seq), Target: target, TransitionID: trID, Cause: domain.CauseWaive,
		PreviousProofID: o.CurrentProofID, RuleVersion: "rule/1"}
	_, err = sem.AppendSemanticObligationTransition(tr, d, o.Revision)
	return err
}

// withDerivedValidity makes proofDerivedValid report valid for this test.
func withDerivedValidity(t *testing.T, valid bool) {
	t.Helper()
	prior := proofDerivedValid
	proofDerivedValid = func(store.SemanticReader, string) (bool, error) { return valid, nil }
	t.Cleanup(func() { proofDerivedValid = prior })
}

// TestReplacementSettlesBeforeRetiring_M2 (K1 A3, ruling M2): replacing the
// source of a stored-SATISFIED resource-bound obligation calls the injected
// settler for the current version in the same transaction BEFORE retirement
// is planned; retirement then uses the settled revision. Without a settler,
// retiring a derived-invalid version is refused and nothing changes; a
// derived-valid one retires normally.
func TestReplacementSettlesBeforeRetiring_M2(t *testing.T) {
	for _, tc := range []struct {
		name        string
		valid       bool
		settler     bool
		wantRefused bool
	}{
		{name: "settler, derived invalid", valid: false, settler: true},
		{name: "settler, derived valid", valid: true, settler: true},
		{name: "no settler, derived invalid", valid: false, wantRefused: true},
		{name: "no settler, derived valid", valid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s store.Store) {
				const sess = "sess-m2"
				withDerivedValidity(t, tc.valid)
				actor := principal(sess, domain.AuthoritySystem)
				var o domain.ObligationVersion
				update(t, s, sess, func(tx store.Tx) error {
					pin := newDirective(sess, "p1", "tests", tx.NextSeq(), "All tests must pass.")
					mustCreate(t, tx, pin)
					if _, err := ReplaceDirective(tx, actor, "task", "tests", pin.ID, "evt-p1"); err != nil {
						return err
					}
					o = satisfiedBoundObligation(t, tx, sess, "o1", pin.ID)
					return nil
				})
				settler := &recordingSettler{t: t, settle: !tc.valid}
				var opts []Option
				if tc.settler {
					opts = append(opts, WithPendingSettler(settler))
				}
				err := s.Update(ctx, sess, func(tx store.Tx) error {
					p2 := newDirective(sess, "p2", "tests", tx.NextSeq(), "All tests must pass, again.")
					mustCreate(t, tx, p2)
					_, err := ReplaceDirective(tx, actor, "task", "tests", p2.ID, "evt-p2", opts...)
					return err
				})
				if tc.wantRefused {
					if !errors.Is(err, ErrPendingSettlement) || !errors.Is(err, domain.ErrVersionConflict) {
						t.Fatalf("retirement over a pending settlement: err = %v, want ErrPendingSettlement", err)
					}
					view(t, s, sess, func(tx store.ReadTx) error {
						if got := obligation(t, tx, "o1"); !got.Current || got.Revision != o.Revision {
							t.Errorf("refused replacement changed the obligation: %+v", got)
						}
						if ok, err := IsCurrent(tx, "p1"); err != nil || !ok {
							t.Errorf("refused replacement retired the source: %v %v", ok, err)
						}
						return nil
					})
					return
				}
				if err != nil {
					t.Fatalf("replacement: %v", err)
				}
				if tc.settler && (len(settler.calls) != 1 || settler.calls[0] != storetest.Ref(o) || !settler.current[0]) {
					t.Fatalf("settler calls = %+v (current %v), want one call on the current version before retirement", settler.calls, settler.current)
				}
				view(t, s, sess, func(tx store.ReadTx) error {
					if got := obligation(t, tx, "o1"); got.Current {
						t.Errorf("obligation not retired: %+v", got)
					}
					return nil
				})
			})
		})
	}
}
