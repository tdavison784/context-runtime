package retrieve

import "github.com/tdavison784/context-runtime/internal/domain"

// AdmissionIntent binds an authenticated request to its exact holder and
// route. Method is part of the immutable request identity (P3-2/28).
type AdmissionIntent struct {
	Rehydrate domain.RehydrateIntent
	Origin    domain.RetrievalOrigin
	Method    string // rehydrate, context_get, or context_rehydrate
}

func validateAdmission(actor domain.Principal, i AdmissionIntent, task domain.TaskState, conv domain.Conversation, policy domain.Phase3Policy) (uint64, error) {
	if err := validateDenialOrigin(actor, i); err != nil {
		return 0, err
	}
	if err := policy.Validate(); err != nil {
		return 0, err
	}
	if task.Validate() != nil || conv.Validate() != nil || task.SessionID != actor.SessionID ||
		task.TaskID != actor.TaskID || task.WorkflowID != actor.WorkflowID ||
		conv.SessionID != actor.SessionID || conv.ConversationID != i.Origin.ConversationID ||
		conv.TaskID != actor.TaskID || conv.AgentID != actor.AgentID {
		return 0, domain.ErrInvalidRecord
	}
	if task.Status != domain.TaskActive || task.Turn == 0 || task.TurnID != i.Origin.TurnID {
		return 0, domain.ErrLeaseExpired
	}
	allowance := i.Rehydrate.CallAllowance
	if allowance == 0 {
		allowance = policy.DefaultLeaseCalls
	}
	if allowance == 0 || allowance > policy.MaxLeaseCalls {
		return 0, domain.ErrResourceLimit
	}
	return allowance, nil
}
