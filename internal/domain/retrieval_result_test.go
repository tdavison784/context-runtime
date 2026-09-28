package domain

import "testing"

func TestRetrievalDeniedAuditCannotPublishSource(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthorityHarness}
	e := RetrievalEvent{SemanticMeta: semanticMeta("e"), RequestID: "r", Principal: p, TriggeringActor: p, ErrorCode: ToolErrorNotFound}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Source = &ItemContentRef{ItemID: "private", ContentHash: HashBytes(nil)}
	if e.Validate() == nil {
		t.Fatal("denied audit leaked source")
	}
}

func TestHarnessRetrievalRequiresMatchingAudit(t *testing.T) {
	p := Principal{SessionID: "s", TaskID: "t", AgentID: "a", Authority: AuthorityHarness}
	source := ItemContentRef{ItemID: "i", ContentHash: HashBytes(nil)}
	r := RetrievalResult{SemanticMeta: semanticMeta("result"), RequestID: "req", LeaseID: "lease", ProjectionID: "projection", RetrievalEventID: "event", PolicyVersion: "policy", Origin: RetrievalOrigin{Holder: p, ConversationID: ConversationIDFor("t", "a"), TurnID: "turn"}, Access: AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "t"}, Observed: ObservedItemState{Source: source, Version: 1, Currentness: ItemCurrent, Generation: GenerationDurable, Residency: ResidencyResident, Authority: AuthorityUser, Expiry: ExpiryLive}}
	e := RetrievalEvent{SemanticMeta: semanticMeta("event"), RequestID: "req", Principal: p, TriggeringActor: p, ResultID: r.ID, Source: &source}
	if err := r.ValidateOriginEvent(e); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*RetrievalEvent){
		func(e *RetrievalEvent) { e.RequestID = "other" },
		func(e *RetrievalEvent) { e.ResultID = "other" },
		func(e *RetrievalEvent) { e.Principal.AgentID = "other" },
		func(e *RetrievalEvent) { e.InvocationID = "fabricated-tool" },
		func(e *RetrievalEvent) { e.TriggeringActor.Authority = AuthorityAgent },
		func(e *RetrievalEvent) { e.Source.ItemID = "other" },
	} {
		bad := e.Clone()
		change(&bad)
		if r.ValidateOriginEvent(bad) == nil {
			t.Fatal("accepted unrelated retrieval audit")
		}
	}
	r.Origin.Holder.Authority = AuthorityAgent
	r.Origin.Invocation = &ToolInvocation{SessionID: "s", ConversationID: r.Origin.ConversationID, TurnID: "turn", CallID: "call", ToolCallID: "tool", ExchangeID: "exchange", Principal: r.Origin.Holder}
	copy := r.Clone()
	copy.Origin.Invocation.CallID = "changed"
	projection := ProjectionRecord{Origin: r.Origin}
	projectionCopy := projection.Clone()
	projectionCopy.Origin.Invocation.ToolCallID = "changed"
	if r.Origin.Invocation.CallID != "call" || projection.Origin.Invocation.ToolCallID != "tool" {
		t.Fatal("origin invocation aliases clone")
	}
}
