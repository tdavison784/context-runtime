package domain

import "testing"

func TestResourceObservationRecordResults(t *testing.T) {
	for _, kind := range []string{"OBSERVATION_RUN", "OBSERVATION", "RESOURCE_BINDING"} {
		t.Run(kind, func(t *testing.T) {
			r := RecordResult{Kind: kind, IDs: []string{"immutable-record"}}
			if err := r.Validate(); err != nil {
				t.Fatal(err)
			}
			r.IDs = nil
			if r.Validate() == nil {
				t.Fatal("accepted result without immutable record")
			}
		})
	}
	if (RecordResult{Kind: "UNREGISTERED", IDs: []string{"id"}}).Validate() == nil {
		t.Fatal("accepted unregistered result family")
	}
}

func TestMutationResultCloneFreezesNestedOutcome(t *testing.T) {
	r := MutationResult{Records: &RecordResult{Kind: "GRANT", IDs: []string{"g"}}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	copy := r.Clone()
	copy.Records.IDs[0] = "other"
	if r.Records.IDs[0] != "g" {
		t.Fatal("nested result aliases")
	}
	r.Tool = &ToolResult{CheckpointID: "c"}
	if r.Validate() == nil {
		t.Fatal("ambiguous receipt result")
	}
}
