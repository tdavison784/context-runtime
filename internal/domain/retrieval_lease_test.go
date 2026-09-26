package domain

import "testing"

func TestLeaseBindsExactHolderAndInferenceWindow(t *testing.T) {
	p := Principal{SessionID: "s", TaskID: "t", AgentID: "a", Authority: AuthorityAgent}
	l := RetrievalLease{SemanticMeta: semanticMeta("l"), Holder: p, ConversationID: ConversationIDFor("t", "a"), TurnID: "turn", Source: ItemContentRef{ItemID: "i", ContentHash: HashBytes(nil)}, IssuedCompletedInferenceIndex: 10, CallAllowance: 2, PolicyVersion: "p"}
	for _, n := range []uint64{10, 11} {
		if !l.Live(p, l.ConversationID, "turn", true, n) {
			t.Fatal("live lease denied")
		}
	}
	for _, n := range []uint64{9, 12, ^uint64(0)} {
		if l.Live(p, l.ConversationID, "turn", true, n) {
			t.Fatal("expired/underflow lease admitted")
		}
	}
	p.Authority = AuthorityHarness
	if l.Live(p, l.ConversationID, "turn", true, 10) {
		t.Fatal("holder authority omitted")
	}
}
