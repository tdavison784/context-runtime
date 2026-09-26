package domain

import "testing"

func TestRetrievalOriginRequiresInvocationExceptTrustedHarness(t *testing.T) {
	holder := Principal{SessionID: "s", TaskID: "t", AgentID: "a", Authority: AuthorityHarness}
	origin := RetrievalOrigin{Holder: holder, ConversationID: ConversationIDFor("t", "a"), TurnID: "turn"}
	if err := origin.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, authority := range []Authority{AuthorityAgent, AuthorityTool, AuthorityUser, AuthoritySystem, AuthorityRetrievedContent} {
		bad := origin.Clone()
		bad.Holder.Authority = authority
		if bad.Validate() == nil {
			t.Fatalf("%s omitted invocation", authority)
		}
	}
	origin.Holder.Authority = AuthorityAgent
	origin.Invocation = &ToolInvocation{SessionID: "s", ConversationID: origin.ConversationID, CallID: "call", ToolCallID: "tool", ExchangeID: "exchange", TurnID: "turn", Principal: origin.Holder}
	if err := origin.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*RetrievalOrigin){
		func(o *RetrievalOrigin) { o.Holder.WorkflowID = "other" },
		func(o *RetrievalOrigin) { o.ConversationID = "other" },
		func(o *RetrievalOrigin) { o.TurnID = "other" },
		func(o *RetrievalOrigin) { o.Invocation.ToolCallID = "" },
		func(o *RetrievalOrigin) { o.Invocation.Principal.Authority = AuthorityHarness },
	} {
		bad := origin.Clone()
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("accepted mismatched origin")
		}
	}
	if origin.Validate() != nil {
		t.Fatal("clone aliases invocation")
	}
}
