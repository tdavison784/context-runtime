package domain

import "testing"

func TestOutcomeEventIDCommitsEntireOriginBinding(t *testing.T) {
	b := OutcomeBinding{Principal: Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", AgentID: "a", Authority: AuthorityAgent}, ConversationID: ConversationIDFor("t", "a"), ExchangeID: "exchange", CallID: "call", TurnID: "turn", Turn: 1}
	id, err := OutcomeEventID(b)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*OutcomeBinding){
		func(b *OutcomeBinding) { b.Principal.SessionID = "other" },
		func(b *OutcomeBinding) { b.Principal.WorkflowID = "other" },
		func(b *OutcomeBinding) { b.Principal.Authority = AuthorityTool },
		func(b *OutcomeBinding) { b.ExchangeID = "other" },
		func(b *OutcomeBinding) { b.CallID = "other" },
		func(b *OutcomeBinding) { b.TurnID = "other" },
		func(b *OutcomeBinding) { b.Turn++ },
	} {
		other := b
		change(&other)
		got, err := OutcomeEventID(other)
		if err != nil || got == id {
			t.Fatal("outcome origin omitted", err)
		}
	}
	b.ConversationID = "wrong"
	if _, err := OutcomeEventID(b); err == nil {
		t.Fatal("invalid binding received an outcome identity")
	}
}
