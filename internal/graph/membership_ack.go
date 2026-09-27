package graph

import "github.com/tdavison784/context-runtime/internal/domain"

// A closed flag alone cannot authorize replacement. Verify its durable
// acknowledgment against the exact generation manifest and completed inference.
func checkMembershipAcknowledgment(x domain.LogicalExchange, ack domain.ExchangeAcknowledgment, manifest domain.AdmissionManifest, call domain.CallRecord) error {
	if x.Validate() != nil || ack.Validate() != nil || manifest.Validate() != nil || call.Validate() != nil ||
		x.State != domain.ExchangeClosed || ack.Cancelled || ack.ID != x.AcknowledgmentID || ack.ExchangeID != x.ID ||
		ack.SessionID != x.SessionID || manifest.SessionID != x.SessionID || call.SessionID != x.SessionID ||
		ack.ManifestID != manifest.ID || manifest.ExchangeID != x.ID || manifest.ConversationID != x.ConversationID ||
		manifest.Principal != x.Principal || manifest.TurnID != x.TurnID || manifest.Purpose != domain.AdmissionGenerationInput ||
		ack.ConsumingCallID != call.CallID || manifest.CallID != call.CallID || call.ConversationID != x.ConversationID ||
		call.Principal != x.Principal || call.Operation != domain.OperationInference || call.State != domain.CallCompleted ||
		checkMembershipControl(x.SessionID, ack.Actor, x.Principal) != nil ||
		checkMembershipControl(x.SessionID, call.ServiceActor, x.Principal) != nil {
		return domain.ErrIncompleteCoverage
	}
	return nil
}
