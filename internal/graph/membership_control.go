package graph

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Trusted membership control uses the ledger's exact owner-match floor. A
// session-level harness is not a wildcard for another task or private agent.
func checkMembershipControl(session string, actor, recipient domain.Principal) error {
	if actor.Validate() != nil || recipient.Validate() != nil ||
		actor.Authority != domain.AuthoritySystem && actor.Authority != domain.AuthorityHarness ||
		actor.SessionID != session || recipient.SessionID != session ||
		recipient.TaskID == "" || recipient.AgentID == "" ||
		actor.WorkflowID != recipient.WorkflowID || actor.TaskID != recipient.TaskID || actor.AgentID != recipient.AgentID {
		return domain.ErrInvalidAuthorityPromotion
	}
	return nil
}

// New membership cannot relabel a late output with the latest task turn.
// Closure/cancellation use the recorded origin instead and do not call this.
func checkMembershipTurn(tx store.ReadTx, p domain.Principal, turnID string, turn uint64) (domain.TaskState, error) {
	task, err := tx.Task(p.TaskID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			err = domain.ErrNotFound
		}
		return domain.TaskState{}, err
	}
	if p.SessionID != tx.SessionID() || task.SessionID != p.SessionID || task.WorkflowID != p.WorkflowID {
		return domain.TaskState{}, domain.ErrNotFound
	}
	if task.Status != domain.TaskActive || turn == 0 || turnID == "" || task.Turn != turn || task.TurnID != turnID {
		return domain.TaskState{}, domain.ErrInvalidTransition
	}
	return task, nil
}
