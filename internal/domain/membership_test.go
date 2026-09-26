package domain

import "testing"

func TestExchangeClosureRequiresAcknowledgment(t *testing.T) {
	p := Principal{SessionID: "s", TaskID: "t", AgentID: "a", Authority: AuthorityAgent}
	x := LogicalExchange{SemanticMeta: semanticMeta("x"), ConversationID: ConversationIDFor("t", "a"), Ordinal: 1, Principal: p, TurnID: "turn", Turn: 1, State: ExchangeOpen, Revision: 1}
	if err := x.Validate(); err != nil {
		t.Fatal(err)
	}
	x.State = ExchangeClosed
	if x.Validate() == nil {
		t.Fatal("unacknowledged closure accepted")
	}
	x.AcknowledgmentID = "ack"
	if err := x.Validate(); err != nil {
		t.Fatal(err)
	}
	x.Principal.AgentID = "b"
	if x.Validate() == nil {
		t.Fatal("other conversation accepted")
	}
}

func TestFrontierDoesNotOutrunMembership(t *testing.T) {
	s := ConversationMembershipState{SemanticMeta: semanticMeta("state"), ConversationID: "c", Revision: 1, LastOrdinal: 2, ClosedFrontier: 3}
	if s.Validate() == nil {
		t.Fatal("frontier outran registered exchanges")
	}
	s.ClosedFrontier = 2
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
	if (Checkpoint{SemanticMeta: semanticMeta("summary")}).Validate() == nil {
		t.Fatal("ordinary summary accepted as checkpoint")
	}
}
