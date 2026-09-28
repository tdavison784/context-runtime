package domain

// NewSemanticEventEnvelope owns v3 request validation and blob snapshotting.
// Limits/policy travel with the immutable request; retries never borrow current
// execution defaults. No caller-owned event slice or blob buffer is retained.
func NewSemanticEventEnvelope(p Principal, occurrence string, e Event, limits Limits, policy Phase3Policy) (EventEnvelope, error) {
	if err := limits.Validate(); err != nil {
		return EventEnvelope{}, err
	}
	limits = limits.Effective()
	hash, err := e.PayloadHashFor(RequestHashV3, p, limits, policy)
	if err != nil {
		return EventEnvelope{}, err
	}
	e = snapshotEventBlobs(e)
	policy = policy.Clone()
	env := EventEnvelope{RequestHashVersion: RequestHashV3, SemanticPolicy: &policy, Limits: limits, SessionID: p.SessionID, OccurrenceID: occurrence, EventID: e.EventID, Principal: p, Event: e, PayloadHash: hash, SchemaVersion: EventEnvelopeSchemaV2}
	return env, env.Validate()
}
