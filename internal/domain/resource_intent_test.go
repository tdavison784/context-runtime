package domain

import "testing"

func TestObservationIntentEvidenceReferenceIsExclusive(t *testing.T) {
	span := 0
	i := ObservationIntent{RequestID: "r", RunID: "run", ExecutionID: "exec", EvidenceSpanIndex: &span, Outcome: OutcomeTimeout, Completeness: ObservationPartial}
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	i.EvidenceItemID = "also"
	if i.Validate() == nil {
		t.Fatal("ambiguous evidence binding")
	}
}
