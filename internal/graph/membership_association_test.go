package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestMembershipToolCallsRequireExactOutputAndResultsStayGrouped(t *testing.T) {
	source := domain.ItemContentRef{ItemID: "output", ContentHash: domain.HashBytes([]byte("output"))}
	x := domain.LogicalExchange{State: domain.ExchangeExecuting}
	out := domain.ExchangeMember{Position: 1, Role: domain.MemberOutput, Source: source, CallID: "call"}
	intent := domain.RegisterExchangeMemberIntent{Position: 2, Role: domain.MemberToolCall, Source: source, CallID: "call", ToolCallID: "tool"}
	if err := checkMemberAssociation(x, intent, []domain.ExchangeMember{out}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"no-output", "other-source", "other-call", "open", "position", "duplicate"} {
		t.Run(change, func(t *testing.T) {
			state, args, members := x, intent, []domain.ExchangeMember{out}
			switch change {
			case "no-output":
				members, args.Position = nil, 1
			case "other-source":
				args.Source.ItemID = "other"
			case "other-call":
				args.CallID = "other"
			case "open":
				state.State = domain.ExchangeOpen
			case "position":
				args.Position = 3
			case "duplicate":
				members = append(members, domain.ExchangeMember{Position: 2, Role: args.Role, Source: args.Source, CallID: args.CallID, ToolCallID: args.ToolCallID})
				args.Position = 3
			}
			if err := checkMemberAssociation(state, args, members); err == nil {
				t.Fatal("unauthenticated association accepted")
			}
		})
	}
	call := domain.ExchangeMember{Position: 2, Role: intent.Role, Source: source, CallID: intent.CallID, ToolCallID: intent.ToolCallID}
	intent.Role, intent.Position, intent.Source.ItemID = domain.MemberToolResult, 3, "result"
	if err := checkMemberAssociation(x, intent, []domain.ExchangeMember{out, call}); err != nil {
		t.Fatal(err)
	}
	intent.ToolCallID = "another"
	if err := checkMemberAssociation(x, intent, []domain.ExchangeMember{out, call}); err == nil {
		t.Fatal("ungrouped tool result")
	}
}
