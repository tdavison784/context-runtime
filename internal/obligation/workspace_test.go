package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func bindIntent(id string, version uint64, ctx domain.WorkspaceSourceContext) domain.WorkspaceBindingIntent {
	in := domain.WorkspaceBindingIntent{
		Context: ctx, RequestID: "bind-" + id + "-" + string(rune('0'+version)), BindingID: id, ResourceID: "repo1", Version: version,
		BaseDir: ".", EnvironmentSpec: "env1", SuiteSpec: "go-test-all", CoverageSpec: "all", Access: taskBoundary(),
	}
	switch ctx.Kind {
	case domain.WorkspaceSource:
		in.SourceItemID = ctx.ID
	case domain.WorkspaceTask:
		in.TaskID = ctx.ID
	case domain.WorkspaceConversation:
		in.ConversationID = ctx.ID
	}
	return in
}

func (s *Service) bindWS(t *testing.T, st store.Store, actor domain.Principal, in domain.WorkspaceBindingIntent) (domain.MutationResult, error) {
	t.Helper()
	var res domain.MutationResult
	err := st.Update(t.Context(), testSession, func(tx store.Tx) error {
		var err error
		res, err = s.BindWorkspaceTx(tx, actor, in, tx.NextSeq())
		return err
	})
	return res, err
}

func resolveFor(t *testing.T, s *Service, st store.Store, source domain.ContextItem) Workspace {
	t.Helper()
	var ws Workspace
	err := st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		ws, err = s.resolveWorkspace(r, source)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return ws
}

func TestBindWorkspace(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	harness := actorOf(domain.AuthorityHarness)
	seedTask(t, st, "task")
	seedResource(t, st, "repo1", harness)
	task := domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"}

	res, err := s.bindWS(t, st, harness, bindIntent("ws1", 1, task))
	if err != nil || res.Records.Kind != "WORKSPACE_BINDING" || res.Records.IDs[0] != "ws1" {
		t.Fatalf("bind = %+v %v", res, err)
	}
	// Replay returns the original result; a changed payload conflicts.
	if again, err := s.bindWS(t, st, harness, bindIntent("ws1", 1, task)); err != nil || again.Records.IDs[0] != "ws1" {
		t.Errorf("replay = %+v %v", again, err)
	}
	changed := bindIntent("ws1", 1, task)
	changed.SuiteSpec = "unit"
	if _, err := s.bindWS(t, st, harness, changed); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Errorf("changed replay = %v", err)
	}
	// Versions are immutable and dense.
	dup := bindIntent("ws1", 1, task)
	dup.RequestID = "other"
	if _, err := s.bindWS(t, st, harness, dup); err == nil {
		t.Error("same version rebound")
	}
	skip := bindIntent("ws1", 3, task)
	if _, err := s.bindWS(t, st, harness, skip); err == nil {
		t.Error("version gap accepted")
	}
	if _, err := s.bindWS(t, st, harness, bindIntent("ws1", 2, task)); err != nil {
		t.Errorf("next version: %v", err)
	}

	for name, c := range map[string]struct {
		actor domain.Principal
		in    domain.WorkspaceBindingIntent
		want  error
	}{
		"user binder":    {actorOf(domain.AuthorityUser), bindIntent("wsU", 1, task), domain.ErrInvalidAuthorityPromotion},
		"agent binder":   {actorOf(domain.AuthorityAgent), bindIntent("wsA", 1, task), domain.ErrInvalidAuthorityPromotion},
		"unknown task":   {harness, bindIntent("wsT", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "nope"}), domain.ErrNotFound},
		"unknown source": {harness, bindIntent("wsS", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceSource, ID: "nope"}), domain.ErrNotFound},
		"unknown convo":  {harness, bindIntent("wsC", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceConversation, ID: "nope"}), domain.ErrNotFound},
		"unknown resource": {harness, func() domain.WorkspaceBindingIntent {
			in := bindIntent("wsR", 1, task)
			in.ResourceID = "repo9"
			return in
		}(), domain.ErrNotFound},
		"foreign session": {domain.Principal{SessionID: "s2", Authority: domain.AuthorityHarness}, bindIntent("wsX", 1, task), domain.ErrNotFound},
		"noncanonical base": {harness, func() domain.WorkspaceBindingIntent {
			in := bindIntent("wsB", 1, task)
			in.BaseDir = "./svc"
			return in
		}(), domain.ErrInvalidRecord},
		"raw environment": {harness, func() domain.WorkspaceBindingIntent {
			in := bindIntent("wsE", 1, task)
			in.EnvironmentSpec = "PATH=/usr/bin"
			return in
		}(), domain.ErrInvalidRecord},
		"binder outside box": {domain.Principal{SessionID: testSession, TaskID: "other", Authority: domain.AuthorityHarness}, bindIntent("wsO", 1, task), domain.ErrNotFound},
	} {
		if _, err := s.bindWS(t, st, c.actor, c.in); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
}

