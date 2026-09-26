package domain

// Logical membership is authenticated receipt/admission, not visibility (P3-7).
type ExchangeState string

const (
	ExchangeOpen      ExchangeState = "OPEN"
	ExchangeExecuting ExchangeState = "EXECUTING"
	ExchangeClosed    ExchangeState = "CLOSED"
	ExchangeCancelled ExchangeState = "CANCELLED"
)

type LogicalExchange struct {
	SemanticMeta
	ConversationID   string
	Ordinal          uint64
	Principal        Principal
	TurnID           string
	Turn             uint64
	State            ExchangeState
	AcknowledgmentID string
	Revision         uint64
}

func (x LogicalExchange) Validate() error {
	if err := x.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := semanticActor(x.SessionID, x.Principal); err != nil {
		return err
	}
	if x.Principal.TaskID == "" || x.Principal.AgentID == "" || x.ConversationID != ConversationIDFor(x.Principal.TaskID, x.Principal.AgentID) || x.Ordinal == 0 || x.Turn == 0 || !semanticID(x.TurnID) || x.Revision == 0 {
		return invalid("exchange: invalid conversation, turn, or order")
	}
	switch x.State {
	case ExchangeOpen, ExchangeExecuting:
		if x.AcknowledgmentID != "" {
			return invalid("exchange: open exchange has acknowledgment")
		}
	case ExchangeClosed, ExchangeCancelled:
		if !semanticID(x.AcknowledgmentID) {
			return invalid("exchange: closure requires acknowledgment")
		}
	default:
		return invalid("exchange: unknown state")
	}
	return nil
}

type ExchangeMemberRole string

const (
	MemberInput      ExchangeMemberRole = "INPUT"
	MemberOutput     ExchangeMemberRole = "OUTPUT"
	MemberToolCall   ExchangeMemberRole = "TOOL_CALL"
	MemberToolResult ExchangeMemberRole = "TOOL_RESULT"
)

type ExchangeMember struct {
	SemanticMeta
	ExchangeID                      string
	Position                        uint64
	Role                            ExchangeMemberRole
	Source                          ItemContentRef
	CallID, ToolCallID, AdmissionID string
}

func (m ExchangeMember) Validate() error {
	if err := m.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !semanticID(m.ExchangeID) || m.Position == 0 {
		return invalid("exchange member: exchange and position required")
	}
	if err := m.Source.Validate(); err != nil {
		return err
	}
	switch m.Role {
	case MemberInput:
	case MemberOutput:
		if !semanticID(m.CallID) {
			return invalid("exchange output: call required")
		}
	case MemberToolCall, MemberToolResult:
		if !semanticID(m.CallID) || !semanticID(m.ToolCallID) {
			return invalid("exchange tool member: originating call required")
		}
	default:
		return invalid("exchange member: unknown role")
	}
	return nil
}

// Acknowledgment is a trusted durable fact; model arguments cannot create it.
type ExchangeAcknowledgment struct {
	SemanticMeta
	ExchangeID, ManifestID string
	ConsumingCallID        string
	CancellationReason     ExchangeCancellationReason
	Actor                  Principal
	Cancelled              bool
}

func (a ExchangeAcknowledgment) Validate() error {
	if err := a.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !semanticID(a.ExchangeID) {
		return invalid("acknowledgment: exchange and manifest required")
	}
	if a.Cancelled {
		if a.ManifestID != "" || a.ConsumingCallID != "" || a.CancellationReason != ExchangeAbandoned && a.CancellationReason != ExchangeExplicitCancellation {
			return invalid("cancellation: cannot fabricate successful admission")
		}
	} else if !semanticID(a.ManifestID) || !semanticID(a.ConsumingCallID) || a.CancellationReason != "" {
		return invalid("acknowledgment: completed consuming inference required")
	}
	if err := semanticActor(a.SessionID, a.Actor); err != nil {
		return err
	}
	if a.Actor.Authority != AuthoritySystem && a.Actor.Authority != AuthorityHarness {
		return ErrInvalidAuthorityPromotion
	}
	return nil
}
