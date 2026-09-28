package ingest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/lifecycle"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

// collectDecisions runs a manual session collection as SYSTEM and returns
// each candidate item's decision.
func (f *fixture) collectDecisions(request string) map[string]domain.GCDecisionCode {
	f.t.Helper()
	svc, err := lifecycle.New(f.s, testPolicy())
	if err != nil {
		f.t.Fatal(err)
	}
	got := map[string]domain.GCDecisionCode{}
	if err := f.s.Update(ctx, sess, func(tx store.Tx) error {
		out, err := svc.Collect(tx, principal(domain.AuthoritySystem), domain.CollectIntent{RequestID: request, Scope: domain.CollectSession, Trigger: domain.GCManual}, tx.NextSeq())
		if err != nil {
			return err
		}
		for _, d := range out.Result.Collect.Decisions {
			got[d.Target.ItemID] = d.Code
		}
		return nil
	}); err != nil {
		f.t.Fatalf("collect: %v", err)
	}
	return got
}

// TestIngestRegisteredOwnersOutliveTaskAndRestart_SPEC212 (SPEC-2.12,
// P3-32, C-15) end to end: ingest alone registers the owners (no seeded
// registration) when a SYSTEM event files a WORKFLOW-scoped goal and an
// AGENT-scoped pin. After their originating task completes, Collect
// protects both (never archives them), the goal stays OPEN, and all of it
// holds across a SQLite restart. Without ingest's registration both would
// be GC_INELIGIBLE (unknown lifetime).
func TestIngestRegisteredOwnersOutliveTaskAndRestart_SPEC212(t *testing.T) {
	path := sqlitetest.Path(t)
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t, db)
	pol := testPolicy()
	f.usePolicy(pol)
	sys := principal(domain.AuthoritySystem)
	e := domain.Event{EventID: "owners", Kind: domain.EventSystem, Spans: []domain.Span{
		textSpan(domain.AuthoritySystem, true, "## Goal [wg] scope=WORKFLOW\nShip the workflow.\n"),
		textSpan(domain.AuthoritySystem, true, "## Pinned\n- [ap] {scope=AGENT} Agent rule.\n")}}
	e.Spans[0].Access = domain.AccessBoundary{Scope: domain.ScopeWorkflow, SessionID: sess, WorkflowID: "wf"}
	e.Spans[1].Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, AgentID: "A"}
	r := f.mustIngest(sys, e)
	goal, pin := mustDirective(t, r, "wg"), mustDirective(t, r, "ap")
	if goal.Scope != domain.ScopeWorkflow || pin.Scope != domain.ScopeAgent {
		t.Fatalf("scopes: goal %s, pin %s", goal.Scope, pin.Scope)
	}
	if _, err := f.lifecycleService().CompleteTaskStandalone(ctx, sys, domain.CompleteTaskIntent{RequestID: "complete-t", TaskID: "T"}); err != nil {
		t.Fatalf("complete: %v", err)
	}
	check := func(f *fixture, request string) {
		t.Helper()
		got := f.collectDecisions(request)
		for _, it := range []domain.ContextItem{goal, pin} {
			if got[it.ID] != domain.GCProtected {
				t.Fatalf("%s: %s %s, want %s", request, it.Scope, got[it.ID], domain.GCProtected)
			}
		}
		if g := f.item(goal.ID); g.GoalStatus == nil || *g.GoalStatus != domain.GoalOpen {
			t.Fatalf("%s: WORKFLOW goal ended with its first task", request)
		}
	}
	check(f, "collect-1")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	check(newFixture(t, reopened), "collect-2")
}