func TestResolveWorkspace(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	harness := actorOf(domain.AuthorityHarness)
	system := actorOf(domain.AuthoritySystem)
	seedTask(t, st, "task")
	seedResource(t, st, "repo1", harness)
	userPin := seedPinned(t, st, "p-user", "tests", domain.AuthorityUser, "All tests must pass.")
	sysPin := seedPinned(t, st, "p-sys", "sys", domain.AuthoritySystem, "All tests must pass.")
	task := domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"}

	if ws := resolveFor(t, s, st, userPin); ws.Binding != nil || ws.Reason != "" {
		t.Fatalf("no bindings = %+v", ws)
	}
	if _, err := s.bindWS(t, st, harness, bindIntent("ws1", 1, task)); err != nil {
		t.Fatal(err)
	}
	if ws := resolveFor(t, s, st, userPin); ws.Binding == nil || ws.Binding.ID != "ws1" {
		t.Errorf("task binding for USER pin = %+v", ws)
	}
	// Q-3: a HARNESS binding never decides how a SYSTEM requirement is met.
	if ws := resolveFor(t, s, st, sysPin); ws.Binding != nil || ws.Reason != domain.ReasonBindingAuthority {
		t.Errorf("HARNESS binding for SYSTEM pin = %+v", ws)
	}
	// The latest version of a binding ID is the one considered.
	v2 := bindIntent("ws1", 2, task)
	v2.SuiteSpec = "suite-v2"
	if _, err := s.bindWS(t, st, harness, v2); err != nil {
		t.Fatal(err)
	}
	if ws := resolveFor(t, s, st, userPin); ws.Binding == nil || ws.Binding.Version != 2 || ws.Binding.SuiteSpec != "suite-v2" {
		t.Errorf("latest version = %+v", ws.Binding)
	}
	// A second applicable task binding is ambiguous.
	if _, err := s.bindWS(t, st, system, bindIntent("ws2", 1, task)); err != nil {
		t.Fatal(err)
	}
	if ws := resolveFor(t, s, st, userPin); ws.Binding != nil || ws.Reason != domain.ReasonBindingAmbiguous {
		t.Errorf("two task bindings = %+v", ws)
	}
	if ws := resolveFor(t, s, st, sysPin); ws.Binding == nil || ws.Binding.ID != "ws2" {
		t.Errorf("SYSTEM binding for SYSTEM pin = %+v", ws)
	}
	// A source binding shadows task bindings.
	src := bindIntent("ws3", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceSource, ID: userPin.ID})
	if _, err := s.bindWS(t, st, harness, src); err != nil {
		t.Fatal(err)
	}
	if ws := resolveFor(t, s, st, userPin); ws.Binding == nil || ws.Binding.ID != "ws3" {
		t.Errorf("source binding = %+v", ws)
	}
	// A binding narrower than the source does not cover it.
	narrow := bindIntent("ws4", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceSource, ID: sysPin.ID})
	narrow.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: testSession, TaskID: "task", AgentID: "agent"}
	if _, err := s.bindWS(t, st, system, narrow); err != nil {
		t.Fatal(err)
	}
	if ws := resolveFor(t, s, st, sysPin); ws.Binding != nil || ws.Reason != domain.ReasonBindingAuthority {
		t.Errorf("narrow binding = %+v", ws)
	}
}
