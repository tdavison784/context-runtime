package domain

import "testing"

func TestCollectReceiptFreezesDecisions(t *testing.T) {
	ref := ItemRevisionRef{ItemID: "i", Version: 1}
	r := CollectReceipt{SemanticMeta: semanticMeta("r"), RequestID: "request", PolicyVersion: "p", Principal: Principal{SessionID: "s", Authority: AuthorityHarness}, CandidateRefs: []ItemRevisionRef{ref}, Decisions: []GCDecision{{Target: ref, Code: GCArchive}}, ArchivedRefs: []ItemRevisionRef{{ItemID: "i", Version: 2}}}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	copy := r.Clone()
	copy.ArchivedRefs[0].ItemID = "x"
	if copy.Validate() == nil || r.ArchivedRefs[0].ItemID != "i" {
		t.Fatal("frozen result mismatch accepted")
	}
}
