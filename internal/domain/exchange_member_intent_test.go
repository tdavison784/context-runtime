package domain

import (
	"testing"
)

func TestRegisterExchangeMemberIntent(t *testing.T) {
	base := RegisterExchangeMemberIntent{RequestID: "req", ExchangeID: "exchange", ExpectedRevision: 2, Position: 1, Role: MemberInput, Source: ItemContentRef{ItemID: "source", ContentHash: NewCanonicalEncoder("test/source/v1").String("source").Hash()}}
	for _, role := range []ExchangeMemberRole{MemberInput, MemberOutput, MemberToolCall, MemberToolResult} {
		i := base.Clone()
		i.Role, i.CallID, i.ToolCallID, i.AdmissionID = role, "call", "tool", "admission"
		if err := i.Validate(); err != nil {
			t.Fatalf("%s: %v", role, err)
		}
	}
	for name, mutate := range map[string]func(*RegisterExchangeMemberIntent){
		"request":     func(i *RegisterExchangeMemberIntent) { i.RequestID = "" },
		"exchange":    func(i *RegisterExchangeMemberIntent) { i.ExchangeID = "" },
		"revision":    func(i *RegisterExchangeMemberIntent) { i.ExpectedRevision = 0 },
		"position":    func(i *RegisterExchangeMemberIntent) { i.Position = 0 },
		"source":      func(i *RegisterExchangeMemberIntent) { i.Source.ItemID = "" },
		"hash":        func(i *RegisterExchangeMemberIntent) { i.Source.ContentHash = "forged" },
		"role":        func(i *RegisterExchangeMemberIntent) { i.Role = "forged" },
		"output call": func(i *RegisterExchangeMemberIntent) { i.Role = MemberOutput },
		"tool call":   func(i *RegisterExchangeMemberIntent) { i.Role = MemberToolCall },
		"tool result": func(i *RegisterExchangeMemberIntent) { i.Role, i.CallID = MemberToolResult, "call" },
		"admission":   func(i *RegisterExchangeMemberIntent) { i.AdmissionID = "bad\x00id" },
	} {
		i := base.Clone()
		mutate(&i)
		if i.Validate() == nil {
			t.Errorf("accepted invalid %s", name)
		}
	}
	clone := base.Clone()
	clone.Source.ItemID = "changed"
	if base.Source.ItemID != "source" || base.Validate() != nil {
		t.Fatal("clone changed original request")
	}
}
