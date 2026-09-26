package domain

import "testing"

func TestSemanticIDsSeparateFamiliesAndExactVersions(t *testing.T) {
	a, _ := MutationReceiptID("s", MutationResourceReport, "r")
	b, _ := MutationReceiptID("s", MutationResourceResync, "r")
	if a == b {
		t.Fatal("receipt families collide")
	}
	target := ObligationRef{SessionID: "s", ObligationID: "o", Version: 1}
	a, _ = ApplicabilityProofID(target, "tr")
	target.Version = 2
	b, _ = ApplicabilityProofID(target, "tr")
	if a == b {
		t.Fatal("proof target versions collide")
	}
	if semanticMeta("i").SemanticSeq() != 1 {
		t.Fatal("semantic sequence accessor")
	}
}
