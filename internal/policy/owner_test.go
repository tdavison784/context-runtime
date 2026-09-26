package policy

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestScopeLifetimeUsesDeclaredOwner(t *testing.T) {
	p, _, lease := leaseFixture()
	task := lease.Task
	it := domain.ContextItem{SessionID: p.SessionID, WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID, TurnID: task.TurnID, CreatedTurn: task.Turn}
	for _, scope := range []domain.Scope{domain.ScopeTurn, domain.ScopeTask, domain.ScopeWorkflow, domain.ScopeAgent, domain.ScopeSession} {
		t.Run(string(scope), func(t *testing.T) {
			it.Scope, it.Access = scope, domain.BoundaryFor(scope, p)
			s := OwnerSnapshot{Seq: 5, Task: &task}
			kind, id := domain.OwnerWorkflow, p.WorkflowID
			if scope == domain.ScopeAgent {
				kind, id = domain.OwnerAgent, p.AgentID
			}
			if scope == domain.ScopeWorkflow || scope == domain.ScopeAgent {
				if ScopeLifetime(it, s) != domain.ExpiryUnknown {
					t.Fatal("unregistered owner treated as live")
				}
				s.Owner = &domain.OwnerRegistration{SemanticMeta: domain.SemanticMeta{ID: "owner", SessionID: p.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: 1}, Kind: kind, OwnerID: id, SourceID: "source", Actor: domain.Principal{SessionID: p.SessionID, Authority: domain.AuthorityHarness}}
			}
			if ScopeLifetime(it, s) != domain.ExpiryLive {
				t.Fatal("registered active owner denied")
			}
			completed := task
			completed.Status, completed.CompletedSeq = domain.TaskCompleted, 5
			s.Task = &completed
			want := domain.ExpiryLive
			if scope == domain.ScopeTurn || scope == domain.ScopeTask {
				want = domain.ExpiryExpired
			}
			if got := ScopeLifetime(it, s); got != want {
				t.Fatalf("completed origin: %s, want %s", got, want)
			}
			if s.Owner != nil {
				s.Owner.OwnerID = "unrelated"
				if ScopeLifetime(it, s) != domain.ExpiryUnknown {
					t.Fatal("unrelated registration accepted")
				}
				s.Owner.OwnerID, s.Owner.Seq = id, 6
				if ScopeLifetime(it, s) != domain.ExpiryUnknown {
					t.Fatal("future registration accepted")
				}
			}
		})
	}
}

func TestTurnScopeNeedsRecordedActiveTurn(t *testing.T) {
	p, _, lease := leaseFixture()
	it := domain.ContextItem{SessionID: "s", WorkflowID: "w", TaskID: "t", Scope: domain.ScopeTurn, Access: domain.BoundaryFor(domain.ScopeTurn, p), TurnID: "turn", CreatedTurn: 1}
	s := OwnerSnapshot{Seq: 5, Task: &lease.Task}
	if ScopeLifetime(it, OwnerSnapshot{}) != domain.ExpiryUnknown {
		t.Fatal("missing task accepted")
	}
	s.Task.Turn, s.Task.TurnID = 2, "next"
	if ScopeLifetime(it, s) != domain.ExpiryExpired {
		t.Fatal("old turn stayed active")
	}
	it.CreatedTurn = 0
	if ScopeLifetime(it, s) != domain.ExpiryUnknown {
		t.Fatal("unknown origin inferred")
	}
}
