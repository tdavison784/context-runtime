package tools

import "github.com/tdavison784/context-runtime/internal/domain"

func checkInvocationRecords(i domain.ToolInvocation, x domain.LogicalExchange, call domain.CallRecord, task domain.TaskState) error {
	p := i.Principal
	if i.Validate() != nil || x.SessionID != i.SessionID || x.ID != i.ExchangeID || x.ConversationID != i.ConversationID || x.Principal != p || call.SessionID != i.SessionID || call.CallID != i.CallID || call.ConversationID != i.ConversationID || call.Principal != p || task.SessionID != p.SessionID || task.TaskID != p.TaskID || task.WorkflowID != p.WorkflowID {
		return domain.ErrNotFound
	}
	a := call.ServiceActor
	if a.Validate() != nil || a.SessionID != p.SessionID || a.WorkflowID != p.WorkflowID || a.TaskID != p.TaskID || a.AgentID != p.AgentID || a.Authority != domain.AuthorityHarness && a.Authority != domain.AuthoritySystem {
		return domain.ErrNotFound
	}
	if x.Validate() != nil || call.Validate() != nil || task.Validate() != nil {
		return domain.ErrIntegrity
	}
	if call.Operation != domain.OperationInference || call.State != domain.CallCompleted || x.State != domain.ExchangeExecuting || task.Status != domain.TaskActive || task.Turn == 0 || task.Turn != x.Turn || task.TurnID != i.TurnID || x.TurnID != i.TurnID {
		return domain.ErrInvalidTransition
	}
	return nil
}
