package domain

import "testing"

// G4 / SEC-1.11 / SPEC-1.2: an event with spans and an empty, non-nil
// Operations stream would be v3-hashed and stored while ingesting no span.
// Operations is either nil (spans ingest in order) or covers every span.
func TestV3RejectsEmptyNonNilOperationStream(t *testing.T) {
	span := Span{Authority: AuthorityHarness, Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}, Parts: []InputPart{{Type: PartText, Text: "hello"}}}
	nilOps := Event{EventID: "event", Kind: EventHarness, Spans: []Span{span}}
	if err := nilOps.ValidateV3(); err != nil {
		t.Fatalf("nil operation stream rejected: %v", err)
	}
	empty := nilOps.Clone()
	empty.Operations = []SemanticOperation{}
	if err := empty.ValidateV3(); err == nil {
		t.Fatal("empty non-nil operation stream accepted with spans it would never ingest")
	}
	if c := empty.Clone(); c.Operations == nil {
		t.Fatal("Clone normalized the empty stream, hiding it from validation")
	}
	noSpans := Event{EventID: "event", Kind: EventHarness, Operations: []SemanticOperation{}}
	if err := noSpans.ValidateV3(); err == nil {
		t.Fatal("empty non-nil operation stream accepted without spans")
	}
}
