package domain

import "testing"

func TestToolCallSpellingIsScopedToOutput(t *testing.T) {
	i := ToolInvocation{SessionID: "s", ConversationID: ConversationIDFor("t", "a"), CallID: "output1", ToolCallID: "call_1", ExchangeID: "x", TurnID: "turn", Principal: Principal{SessionID: "s", TaskID: "t", AgentID: "a", Authority: AuthorityAgent}}
	id, err := i.ID()
	if err != nil {
		t.Fatal(err)
	}
	i.CallID = "output2"
	other, _ := i.ID()
	if id == other {
		t.Fatal("tool spelling aliases different outputs")
	}
	i.Principal.Authority = AuthorityHarness
	if i.Validate() == nil {
		t.Fatal("tool execution promoted to harness")
	}
}
