package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// SEC-3.10 (P3-20, FR-AUTH-002): a workspace binding version can be
// appended only by an actor whose authority is at least the previous
// version's reporter's and who may read the previous version. A lower
// authority never overrides a higher-authority binding, in its own context
// or by moving it to another; a hidden previous version answers as absent.
func TestSEC310BindingVersionNeedsReporterAuthority(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	harness := actorOf(domain.AuthorityHarness)
	system := actorOf(domain.AuthoritySystem)
	seedTask(t, st, "task")
	seedResource(t, st, "repo1", harness)
	sysPin := seedPinned(t, st, "p-sys", "sys", domain.AuthoritySystem, "All tests must pass.")
	task := domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"}
	if _, err := s.bindWS(t, st, system, bindIntent("ws-sys", 1, task)); err != nil {
		t.Fatal(err)
	}
	resolved := func(step string) {
		t.Helper()
		if ws := resolveFor(t, s, st, sysPin); ws.Binding == nil || ws.Binding.ID != "ws-sys" || ws.Binding.Version != 1 {
			t.Errorf("%s: SYSTEM pin resolves %+v, want ws-sys v1", step, ws)
		}
	}
	resolved("setup")
	same := bindIntent("ws-sys", 2, task)
	same.RequestID = "harness-v2-same"
	if _, err := s.bindWS(t, st, harness, same); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Errorf("HARNESS v2 of a SYSTEM binding, same context: %v", err)
	}
	resolved("same context")
	cross := bindIntent("ws-sys", 2, domain.WorkspaceSourceContext{Kind: domain.WorkspaceSource, ID: sysPin.ID})
	cross.RequestID = "harness-v2-cross"
	if _, err := s.bindWS(t, st, harness, cross); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Errorf("HARNESS v2 of a SYSTEM binding, other context: %v", err)
	}
	resolved("other context")
	// Equal or higher authority may append.
	if _, err := s.bindWS(t, st, harness, bindIntent("ws-h", 1, task)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.bindWS(t, st, harness, bindIntent("ws-h", 2, task)); err != nil {
		t.Errorf("HARNESS v2 of its own HARNESS binding: %v", err)
	}
	if _, err := s.bindWS(t, st, system, bindIntent("ws-h", 3, task)); err != nil {
		t.Errorf("SYSTEM v3 of a HARNESS binding: %v", err)
	}
	// A previous version the actor cannot read is absent, not refused.
	agentB := domain.Principal{SessionID: testSession, WorkflowID: system.WorkflowID, TaskID: "task", AgentID: "b", Authority: domain.AuthoritySystem}
	private := bindIntent("ws-b", 1, task)
	private.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task", AgentID: "b"}
	if _, err := s.bindWS(t, st, agentB, private); err != nil {
		t.Fatal(err)
	}
	if _, err := s.bindWS(t, st, system, bindIntent("ws-b", 2, task)); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("v2 over a previous version hidden from the actor: %v, want ErrNotFound", err)
	}
}
