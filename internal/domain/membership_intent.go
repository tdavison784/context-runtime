package domain

type RegisterExchangeIntent struct {
	RequestID                        string
	Principal                        Principal // authenticated recipient, validated by trusted registrar
	TurnID                           string
	Turn, ExpectedMembershipRevision uint64
}

func (i RegisterExchangeIntent) Validate() error {
	if err := validateIngestPrincipal(i.Principal); err != nil {
		return err
	}
	if !semanticID(i.RequestID) || i.Principal.TaskID == "" || i.Principal.AgentID == "" || !semanticID(i.TurnID) || i.Turn == 0 {
		return invalid("exchange registration: exact originating principal/turn required")
	}
	return nil
}

type AdmitExchangeIntent struct {
	RequestID, ExchangeID, CoverageID, CallID string
	Purpose                                   AdmissionPurpose
	ExpectedMembershipRevision                uint64
}

func (i AdmitExchangeIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.ExchangeID) || !semanticID(i.CoverageID) || i.ExpectedMembershipRevision == 0 || i.Purpose != AdmissionReceived && i.Purpose != AdmissionGenerationInput {
		return invalid("admission intent: exact exchange, manifest coverage, and revision required")
	}
	return nil
}

type AcknowledgeExchangeIntent struct {
	RequestID, ExchangeID, ManifestID, ConsumingCallID string
	ExpectedRevision                                   uint64
}

func (i AcknowledgeExchangeIntent) Validate() error {
	for _, id := range []string{i.RequestID, i.ExchangeID, i.ManifestID, i.ConsumingCallID} {
		if !semanticID(id) {
			return invalid("acknowledgment: recorded consuming inference required")
		}
	}
	if i.ExpectedRevision == 0 {
		return invalid("acknowledgment: expected revision required")
	}
	return nil
}

type ExchangeCancellationReason string

const (
	ExchangeAbandoned            ExchangeCancellationReason = "ABANDONED"
	ExchangeExplicitCancellation ExchangeCancellationReason = "CANCELLED"
)

type CancelExchangeIntent struct {
	RequestID, ExchangeID string
	ExpectedRevision      uint64
	Reason                ExchangeCancellationReason
}

func (i CancelExchangeIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.ExchangeID) || i.ExpectedRevision == 0 || i.Reason != ExchangeAbandoned && i.Reason != ExchangeExplicitCancellation {
		return invalid("exchange cancellation: request, revision, and closed reason required")
	}
	return nil
}
