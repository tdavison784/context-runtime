package domain

// RegisterExchangeMemberIntent names requested membership, never authenticated
// actor, session, sequence or persisted row identity. W5 derives those in tx and
// checks the exact exchange revision, source, admission and completed call.
type RegisterExchangeMemberIntent struct {
	RequestID, ExchangeID           string
	ExpectedRevision, Position      uint64
	Role                            ExchangeMemberRole
	Source                          ItemContentRef
	CallID, ToolCallID, AdmissionID string
}

func (i RegisterExchangeMemberIntent) Clone() RegisterExchangeMemberIntent { return i }

func (i RegisterExchangeMemberIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.ExchangeID) || i.ExpectedRevision == 0 || i.Position == 0 {
		return invalid("exchange member intent: request, exchange, revision and position required")
	}
	if err := i.Source.Validate(); err != nil {
		return err
	}
	for _, id := range []string{i.CallID, i.ToolCallID, i.AdmissionID} {
		if id != "" && !semanticID(id) {
			return invalid("exchange member intent: invalid optional reference")
		}
	}
	switch i.Role {
	case MemberInput:
	case MemberOutput:
		if !semanticID(i.CallID) {
			return invalid("exchange output intent: call required")
		}
	case MemberToolCall, MemberToolResult:
		if !semanticID(i.CallID) || !semanticID(i.ToolCallID) {
			return invalid("exchange tool member intent: originating call required")
		}
	default:
		return invalid("exchange member intent: unknown role")
	}
	return nil
}
