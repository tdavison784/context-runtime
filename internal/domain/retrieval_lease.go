package domain

// RetrievalLease binds immutable content, not mutable item lifecycle Version.
// New requests may create new leases; they never extend this record (P3-29).
type RetrievalLease struct {
	SemanticMeta
	Holder                                       Principal
	ConversationID, TurnID                       string
	Source                                       ItemContentRef
	IssuedCompletedInferenceIndex, CallAllowance uint64
	PolicyVersion                                string
}

func (l RetrievalLease) Validate() error {
	if err := l.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := semanticActor(l.SessionID, l.Holder); err != nil {
		return err
	}
	if l.Holder.TaskID == "" || l.Holder.AgentID == "" || l.ConversationID != ConversationIDFor(l.Holder.TaskID, l.Holder.AgentID) || !semanticID(l.TurnID) || !semanticID(l.PolicyVersion) || l.CallAllowance == 0 {
		return invalid("lease: holder, current turn, and finite allowance required")
	}
	return l.Source.Validate()
}

// Lease liveness is owned by the single pure internal/policy predicate (W3).

type ExpiryState string

const (
	ExpiryLive    ExpiryState = "LIVE"
	ExpiryExpired ExpiryState = "EXPIRED"
	ExpiryUnknown ExpiryState = "UNKNOWN"
)

// ObservedItemState is a frozen read result, never a replacement item row.
type ObservedItemState struct {
	Source      ItemContentRef
	Version     uint64
	Currentness ItemCurrentness
	GoalStatus  *GoalStatus
	Generation  Generation
	Residency   Residency
	Authority   Authority
	Expiry      ExpiryState
}

func (s ObservedItemState) Clone() ObservedItemState {
	if s.GoalStatus != nil {
		v := *s.GoalStatus
		s.GoalStatus = &v
	}
	return s
}
func (s ObservedItemState) Validate() error {
	if err := s.Source.Validate(); err != nil {
		return err
	}
	if s.Version == 0 || !s.Currentness.Valid() || !s.Generation.Valid() || !s.Residency.Valid() || !s.Authority.Valid() || s.GoalStatus != nil && !s.GoalStatus.Valid() {
		return invalid("observed item: invalid lifecycle snapshot")
	}
	if s.Expiry != ExpiryLive && s.Expiry != ExpiryExpired && s.Expiry != ExpiryUnknown {
		return invalid("observed item: explicit expiry required")
	}
	return nil
}

type GetResult struct {
	Item        ContextItem
	Observed    ObservedItemState
	SnapshotSeq uint64
}

func (r GetResult) Clone() GetResult {
	r.Item = r.Item.Clone()
	r.Observed = r.Observed.Clone()
	return r
}

type RehydrateIntent struct {
	RequestID, ItemID string
	CallAllowance     uint64
}

func (i RehydrateIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.ItemID) {
		return invalid("retrieval intent: request and occurrence required")
	}
	return nil
}
