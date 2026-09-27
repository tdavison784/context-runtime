package domain

import (
	"errors"
	"strings"
	"testing"
)

func toolOutcomeBinding() OutcomeBinding {
	return OutcomeBinding{Principal: Principal{SessionID: "s", WorkflowID: "w", TaskID: "t", AgentID: "a", Authority: AuthorityAgent}, ConversationID: ConversationIDFor("t", "a"), ExchangeID: "exchange", CallID: "call", TurnID: "turn", Turn: 1}
}

// Golden vectors freeze the outcome-event-id/v1 encoding and the tool-outcome
// EventID format (ADR 4); changing either reinterprets stored EventIDs.
const (
	goldenOutcomeEventID     = "outcome-ffb7703a1e9aa2fc44d7d184982e8034abba60184da65030a5906ee603cb5522"
	goldenToolOutcomeEventID = goldenOutcomeEventID + "/toolu_01/a"
)

func TestToolOutcomeEventIDGolden(t *testing.T) {
	b := toolOutcomeBinding()
	outcome, err := OutcomeEventID(b)
	if err != nil || outcome != goldenOutcomeEventID {
		t.Fatalf("OutcomeEventID = %q, %v; golden %q", outcome, err, goldenOutcomeEventID)
	}
	id, err := ToolOutcomeEventID(b, "toolu_01/a")
	if err != nil || id != goldenToolOutcomeEventID {
		t.Fatalf("ToolOutcomeEventID = %q, %v; golden %q", id, err, goldenToolOutcomeEventID)
	}
	gotOutcome, gotCall, err := ParseToolOutcomeEventID(id)
	if err != nil || gotOutcome != outcome || gotCall != "toolu_01/a" {
		t.Fatalf("Parse = %q, %q, %v; a slash in the tool call ID must not move the split", gotOutcome, gotCall, err)
	}
	if err := ValidateToolOutcomeEventID(id, b, "toolu_01/a"); err != nil {
		t.Fatal(err)
	}
	if err := (Event{EventID: id, Kind: EventTool}).validateShape(true); err != nil {
		t.Fatalf("tool-outcome EventID is not a valid event ID: %v", err)
	}
}

func TestToolOutcomeEventIDBindsOutputAndCall(t *testing.T) {
	b := toolOutcomeBinding()
	id, err := ToolOutcomeEventID(b, "toolu_01")
	if err != nil {
		t.Fatal(err)
	}
	other := b
	other.CallID = "other-call"
	for _, bad := range []struct {
		b    OutcomeBinding
		call string
	}{{other, "toolu_01"}, {b, "toolu_02"}, {b, "toolu_0"}} {
		if err := ValidateToolOutcomeEventID(id, bad.b, bad.call); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("EventID accepted for another output or tool call: %v", err)
		}
	}
	invalidBinding := b
	invalidBinding.ConversationID = "wrong"
	if _, err := ToolOutcomeEventID(invalidBinding, "toolu_01"); err == nil {
		t.Fatal("invalid binding received a tool-outcome EventID")
	}
	longest := strings.Repeat("x", MaxToolOutcomeCallIDBytes)
	if id, err := ToolOutcomeEventID(b, longest); err != nil || len(id) != MaxEventIDBytes {
		t.Fatalf("longest tool call ID: %d bytes, %v; want exactly MaxEventIDBytes", len(id), err)
	}
	for _, call := range []string{"", longest + "x", "a b", "é", "a\n"} {
		if _, err := ToolOutcomeEventID(b, call); !errors.Is(err, ErrInvalidRecord) {
			t.Fatalf("tool call ID %q accepted: %v", call, err)
		}
	}
}

func TestParseToolOutcomeEventIDRejectsMalformed(t *testing.T) {
	outcome, _ := OutcomeEventID(toolOutcomeBinding())
	for _, id := range []string{
		"", outcome, outcome + "/", outcome + "x", outcome + "-toolu",
		"outcome-" + strings.ToUpper(outcome[len("outcome-"):]) + "/toolu",
		"outcome-" + strings.Repeat("g", 64) + "/toolu",
		"income-" + outcome[len("outcome-"):] + "x/toolu",
		outcome[:len(outcome)-1] + "/toolu",
		outcome + "/a b",
	} {
		if _, _, err := ParseToolOutcomeEventID(id); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("Parse(%q) accepted a malformed tool-outcome EventID", id)
		}
	}
}
