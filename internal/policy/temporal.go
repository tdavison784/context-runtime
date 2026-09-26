package policy

import "github.com/tdavison784/context-runtime/internal/domain"

// EligibilityReason is a closed, content-free policy explanation.
type EligibilityReason string

const (
	ReasonAllowed       EligibilityReason = "ALLOWED"
	ReasonUnknownOwner  EligibilityReason = "UNKNOWN_OWNER"
	ReasonInactiveOwner EligibilityReason = "INACTIVE_OWNER"
	ReasonExpiredTurn   EligibilityReason = "EXPIRED_TURN"
	ReasonUnknownTurn   EligibilityReason = "UNKNOWN_TURN"
	ReasonExpiredTTL    EligibilityReason = "EXPIRED_TTL"
)

// OrdinaryLifetime evaluates scope and TTL only. Access and a historical lease
// are independent checks; callers must not turn a lease into an ordinary life.
func OrdinaryLifetime(it domain.ContextItem, s OwnerSnapshot, p domain.Principal, dispatchTurn string) (bool, EligibilityReason) {
	switch ScopeLifetime(it, s) {
	case domain.ExpiryUnknown:
		return false, ReasonUnknownOwner
	case domain.ExpiryExpired:
		if it.Scope == domain.ScopeTurn && s.Task.Status == domain.TaskActive {
			return false, ReasonExpiredTurn
		}
		return false, ReasonInactiveOwner
	}
	if it.Scope == domain.ScopeTurn && (p.TaskID != it.TaskID || dispatchTurn != it.TurnID) {
		return false, ReasonExpiredTurn
	}
	if it.TTLTurns != nil {
		t := s.Task
		if t == nil || t.Validate() != nil || t.SessionID != it.SessionID || t.TaskID != it.TaskID || t.WorkflowID != it.WorkflowID ||
			t.CompletedSeq > s.Seq || it.CreatedTurn == 0 || t.Turn == 0 || t.TurnID == "" || t.Turn < it.CreatedTurn {
			return false, ReasonUnknownTurn
		}
		if t.Status != domain.TaskActive || p.SessionID != it.SessionID || p.TaskID != it.TaskID || dispatchTurn != t.TurnID ||
			!domain.TTLLive(it.CreatedTurn, t.Turn, *it.TTLTurns) {
			return false, ReasonExpiredTTL
		}
	}
	return true, ReasonAllowed
}
