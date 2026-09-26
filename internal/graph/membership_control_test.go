package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestMembershipControlRequiresExactTrustedOwnership(t *testing.T) {
	p := domain.Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", AgentID: "a", Authority: domain.AuthorityAgent}
	actor := p
	actor.Authority = domain.AuthorityHarness
	for _, authority := range []domain.Authority{domain.AuthoritySystem, domain.AuthorityHarness} {
		actor.Authority = authority
		if err := checkMembershipControl("s", actor, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, mutate := range []func(*domain.Principal){
		func(a *domain.Principal) { a.Authority = domain.AuthorityAgent },
		func(a *domain.Principal) { a.Authority = domain.AuthorityUser },
		func(a *domain.Principal) { a.Authority = domain.AuthorityTool },
		func(a *domain.Principal) { a.Authority = domain.AuthorityRetrievedContent },
		func(a *domain.Principal) { a.SessionID = "other" },
		func(a *domain.Principal) { a.WorkflowID = "" },
		func(a *domain.Principal) { a.TaskID = "" },
		func(a *domain.Principal) { a.AgentID = "b" },
	} {
		bad := actor
		mutate(&bad)
		if err := checkMembershipControl("s", bad, p); err != domain.ErrInvalidAuthorityPromotion {
			t.Fatalf("actor %+v: %v", bad, err)
		}
	}
}

func TestMembershipTurnUsesPersistedTaskOwnership(t *testing.T) {
	s := memory.New()
	defer s.Close()
	p := storetest.NewPrincipal("s", domain.AuthorityAgent)
	actor := p
	actor.Authority = domain.AuthorityHarness
	var task domain.TaskState
	update(t, s, "s", func(tx store.Tx) error {
		task = storetest.NewTask("s", p.TaskID)
		task.WorkflowID, task.TurnID, task.Turn = p.WorkflowID, "turn-2", 2
		ev := domain.LifecycleEvent{ID: "task-open", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: p.TaskID, Action: "open", Actor: actor}
		var err error
		task, err = tx.PutTask(task, 0, ev)
		return err
	})
	view(t, s, "s", func(tx store.ReadTx) error {
		if got, err := checkMembershipTurn(tx, p, "turn-2", 2); err != nil || got != task {
			t.Fatalf("current turn: %+v, %v", got, err)
		}
		for _, turn := range []uint64{0, 1, 3} {
			if _, err := checkMembershipTurn(tx, p, "turn-2", turn); !errors.Is(err, domain.ErrInvalidTransition) {
				t.Fatalf("turn %d: %v", turn, err)
			}
		}
		other := p
		other.WorkflowID = "other"
		if _, err := checkMembershipTurn(tx, other, "turn-2", 2); err != domain.ErrNotFound {
			t.Fatal("workflow mismatch exposed task", err)
		}
		other.TaskID = "missing"
		if _, err := checkMembershipTurn(tx, other, "turn-2", 2); err != domain.ErrNotFound {
			t.Fatal("missing task error differs", err)
		}
		if _, err := checkMembershipTurn(tx, p, "old-turn", 2); err != domain.ErrInvalidTransition {
			t.Fatal("old turn relabeled", err)
		}
		return nil
	})
}
