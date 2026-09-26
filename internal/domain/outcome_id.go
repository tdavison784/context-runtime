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
