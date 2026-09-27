package obligation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func (f *evalFixture) newRun(t *testing.T) domain.ObservationRun {
	t.Helper()
	runN++
	run, err := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), fmt.Sprintf("exec-%d", runN), f.target))
	if err != nil {
		t.Fatal(err)
	}
	return run
}

// G1 (SEC-1.1 = SPEC-1.1 = DUR-1.1): a proof applies only if no newer
// complete applicable FAIL exists for the subject at the evaluation
// snapshot, whatever the obligation's current status.
func TestG1StalePassAfterNewerFail(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	older, newer := f.newRun(t), f.newRun(t)
	f.report(t, newer, domain.OutcomeFail, hashOf("W1"), nil)
	f.report(t, older, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("older PASS satisfied after a newer complete FAIL: %+v", o)
	}
}

func TestG1StalePassAfterRejection(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	r1, r2, r3 := f.newRun(t), f.newRun(t), f.newRun(t)
	f.report(t, r1, domain.OutcomePass, hashOf("W1"), nil)
	f.report(t, r3, domain.OutcomeFail, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("newer FAIL did not reject: %+v", o)
	}
	f.report(t, r2, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("delayed PASS between PASS and FAIL re-satisfied: %+v", o)
	}
}

// DUR-1.1: a run has one terminal outcome; a contradictory second terminal
// observation of the same run is rejected and changes nothing.
func TestG1OneTerminalObservationPerRun(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	run := f.newRun(t)
	f.report(t, run, domain.OutcomeFail, hashOf("W1"), nil)
	runN++
	_, err := f.observe(t, f.harness, obsIntent(fmt.Sprintf("obs-%d", runN), run, f.evidence.ID, domain.OutcomePass, hashOf("W1")))
	if !errors.Is(err, domain.ErrInvalidTransition) {
		t.Errorf("second terminal observation of one run: %v", err)
	}
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Errorf("same-run PASS after FAIL satisfied: %+v", o)
	}
}
