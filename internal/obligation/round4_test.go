package obligation

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// SEC-4.10 = SPEC-4.8 (P3-14/P3-20): versioning a workspace binding is no
// existence oracle. Whatever version an actor names, a binding whose latest
// version it cannot read answers exactly as a binding that does not exist.
func TestSEC410BindingVersionIsNoOracle(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	harness := actorOf(domain.AuthorityHarness)
	system := actorOf(domain.AuthoritySystem)
	seedTask(t, st, "task")
	seedResource(t, st, "repo1", harness)
	task := domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"}
	agentB := domain.Principal{SessionID: testSession, WorkflowID: system.WorkflowID, TaskID: "task", AgentID: "b", Authority: domain.AuthoritySystem}
	for v := uint64(1); v <= 2; v++ {
		in := bindIntent("ws-hidden", v, task)
		in.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task", AgentID: "b"}
		if _, err := s.bindWS(t, st, agentB, in); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"ws-hidden", "ws-absent"} {
		for v := uint64(2); v <= 4; v++ {
			in := bindIntent(id, v, task)
			in.RequestID = "probe-" + id + "-" + string(rune('0'+v))
			if _, err := s.bindWS(t, st, system, in); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("%s v%d: %v, want ErrNotFound", id, v, err)
			}
		}
	}
	// A first version of an existing hidden ID is refused as not found too.
	first := bindIntent("ws-hidden", 1, task)
	first.RequestID = "probe-ws-hidden-first"
	if _, err := s.bindWS(t, st, system, first); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("ws-hidden v1: %v, want ErrNotFound", err)
	}
	// Control: a new ID starts at version 1.
	if _, err := s.bindWS(t, st, system, bindIntent("ws-absent", 1, task)); err != nil {
		t.Errorf("new binding v1: %v", err)
	}
}

// K1 A2: EffectiveStatus passes non-SATISFIED statuses and attestations
// through, and keeps a SATISFIED matcher proof SATISFIED while it is valid.
func TestEffectiveStatusPassThrough(t *testing.T) {
	f := newEvalFixture(t)
	check := func(step string, ref domain.ObligationRef, want domain.ObligationStatus) {
		t.Helper()
		o := f.status(t, ref)
		var got domain.ObligationStatus
		var pending bool
		var err error
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			got, pending, err = EffectiveStatus(r, o)
			return nil
		})
		if err != nil || got != want || pending {
			t.Errorf("%s: effective = %s pending=%v err=%v, want %s", step, got, pending, err, want)
		}
	}
	check("unresolved", f.sysTests, domain.ObligationUnresolved)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	check("matcher proof", f.sysTests, domain.ObligationSatisfied)
	o := f.status(t, f.user)
	if _, err := f.s.transition(t, f.st, f.system, intent(f.user, o.Revision, domain.ObligationSatisfied)); err != nil {
		t.Fatal(err)
	}
	check("attestation", f.user, domain.ObligationSatisfied)
}

// SPEC-4.10 (P3-22): reevaluation selects only evidence whose derived
// applicability is CURRENT; a subject state stale at read is never selected
// or named in the receipt, and a current one still is.
func TestSPEC410ReevaluationSelectsOnlyCurrentEvidence(t *testing.T) {
	f := newEvalFixture(t)
	obs := f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	f.r.set(t, f.fixture, hashOf("W2"), false) // the PASS no longer describes the workspace
	res, err := f.reevaluate(t, f.harness, f.sysTests, f.status(t, f.sysTests).Revision)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range res.Records.IDs {
		if id == obs.ID {
			t.Errorf("reevaluation selected stale evidence %s: %v", obs.ID, res.Records.IDs)
		}
	}
	f.r.set(t, f.fixture, hashOf("W1"), false) // the same PASS is current again
	res, err = f.reevaluate(t, f.harness, f.sysTests, f.status(t, f.sysTests).Revision)
	if err != nil {
		t.Fatal(err)
	}
	if ids := res.Records.IDs; len(ids) != 2 || ids[1] != obs.ID {
		t.Errorf("reevaluation of current evidence = %v, want it to name %s", ids, obs.ID)
	}
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Errorf("current evidence did not satisfy: %+v", o)
	}
}

// GLM-1: the service exposes no viewer-less subject-applicability read.
// Consumers derive applicability with store.SubjectApplicability over a
// state they have already access-checked; the service's own derivation is
// internal.
func TestNoViewerlessSubjectApplicability_GLM1(t *testing.T) {
	if _, ok := reflect.TypeOf(&Service{}).MethodByName("SubjectApplicability"); ok {
		t.Error("obligation.Service.SubjectApplicability is exported without a viewer")
	}
}
