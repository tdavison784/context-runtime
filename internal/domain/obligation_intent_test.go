package domain

import "testing"

func TestTransitionIntentRejectsImplicitFreshness(t *testing.T) {
	i := TransitionIntent{RequestID: "r", Target: ObligationRef{SessionID: "s", ObligationID: "o", Version: 1}, ExpectedRevision: 1, To: ObligationSatisfied}
	if i.Validate() == nil {
		t.Fatal("implicit freshness accepted")
	}
	i.AssertionMode = AssertionAttestation
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	i.To = ObligationBlocked
	if i.Validate() == nil {
		t.Fatal("block smuggles proof intent")
	}
}
