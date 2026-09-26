package policy

import (
	"math"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestOrdinaryLifetimeTTLEdges(t *testing.T) {
	p, _, lease := leaseFixture()
	n := 2
	it := domain.ContextItem{SessionID: "s", WorkflowID: "w", TaskID: "t", Scope: domain.ScopeSession, Access: domain.BoundaryFor(domain.ScopeSession, p), CreatedTurn: 1, TTLTurns: &n}
	s := OwnerSnapshot{Seq: 5, Task: &lease.Task}
	for _, current := range []uint64{0, 1, 2, 3, math.MaxUint64} {
		s.Task.Turn = current
		live, _ := OrdinaryLifetime(it, s, p, "turn")
		if live != (current == 1 || current == 2) {
			t.Fatalf("current=%d live=%v", current, live)
		}
	}
	it.CreatedTurn, s.Task.Turn = math.MaxUint64-1, math.MaxUint64
	if live, _ := OrdinaryLifetime(it, s, p, "turn"); !live {
		t.Fatal("TTL overflowed")
	}
	it.CreatedTurn = 0
	if live, _ := OrdinaryLifetime(it, s, p, "turn"); live {
		t.Fatal("unknown creation turn admitted")
	}
}

func TestOrdinaryLifetimeNeverBorrowsAnotherTurnSource(t *testing.T) {
	p, _, lease := leaseFixture()
	n := 10
	it := domain.ContextItem{SessionID: "s", WorkflowID: "w", TaskID: "t", Scope: domain.ScopeSession, Access: domain.BoundaryFor(domain.ScopeSession, p), CreatedTurn: 1, TTLTurns: &n}
	s := OwnerSnapshot{Seq: 5, Task: &lease.Task}
	other := p
	other.TaskID = "other"
	if live, _ := OrdinaryLifetime(it, s, other, "turn"); live {
		t.Fatal("foreign turn counter borrowed")
	}
	if live, _ := OrdinaryLifetime(it, s, p, "old"); live {
		t.Fatal("stale dispatch turn admitted")
	}
	s.Task.Status, s.Task.CompletedSeq = domain.TaskCompleted, 5
	if live, _ := OrdinaryLifetime(it, s, p, "turn"); live {
		t.Fatal("terminal origin froze live TTL")
	}
	it.TTLTurns = nil
	if live, _ := OrdinaryLifetime(it, s, p, "turn"); !live {
		t.Fatal("session lifetime ended with origin task")
	}
	it.Scope, it.Access, it.TurnID, it.TTLTurns = domain.ScopeTurn, domain.BoundaryFor(domain.ScopeTurn, p), "turn", &n
	s.Task.Status, s.Task.CompletedSeq, s.Task.Turn, s.Task.TurnID = domain.TaskActive, 0, 2, "next"
	if live, _ := OrdinaryLifetime(it, s, p, "next"); live {
		t.Fatal("long TTL extended old TURN")
	}
}
