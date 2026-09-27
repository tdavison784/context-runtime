package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func (f fixture) unfinished(t *testing.T, s *Service) (bool, error) {
	var out bool
	var err error
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		out, err = s.UnfinishedTaskObligations(tx, "task")
		return nil
	})
	return out, err
}

func TestUnfinishedTaskObligations(t *testing.T) {
	f := newFixture(t)
	if u, err := f.unfinished(t, f.s); err != nil || !u {
		t.Fatalf("fresh obligations = %v %v", u, err)
	}
	// A materialization exception never counts as finished (FR-OBL-003).
	if _, err := f.s.setMat(t, f.st, f.system, domain.SetObligationMaterializationIntent{RequestID: "m", Target: f.sys, ExpectedRevision: 1, Disabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.transition(t, f.st, f.system, intent(f.user, 1, domain.ObligationWaived)); err != nil {
		t.Fatal(err)
	}
	if u, _ := f.unfinished(t, f.s); !u {
		t.Error("materialization-disabled obligation counted as finished")
	}
	if _, err := f.s.transition(t, f.st, f.system, intent(f.sys, 2, domain.ObligationBlocked)); err != nil {
		t.Fatal(err)
	}
	if u, _ := f.unfinished(t, f.s); !u {
		t.Error("blocked obligation counted as finished")
	}
	if _, err := f.s.transition(t, f.st, f.system, intent(f.sys, 3, domain.ObligationWaived)); err != nil {
		t.Fatal(err)
	}
	if u, err := f.unfinished(t, f.s); err != nil || u {
		t.Errorf("all waived = %v %v", u, err)
	}
	// An exhausted work bound is an error, never "finished".
	// The budget is shrunk after New: a valid policy needs room for its
	// live-proof dependents (MaxLiveProofDependents), a work bound of 1
	// does not.
	small, err := New(testPolicy(), DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	small.policy.MaxTransactionWork = 1
	if _, err := f.unfinished(t, small); !errors.Is(err, domain.ErrResourceLimit) {
		t.Errorf("bounded completion check: %v", err)
	}
}

func (f *evalFixture) satisfies(t *testing.T, viewer domain.Principal, current bool) (SatisfiesView, error) {
	var v SatisfiesView
	var err error
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		v, err = f.s.Satisfies(tx, viewer, f.sysTests, current)
		return nil
	})
	return v, err
}

func TestSatisfiesView(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	first := f.status(t, f.sysTests).CurrentProofID
	v, err := f.satisfies(t, f.userP, true)
	if err != nil || len(v.Relations) != 1 || v.Truncated || !v.Relations[0].Current || v.Relations[0].ProofID != first || v.Relations[0].Evidence.ItemID == "" {
		t.Fatalf("current view = %+v %v", v, err)
	}
	// Invalidation removes the current edge but keeps history.
	f.r.set(t, f.fixture, hashOf("W2"), false)
	if v, _ := f.satisfies(t, f.userP, true); len(v.Relations) != 0 {
		t.Errorf("invalidated proof still current: %+v", v)
	}
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W2"), nil)
	hist, _ := f.satisfies(t, f.userP, false)
	if len(hist.Relations) != 2 || hist.Relations[0].ProofID != first || hist.Relations[0].Current || !hist.Relations[1].Current {
		t.Errorf("history = %+v", hist)
	}
	// Access: outside viewers learn nothing.
	for name, viewer := range map[string]domain.Principal{
		"other task":    {SessionID: testSession, TaskID: "other", Authority: domain.AuthoritySystem},
		"other session": {SessionID: "s2", Authority: domain.AuthoritySystem},
	} {
		if _, err := f.satisfies(t, viewer, false); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestSatisfiesNoEdgeForAttestation(t *testing.T) {
	f := newEvalFixture(t)
	in := intent(f.sysTests, 1, domain.ObligationSatisfied)
	in.EvidenceIDs = []string{f.evidence.ID}
	if _, err := f.s.transition(t, f.st, f.system, in); err != nil {
		t.Fatal(err)
	}
	if v, err := f.satisfies(t, f.system, false); err != nil || len(v.Relations) != 0 {
		t.Errorf("attestation fabricated an evidence edge: %+v %v", v, err)
	}
}

func TestVisibleObligations(t *testing.T) {
	f := newFixture(t)
	var mine, theirs []ObligationView
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		mine, _ = f.s.VisibleObligations(tx, f.userP, "task")
		theirs, _ = f.s.VisibleObligations(tx, domain.Principal{SessionID: testSession, TaskID: "other", Authority: domain.AuthoritySystem}, "task")
		return nil
	})
	if len(mine) != 2 || len(theirs) != 0 {
		t.Errorf("visible = %d / %d", len(mine), len(theirs))
	}
}
