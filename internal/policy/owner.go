package policy

import "github.com/tdavison784/context-runtime/internal/domain"

// OwnerSnapshot supplies the item's originating task and declared broad-scope
// registration at Seq. Missing records stay nil; no child-task census is used.
type OwnerSnapshot struct {
	Seq   uint64
	Task  *domain.TaskState
	Owner *domain.OwnerRegistration
}

// ScopeLifetime is independent of access, TTL, residency, and currentness.
// Protection uses the declared owner's lifetime, not the originating TaskID
// alone. Unknown means neither admission nor automatic archival is justified.
func ScopeLifetime(it domain.ContextItem, s OwnerSnapshot) domain.ExpiryState {
	if s.Seq == 0 || it.Access.Validate() != nil || it.Access.SessionID != it.SessionID || it.Access.Scope != it.Scope {
		return domain.ExpiryUnknown
	}
	switch it.Scope {
	case domain.ScopeSession:
		return domain.ExpiryLive
	case domain.ScopeTask, domain.ScopeTurn:
		t := s.Task
		if t == nil || t.Validate() != nil || t.SessionID != it.SessionID || t.TaskID != it.TaskID || t.WorkflowID != it.WorkflowID || it.Access.TaskID != it.TaskID || t.CompletedSeq > s.Seq {
			return domain.ExpiryUnknown
		}
		if it.Scope == domain.ScopeTurn && (it.CreatedTurn == 0 || it.TurnID == "" || t.Turn == 0 || t.TurnID == "" || t.Turn < it.CreatedTurn) {
			return domain.ExpiryUnknown
		}
		if t.Status == domain.TaskCompleted || it.Scope == domain.ScopeTurn && (t.Turn != it.CreatedTurn || t.TurnID != it.TurnID) {
			return domain.ExpiryExpired
		}
		return domain.ExpiryLive
	case domain.ScopeWorkflow, domain.ScopeAgent:
		kind, id := domain.OwnerWorkflow, it.Access.WorkflowID
		if it.Scope == domain.ScopeAgent {
			kind, id = domain.OwnerAgent, it.Access.AgentID
		}
		o := s.Owner
		if o == nil || o.Validate() != nil || o.SessionID != it.SessionID || o.Kind != kind || o.OwnerID != id || o.Seq > s.Seq {
			return domain.ExpiryUnknown
		}
		return domain.ExpiryLive
	}
	return domain.ExpiryUnknown
}
