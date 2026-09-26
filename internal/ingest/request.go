package ingest

import (
	"github.com/tdavison784/context-runtime/internal/domain"
)

// Request identity across the v2→v3 change (P3-40). A new event is hashed
// under v3 when the ingester records a Phase 3 policy and under frozen v2
// otherwise; a retry is always canonicalized under the schema, limits and
// policy its receipt recorded, never today's.

// semanticShaped reports whether e uses any Phase 3-only field.
func semanticShaped(e domain.Event) bool {
	return e.Operations != nil || e.Control
}

// validateRequest checks e's structure and authority for p under limits
// and, for a v3 request, policy pol. A typed stream without a recorded
// policy fails closed.
func validateRequest(e domain.Event, p domain.Principal, limits domain.Limits, pol *domain.Phase3Policy) error {
	if !semanticShaped(e) && pol == nil {
		return e.ValidateFor(p, limits)
	}
	if pol == nil {
		return domain.ErrUnsupportedSchema
	}
	_, err := e.PayloadHashFor(domain.RequestHashV3, p, limits, *pol)
	return err
}

// newRequestHash is a new event's identity under the current configuration.
func newRequestHash(e domain.Event, p domain.Principal, limits domain.Limits, pol *domain.Phase3Policy) (string, error) {
	if pol == nil {
		if semanticShaped(e) {
			return "", domain.ErrUnsupportedSchema
		}
		return e.PayloadHash(p)
	}
	return e.PayloadHashFor(domain.RequestHashV3, p, limits, *pol)
}

// recordedHash canonicalizes a retry under its receipt's recorded request
// schema, limits and policy. Any failure means the retry cannot be the
// recorded request: a Phase 3-only field under a v2 identity is never
// projected away to make it match.
func recordedHash(e domain.Event, p domain.Principal, rc domain.IngestReceipt) (string, error) {
	var (
		h   string
		err error
	)
	switch {
	case rc.SchemaVersion == domain.IngestReceiptSchemaVersion && (rc.RequestHashVersion == "" || rc.RequestHashVersion == domain.RequestHashV2):
		h, err = e.PayloadHashFor(domain.RequestHashV2, p, domain.Limits{}, domain.Phase3Policy{})
	case rc.RequestHashVersion == domain.RequestHashV3 && rc.Versions.Semantic != nil:
		h, err = e.PayloadHashFor(domain.RequestHashV3, p, rc.Versions.Limits, *rc.Versions.Semantic)
	default:
		return "", domain.ErrEventIDConflict
	}
	if err != nil {
		return "", domain.ErrEventIDConflict
	}
	return h, nil
}

// ceilingPolicy is pol with its request-size limits raised to the hard
// ceilings, for validating a known retry before its recorded policy is read
// (F3): the ceiling bounds hashing work, the recorded policy decides.
func ceilingPolicy(pol *domain.Phase3Policy) *domain.Phase3Policy {
	if pol == nil {
		return nil
	}
	c := *pol
	c.MaxOperations = max(c.MaxOperations, hardMaxOperations)
	c.MaxMetadataBytes = max(c.MaxMetadataBytes, hardMaxOperationBytes)
	return &c
}

// newSemanticEnvelope snapshots a v3 request the way domain.NewEventEnvelope
// snapshots a v2 one: supplied blob bytes become verified references, and
// the recorded hash schema, limits and policy travel with it.
func newSemanticEnvelope(p domain.Principal, occurrence string, e domain.Event, limits domain.Limits, pol domain.Phase3Policy) (domain.EventEnvelope, error) {
	hash, err := e.PayloadHashFor(domain.RequestHashV3, p, limits, pol)
	if err != nil {
		return domain.EventEnvelope{}, err
	}
	e = e.Clone()
	for i := range e.Spans {
		for j, part := range e.Spans[i].Parts {
			if part.Data != nil {
				snap := part.Snapshot()
				e.Spans[i].Parts[j] = domain.InputPart{Type: part.Type, MediaType: part.MediaType, BlobHash: snap.BlobHash, BlobSize: snap.BlobSize}
			}
		}
	}
	env := domain.EventEnvelope{
		RequestHashVersion: domain.RequestHashV3,
		SemanticPolicy:     &pol,
		Limits:             limits,
		SessionID:          p.SessionID,
		OccurrenceID:       occurrence,
		EventID:            e.EventID,
		Principal:          p,
		Event:              e,
		PayloadHash:        hash,
		SchemaVersion:      domain.EventEnvelopeSchemaV2,
	}
	return env, env.Validate()
}
