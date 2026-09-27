package domain

import "testing"

func TestOperationIsClosedAndDeepCloned(t *testing.T) {
	i := 0
	o := SemanticOperation{Kind: OperationObservation, Observation: &ObservationIntent{RunID: "run", ExecutionID: "exec", EvidenceSpanIndex: &i, Outcome: OutcomeTimeout, Completeness: ObservationPartial}}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	copy := o.Clone()
	*copy.Observation.EvidenceSpanIndex = 3
	if *o.Observation.EvidenceSpanIndex != 0 {
		t.Fatal("operation clone aliases nested pointer")
	}
	o.Span = &SpanIngestIntent{Index: 0}
	if o.Validate() == nil {
		t.Fatal("ambiguous operation accepted")
	}
}

func TestOperationReferenceCannotSkipResolvedValidation(t *testing.T) {
	o := SemanticOperation{Kind: OperationDeclareObligation, DeclareObligation: &DeclareObligationIntent{}, References: []OperationReference{{Slot: OperationSourceItem, Alias: "source"}}}
	if err := o.Validate(); err != nil {
		t.Fatal(err)
	}
	if o.ValidateResolved() == nil {
		t.Fatal("unresolved intent reached a service")
	}
	e := Event{Kind: EventHarness, Operations: []SemanticOperation{o}}
	if e.ValidateV3() == nil {
		t.Fatal("forward alias accepted")
	}
}
