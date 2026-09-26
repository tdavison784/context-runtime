package tools

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

type invocationState struct {
	exchange     domain.LogicalExchange
	task         domain.TaskState
	output       domain.ItemContentRef
	nextPosition uint64
}

func privateReadError(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrNotFound
	}
	return err
}

func readInvocation(tx store.ReadTx, sem store.SemanticReader, i domain.ToolInvocation, policy domain.Phase3Policy) (state invocationState, err error) {
	if i.Validate() != nil || i.SessionID != tx.SessionID() {
		return state, domain.ErrNotFound
	}
	if policy.MaxTransactionWork <= 5 {
		return state, domain.ErrResourceLimit
	}
	x, err := sem.LogicalExchange(i.ExchangeID)
	if err != nil {
		return state, privateReadError(err)
	}
	call, err := tx.Call(i.CallID)
	if err != nil {
		return state, privateReadError(err)
	}
	task, err := tx.Task(i.Principal.TaskID)
	if err != nil {
		return state, privateReadError(err)
	}
	if err = checkInvocationRecords(i, x, call, task); err != nil {
		return state, err
	}
	members, err := completePages(policy.MaxPageSize, min(policy.MaxCoverageMembers, policy.MaxTransactionWork-5), func(page store.Page) (store.ResultPage[domain.ExchangeMember], error) {
		return sem.ExchangeMembers(x.ID, page)
	})
	if err != nil {
		return state, err
	}
	var output, tool *domain.ExchangeMember
	positions := make(map[uint64]bool, len(members))
	for n := range members {
		m := &members[n]
		if m.Validate() != nil || m.ExchangeID != x.ID || m.SessionID != i.SessionID || m.Position > uint64(len(members)) || positions[m.Position] {
			return state, domain.ErrIncompleteCoverage
		}
		positions[m.Position] = true
		if m.Role == domain.MemberOutput {
			if output != nil || m.CallID != i.CallID {
				return state, domain.ErrIncompleteCoverage
			}
			output = m
		}
		if m.CallID == i.CallID && m.ToolCallID == i.ToolCallID {
			if m.Role == domain.MemberToolResult {
				return state, domain.ErrInvalidTransition
			}
			if m.Role == domain.MemberToolCall {
				if tool != nil {
					return state, domain.ErrIncompleteCoverage
				}
				tool = m
			}
		}
	}
	if output == nil || tool == nil || output.Source != tool.Source {
		return state, domain.ErrIncompleteCoverage
	}
	source, err := tx.Item(output.Source.ItemID)
	if err != nil {
		return state, privateReadError(err)
	}
	p := i.Principal
	if !source.Access.Permits(p) || source.SessionID != p.SessionID || source.WorkflowID != p.WorkflowID || source.TaskID != p.TaskID || source.AgentID != p.AgentID {
		return state, domain.ErrNotFound
	}
	if source.ValidateSemantic() != nil || source.Authority != domain.AuthorityAgent || source.TurnID != i.TurnID || source.CreatedTurn != x.Turn || source.ContentHash != output.Source.ContentHash || domain.ContentHash(source.Parts) != source.ContentHash {
		return state, domain.ErrIntegrity
	}
	return invocationState{exchange: x, task: task, output: output.Source, nextPosition: uint64(len(members)) + 1}, nil
}
