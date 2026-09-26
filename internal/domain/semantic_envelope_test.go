package domain

import "testing"

func TestSemanticEnvelopeOwnsBlobSnapshotAndRecordedInputs(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthorityHarness}
	data := []byte{1, 2, 3}
	e := Event{EventID: "event", Kind: EventHarness, Spans: []Span{{Authority: AuthorityHarness, Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}, Parts: []InputPart{{Type: PartImage, MediaType: "image/png", Data: data}}}}}
	policy := semanticPolicy()
	occurrence := CallerOccurrenceID("s", "event")
	env, err := NewSemanticEventEnvelope(p, occurrence, e, Limits{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	part := env.Event.Spans[0].Parts[0]
	if part.Data != nil || part.BlobHash != HashBytes(data) || part.BlobSize != 3 || env.Limits != (Limits{}).Effective() {
		t.Fatal("envelope did not freeze blob and effective limits")
	}
	reference := e.Clone()
	reference.Spans[0].Parts[0] = part
	refEnv, err := NewSemanticEventEnvelope(p, occurrence, reference, Limits{}, policy)
	if err != nil || refEnv.PayloadHash != env.PayloadHash {
		t.Fatal("bytes and snapshot hash differ", err)
	}
	data[0] = 9
	policy.MaxOperations = 1
	e.Spans[0].Parts[0].MediaType = "changed"
	if err := env.Validate(); err != nil {
		t.Fatal("caller mutation corrupted envelope", err)
	}
	if env.SemanticPolicy.MaxOperations == 1 {
		t.Fatal("policy aliases caller")
	}
	if _, err := NewSemanticEventEnvelope(p, "invalid-occurrence", reference, Limits{}, semanticPolicy()); err == nil {
		t.Fatal("invalid occurrence accepted")
	}
}
