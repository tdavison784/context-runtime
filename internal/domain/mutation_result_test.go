package domain

import "testing"

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
