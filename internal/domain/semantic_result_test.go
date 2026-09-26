package domain

import "testing"

func TestDuplicateResultCannotClaimSupersession(t *testing.T) {
	r := KeyedWriteResult{ItemID: "dup", CanonicalItemID: "original", Duplicate: true}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	r.SupersededItemID = "old"
	if r.Validate() == nil {
		t.Fatal("duplicate superseded current state")
	}
	if (ToolResult{CheckpointID: "checkpoint", RetrievalResultID: "retrieval"}).Validate() == nil {
		t.Fatal("ambiguous result accepted")
	}
}
