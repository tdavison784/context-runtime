package domain

import "testing"

func TestLeaseRequiresExactHolderAndFiniteAllowance(t *testing.T) {
	p := Principal{SessionID: "s", TaskID: "t", AgentID: "a", Authority: AuthorityAgent}
	l := RetrievalLease{SemanticMeta: semanticMeta("l"), Holder: p, ConversationID: ConversationIDFor("t", "a"), TurnID: "turn", Source: ItemContentRef{ItemID: "i", ContentHash: HashBytes(nil)}, IssuedCompletedInferenceIndex: 10, CallAllowance: 2, PolicyVersion: "p"}
	if err := l.Validate(); err != nil {
		t.Fatal(err)
	}
	l.CallAllowance = 0
	if l.Validate() == nil {
		t.Fatal("unlimited lease accepted")
	}
	l.CallAllowance = 2
	l.Holder.AgentID = "other"
	if l.Validate() == nil {
		t.Fatal("holder conversation mismatch")
	}
}
