package domain

import "testing"

func TestRetrievalDeniedAuditCannotPublishSource(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthorityHarness}
	e := RetrievalEvent{SemanticMeta: semanticMeta("e"), RequestID: "r", Principal: p, TriggeringActor: p, InvocationID: "inv", ErrorCode: ToolErrorNotFound}
	if err := e.Validate(); err != nil {
		t.Fatal(err)
	}
	e.Source = &ItemContentRef{ItemID: "private", ContentHash: HashBytes(nil)}
	if e.Validate() == nil {
		t.Fatal("denied audit leaked source")
	}
}
