package domain

import "strings"

// OutcomeEventIDDomain freezes every authenticated binding field in W7's
// existing order. Outcomes from a different call, exchange or turn never alias.
const OutcomeEventIDDomain = "context-runtime/ingest/outcome-event-id/v1"

func OutcomeEventID(b OutcomeBinding) (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	c := NewCanonicalEncoder(OutcomeEventIDDomain)
	encodePrincipal(c, b.Principal)
	c.String(b.ConversationID).String(b.ExchangeID).String(b.CallID).String(b.TurnID).Uint(b.Turn)
	return "outcome-" + strings.TrimPrefix(c.Hash(), "sha256:"), nil
}

// ToolOutcomeEventIDSeparator joins a provider output's OutcomeEventID and a
// tool call ID it issued into the EventID of that call's external result.
const ToolOutcomeEventIDSeparator = "/"

const (
	outcomeEventIDPrefix = "outcome-"
	outcomeEventIDLen    = len(outcomeEventIDPrefix) + 64
	// MaxToolOutcomeCallIDBytes keeps a tool-outcome EventID within
	// MaxEventIDBytes; a longer tool call ID has no tool-outcome EventID.
	MaxToolOutcomeCallIDBytes = MaxEventIDBytes - outcomeEventIDLen - len(ToolOutcomeEventIDSeparator)
)

// ToolOutcomeEventID is the EventID of the external tool result for
// toolCallID of the output bound by b: OutcomeEventID(b), "/", toolCallID.
// The fixed-length outcome prefix makes the split unambiguous even when the
// tool call ID contains "/", and binds the result to exactly one call of one
// authenticated output. It reuses the outcome-event-id/v1 domain; it
// registers no new canonical domain.
func ToolOutcomeEventID(b OutcomeBinding, toolCallID string) (string, error) {
	if err := validToolOutcomeCallID(toolCallID); err != nil {
		return "", err
	}
	id, err := OutcomeEventID(b)
	if err != nil {
		return "", err
	}
	return id + ToolOutcomeEventIDSeparator + toolCallID, nil
}

// ParseToolOutcomeEventID splits a well-formed tool-outcome EventID. It is
// shape only and authenticates nothing: use ValidateToolOutcomeEventID to
// bind an EventID to an authenticated output and tool call.
func ParseToolOutcomeEventID(eventID string) (outcomeEventID, toolCallID string, err error) {
	if len(eventID) <= outcomeEventIDLen || !strings.HasPrefix(eventID, outcomeEventIDPrefix) || eventID[outcomeEventIDLen:outcomeEventIDLen+len(ToolOutcomeEventIDSeparator)] != ToolOutcomeEventIDSeparator {
		return "", "", invalid("tool outcome event ID: outcome prefix and separator required")
	}
	outcomeEventID, toolCallID = eventID[:outcomeEventIDLen], eventID[outcomeEventIDLen+len(ToolOutcomeEventIDSeparator):]
	for _, c := range outcomeEventID[len(outcomeEventIDPrefix):] {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", "", invalid("tool outcome event ID: outcome hash must be lowercase hex")
		}
	}
	if err := validToolOutcomeCallID(toolCallID); err != nil {
		return "", "", err
	}
	return outcomeEventID, toolCallID, nil
}

// ValidateToolOutcomeEventID requires eventID to be exactly the tool-outcome
// EventID of the authenticated binding b and toolCallID.
func ValidateToolOutcomeEventID(eventID string, b OutcomeBinding, toolCallID string) error {
	want, err := ToolOutcomeEventID(b, toolCallID)
	if err != nil {
		return err
	}
	if eventID != want {
		return invalid("tool outcome event ID: does not name this output's tool call")
	}
	return nil
}

func validToolOutcomeCallID(id string) error {
	if id == "" || len(id) > MaxToolOutcomeCallIDBytes || !printableASCII(id, false) {
		return invalid("tool outcome event ID: tool call ID must be 1..%d printable ASCII bytes", MaxToolOutcomeCallIDBytes)
	}
	return nil
}
