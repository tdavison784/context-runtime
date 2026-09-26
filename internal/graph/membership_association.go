package graph

import "github.com/tdavison784/context-runtime/internal/domain"

// A round has one completed assistant output. Every tool call names that exact
// output source; every result names a previously registered call in this round.
func checkMemberAssociation(x domain.LogicalExchange, intent domain.RegisterExchangeMemberIntent, members []domain.ExchangeMember) error {
	if intent.Position != uint64(len(members))+1 {
		return domain.ErrInvalidTransition
	}
	positions := make(map[uint64]bool, len(members))
	var output *domain.ExchangeMember
	var matchedCall, matchedResult bool
	for n := range members {
		m := &members[n]
		if m.ExchangeID != x.ID || m.Position == 0 || m.Position > uint64(len(members)) || positions[m.Position] {
			return domain.ErrIncompleteCoverage
		}
		positions[m.Position] = true
		if m.Role == domain.MemberOutput {
			if output != nil {
				return domain.ErrIncompleteCoverage
			}
			output = m
		}
		if m.CallID == intent.CallID && m.ToolCallID == intent.ToolCallID {
			matchedCall = matchedCall || m.Role == domain.MemberToolCall
			matchedResult = matchedResult || m.Role == domain.MemberToolResult
		}
	}
	switch intent.Role {
	case domain.MemberInput:
		if x.State == domain.ExchangeOpen && output == nil && intent.CallID == "" && intent.ToolCallID == "" {
			return nil
		}
	case domain.MemberOutput:
		if x.State == domain.ExchangeOpen && output == nil && intent.ToolCallID == "" {
			return nil
		}
	case domain.MemberToolCall:
		if x.State == domain.ExchangeExecuting && output != nil && output.CallID == intent.CallID && output.Source == intent.Source && !matchedCall {
			return nil
		}
	case domain.MemberToolResult:
		if x.State == domain.ExchangeExecuting && output != nil && output.CallID == intent.CallID && matchedCall && !matchedResult {
			return nil
		}
	}
	return domain.ErrInvalidTransition
}
