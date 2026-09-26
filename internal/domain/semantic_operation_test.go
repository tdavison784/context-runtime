package domain

import "testing"

func TestOperationIsClosedAndDeepCloned(t *testing.T) {
	i := 0
	o := SemanticOperation{Kind: OperationObservation, Observation: &ObservationIntent{RequestID: "r", RunID: "run", ExecutionID: "exec", EvidenceSpanIndex: &i, Outcome: OutcomeTimeout, Completeness: ObservationPartial}}
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
