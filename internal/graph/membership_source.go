package graph

import "github.com/tdavison784/context-runtime/internal/domain"

func checkMemberSource(x domain.LogicalExchange, intent domain.RegisterExchangeMemberIntent, source domain.ContextItem, call *domain.CallRecord) error {
	if source.SessionID != x.SessionID || !source.Access.Permits(x.Principal) {
		return domain.ErrNotFound
	}
	if source.ID != intent.Source.ItemID || source.ContentHash != intent.Source.ContentHash || source.ContentHash != domain.ContentHash(source.Parts) {
		return domain.ErrIntegrity
	}
	if intent.Role == domain.MemberInput {
		return nil // Reading earlier context does not change its source origin.
	}
	if source.WorkflowID != x.Principal.WorkflowID || source.TaskID != x.Principal.TaskID || source.AgentID != x.Principal.AgentID || source.TurnID != x.TurnID || source.CreatedTurn != x.Turn {
		return domain.ErrInvalidTransition
	}
	if (intent.Role == domain.MemberOutput || intent.Role == domain.MemberToolCall) && source.Authority != domain.AuthorityAgent {
		return domain.ErrInvalidAuthorityPromotion
	}
	if call == nil || call.Validate() != nil || call.CallID != intent.CallID || call.SessionID != x.SessionID || call.ConversationID != x.ConversationID || call.Principal != x.Principal || call.Operation != domain.OperationInference || call.State != domain.CallCompleted || checkMembershipControl(x.SessionID, call.ServiceActor, x.Principal) != nil {
		return domain.ErrInvalidTransition
	}
	return nil
}
