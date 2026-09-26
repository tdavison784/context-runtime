package policy

import "github.com/tdavison784/context-runtime/internal/domain"

// LeaseSnapshot contains facts from one store snapshot. Source identifies the
// content being admitted; lifecycle/usage revisions intentionally are absent.
type LeaseSnapshot struct {
	Seq          uint64
	Source       domain.ItemContentRef
	Task         domain.TaskState
	Conversation domain.Conversation
}

// LeaseLive is the single P3-29/31 liveness predicate. It does not grant access;
// callers must separately check the source boundary. No current policy change
// can renew or shorten an already issued lease's recorded allowance.
func LeaseLive(l domain.RetrievalLease, s LeaseSnapshot, p domain.Principal, dispatchTurn string) bool {
	if l.Validate() != nil || l.Holder != p || l.Seq > s.Seq || l.Source != s.Source || l.TurnID != dispatchTurn {
		return false
	}
	t, c := s.Task, s.Conversation
	if t.Validate() != nil || t.SessionID != p.SessionID || t.TaskID != p.TaskID || t.WorkflowID != p.WorkflowID ||
		t.Status != domain.TaskActive || t.Turn == 0 || t.TurnID != dispatchTurn {
		return false
	}
	if c.Validate() != nil || c.SessionID != p.SessionID || c.TaskID != p.TaskID || c.AgentID != p.AgentID || c.ConversationID != l.ConversationID {
		return false
	}
	return c.LogicalCalls >= l.IssuedCompletedInferenceIndex && c.LogicalCalls-l.IssuedCompletedInferenceIndex < l.CallAllowance
}
