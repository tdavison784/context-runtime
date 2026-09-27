package obligation

import (
	"errors"
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
