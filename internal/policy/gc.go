package policy

import "github.com/tdavison784/context-runtime/internal/domain"

// GCReason is a closed, content-free explanation of one collection decision.
type GCReason string

const (
	GCReasonStale            GCReason = "STALE_VERSION"
	GCReasonExpiredScope     GCReason = "EXPIRED_SCOPE"
	GCReasonExpiredTTL       GCReason = "EXPIRED_TTL"
	GCReasonEndedTurn        GCReason = "ENDED_TURN_EPHEMERAL"
	GCReasonLive             GCReason = "LIVE"
	GCReasonUnknown          GCReason = "UNKNOWN_LIFETIME"
	GCReasonNotResident      GCReason = "NOT_RESIDENT"
	GCReasonRequirement      GCReason = "CURRENT_REQUIREMENT"
	GCReasonActiveTurn       GCReason = "ACTIVE_TURN"
	GCReasonOpenExchange     GCReason = "OPEN_EXCHANGE"
	GCReasonLiveLease        GCReason = "LIVE_LEASE"
	GCReasonActiveCheckpoint GCReason = "ACTIVE_CHECKPOINT"
)

// GCSnapshot holds facts from the collection's single store snapshot. Every
// boolean must be derived from a complete bounded read; a builder that cannot
// establish one reports it as true (protected) or aborts, never false.
type GCSnapshot struct {
	OwnerSnapshot
	Item                 domain.ItemRevisionRef
	Currentness          domain.ItemCurrentness
	ObligationsKnown     bool
	OpenObligationSource bool // a current UNRESOLVED/BLOCKED obligation names the item
	OpenExchange         bool // member of an open, executing or unacknowledged exchange
	LiveLease            bool // some holder's lease on this exact content may be live
	// NewestCheckpoint: a CHECKPOINT item is its conversation's newest
	// checkpoint, or that cannot be established.
	NewestCheckpoint bool
}

// CollectDecision is the pure gc/v1 rule (P3-38). It never consults
// LastUsedCall, wall clock or size pressure, and unknown lifetime never
// justifies archival. Authorization of an ARCHIVE decision is separate.
func CollectDecision(it domain.ContextItem, s GCSnapshot) (domain.GCDecisionCode, GCReason, error) {
	if it.Validate() != nil || s.Seq < it.Seq || s.Item.ItemID != it.ID || s.Item.Version != it.Version || !s.Currentness.Valid() || !s.ObligationsKnown ||
		it.DirectiveID != "" && s.Currentness == domain.ItemUnkeyed {
		return "", "", domain.ErrInvalidRecord
	}
	if it.Residency != domain.ResidencyResident {
		return domain.GCIneligible, GCReasonNotResident, nil
	}
	lifetime := ScopeLifetime(it, s.OwnerSnapshot)
	task := knownTask(it, s.OwnerSnapshot)
	// Protection first: the reasons below outrank every collectible reason.
	switch {
	case s.OpenExchange:
		return domain.GCProtected, GCReasonOpenExchange, nil
	case s.LiveLease:
		return domain.GCProtected, GCReasonLiveLease, nil
	case task != nil && task.Status == domain.TaskActive && it.TurnID != "" && it.TurnID == task.TurnID:
		return domain.GCProtected, GCReasonActiveTurn, nil
	case it.Role == domain.RoleCheckpoint && s.NewestCheckpoint && (task == nil || task.Status == domain.TaskActive):
		// Only the newest checkpoint of an active (or unknown) conversation is
		// relevant; older and completed-conversation checkpoints are not kept
		// forever by kind (C-17).
		return domain.GCProtected, GCReasonActiveCheckpoint, nil
	case lifetime == domain.ExpiryLive && s.OpenObligationSource:
		return domain.GCProtected, GCReasonRequirement, nil
	case lifetime == domain.ExpiryLive && current(s.Currentness) && requirement(it, ttlExpired(it, task)):
		return domain.GCProtected, GCReasonRequirement, nil
	}
	switch {
	case s.Currentness == domain.ItemHistorical || s.Currentness == domain.ItemDuplicate:
		return domain.GCArchive, GCReasonStale, nil
	case lifetime == domain.ExpiryExpired:
		return domain.GCArchive, GCReasonExpiredScope, nil
	case lifetime == domain.ExpiryUnknown:
		return domain.GCIneligible, GCReasonUnknown, nil
	case ttlExpired(it, task):
		return domain.GCArchive, GCReasonExpiredTTL, nil
	case it.Generation == domain.GenerationEphemeral && it.TurnID != "" && task != nil && (task.Status != domain.TaskActive || task.TurnID != it.TurnID):
		return domain.GCArchive, GCReasonEndedTurn, nil
	}
	return domain.GCIneligible, GCReasonLive, nil
}

// MayArchive decides from item-local and lifetime facts alone, treating
// every protection fact as absent. Protection facts only turn ARCHIVE into
// PROTECTED, so a non-ARCHIVE answer here is final and callers may skip the
// protection reads; an ARCHIVE answer needs the full CollectDecision.
func MayArchive(it domain.ContextItem, s GCSnapshot) (domain.GCDecisionCode, GCReason, error) {
	s.ObligationsKnown = true
	s.OpenObligationSource, s.OpenExchange, s.LiveLease, s.NewestCheckpoint = false, false, false, false
	return CollectDecision(it, s)
}

func current(c domain.ItemCurrentness) bool {
	return c == domain.ItemCurrent || c == domain.ItemUnkeyed
}

// requirement is a current pin, OPEN goal or eligible SYSTEM
// instruction/constraint: an expired TTL ends only the last (SPEC-1.15).
func requirement(it domain.ContextItem, expired bool) bool {
	return it.Generation == domain.GenerationPinned || it.Kind == domain.KindGoal && it.GoalStatus != nil && *it.GoalStatus == domain.GoalOpen ||
		!expired && it.Authority == domain.AuthoritySystem && (it.Kind == domain.KindInstruction || it.Kind == domain.KindConstraint)
}

// ttlExpired reports a TTL the snapshot proves expired against the known
// originating task; unknown TTL state is not expiry.
func ttlExpired(it domain.ContextItem, task *domain.TaskState) bool {
	return it.TTLTurns != nil && task != nil && task.WorkflowID == it.WorkflowID && it.CreatedTurn != 0 && task.Turn >= it.CreatedTurn &&
		(task.Status != domain.TaskActive || !domain.TTLLive(it.CreatedTurn, task.Turn, *it.TTLTurns))
}

// knownTask is the item's originating task when the snapshot establishes it.
func knownTask(it domain.ContextItem, s OwnerSnapshot) *domain.TaskState {
	t := s.Task
	if t == nil || it.TaskID == "" || t.Validate() != nil || t.SessionID != it.SessionID || t.TaskID != it.TaskID || t.CompletedSeq > s.Seq || t.Turn == 0 || t.TurnID == "" {
		return nil
	}
	return t
}
