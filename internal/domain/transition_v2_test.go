package domain

import "testing"

func TestInvalidationCannotReuseHistoricalGrantToSatisfy(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthorityHarness}
	ref := ObligationRef{SessionID: "s", ObligationID: "o", Version: 1}
	tx := ObligationTransition{ID: "tr", SessionID: "s", ObligationID: "o", Version: 1, Seq: 3, From: ObligationSatisfied, To: ObligationUnresolved, Action: ActionAssertObligation, Actor: p, Cause: CauseResourceInvalidation, RequestID: "request", ReasonCode: ReasonResourceChanged, PriorProofID: "proof", CauseRecordID: "update", OriginAuthorizationRef: &OriginAuthorizationRef{TransitionID: "old", GrantID: "expired", Actor: p, Target: ref, Seq: 1}}
	if err := tx.Validate(); err != nil {
		t.Fatal(err)
	}
	tx.From = ObligationUnresolved
	tx.To = ObligationSatisfied
	if tx.Validate() == nil {
		t.Fatal("restricted invalidation authorized positive transition")
	}
}
