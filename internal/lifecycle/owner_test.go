package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// seedBroadOwners commits task T1 with a WORKFLOW-scoped OPEN goal and an
// AGENT-scoped pin, optionally registering both owners as the trusted
// ingestion (HARNESS) actor would on first association (P3-32, SPEC-1.7).
func seedBroadOwners(t *testing.T, db store.Store, register bool) {
	t.Helper()
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		if _, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
			return err
		}
		if register {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
			for _, o := range []domain.OwnerRegistration{
				{Kind: domain.OwnerWorkflow, OwnerID: "wf", SourceID: "evt-1"},
				{Kind: domain.OwnerAgent, OwnerID: "agent", WorkflowID: "wf", SourceID: "evt-1"},
			} {
				o.SemanticMeta = domain.SemanticMeta{ID: "owner-" + string(o.Kind), SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()}
				o.Actor = harness
				if err := sem.InsertOwnerRegistration(o); err != nil {
					return err
				}
			}
		}
		goal := storetest.NewGoal("s", "wf-goal", tx.NextSeq(), "ship the workflow")
		goal.Scope, goal.Access = domain.ScopeWorkflow, domain.AccessBoundary{Scope: domain.ScopeWorkflow, SessionID: "s", WorkflowID: "wf"}
		pin := storetest.NewItem("s", "agent-pin", tx.NextSeq(), "agent rule")
		pin.Kind, pin.Generation, pin.Retention = domain.KindConstraint, domain.GenerationPinned, domain.RetentionProtected
		pin.Scope, pin.Access = domain.ScopeAgent, domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", WorkflowID: "wf", AgentID: "agent"}
		for _, it := range []domain.ContextItem{goal, pin} {
			if err := tx.InsertItem(it); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// collectDecisions runs a session collection and returns each candidate's
// decision; callers complete T1 first where the test requires it.
func collectDecisions(t *testing.T, db store.Store, request string) map[string]domain.GCDecisionCode {
	t.Helper()
	s, _ := New(db, testPolicy())
	out, err := collect(newFacets(), db, s, storetest.NewPrincipal("s", domain.AuthoritySystem), domain.CollectIntent{RequestID: request, Scope: domain.CollectSession, Trigger: domain.GCManual})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]domain.GCDecisionCode{}
	for _, d := range out.Result.Collect.Decisions {
		got[d.Target.ItemID] = d.Code
	}
	return got
}

func TestRegisteredBroadOwnersOutliveTheirTask(t *testing.T) {
	ctx := context.Background()
	for _, register := range []bool{true, false} {
		want := domain.GCProtected
		if !register {
			want = domain.GCIneligible // unknown owner: never archived, never selected
		}
		eachStore(t, func(t *testing.T, db store.Store) {
			seedBroadOwners(t, db, register)
			s, _ := New(db, testPolicy())
			if _, err := s.CompleteTaskStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}); err != nil {
				t.Fatal(err)
			}
			got := collectDecisions(t, db, "c")
			for _, id := range []string{"wf-goal", "agent-pin"} {
				if got[id] != want {
					t.Fatalf("register=%v %s: %s, want %s", register, id, got[id], want)
				}
			}
			if register {
				if err := db.View(ctx, "s", func(tx store.ReadTx) error {
					sem, err := store.ReadSemantic(tx)
					if err != nil {
						return err
					}
					goal, _ := tx.Item("wf-goal")
					o, err := sem.OwnerRegistration(domain.OwnerWorkflow, "wf")
					if err != nil {
						return err
					}
					if *goal.GoalStatus != domain.GoalOpen || policy.ScopeLifetime(goal, policy.OwnerSnapshot{Seq: tx.LastSeq(), Owner: &o}) != domain.ExpiryLive {
						t.Fatal("WORKFLOW goal ended with its first task")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestOwnerRegistrationSurvivesSQLiteRestart(t *testing.T) {
	ctx := context.Background()
	path := sqlitetest.Path(t)
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedBroadOwners(t, db, true)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got := collectDecisions(t, reopened, "c")
	for _, id := range []string{"wf-goal", "agent-pin"} {
		if got[id] != domain.GCProtected {
			t.Fatalf("%s after restart: %s", id, got[id])
		}
	}
}
