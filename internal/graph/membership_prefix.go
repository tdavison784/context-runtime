package graph

import (
	"cmp"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// ClosedPrefixSnapshot is the membership snapshot a checkpoint may replace.
// Exchange IDs are explicit; the frontier never substitutes for membership.
type ClosedPrefixSnapshot struct {
	ConversationID                     string
	MembershipRevision, ClosedFrontier uint64
	Exchanges                          []domain.LogicalExchange
}

// Structural validation is separate from verifying acknowledgment evidence.
// ReadClosedExchangePrefix performs both before publishing a snapshot.
func closedMembershipPrefix(state domain.ConversationMembershipState, issuing domain.LogicalExchange, all []domain.LogicalExchange) (ClosedPrefixSnapshot, error) {
	invalid := func() (ClosedPrefixSnapshot, error) { return ClosedPrefixSnapshot{}, domain.ErrIncompleteCoverage }
	if state.Validate() != nil || issuing.Validate() != nil || state.SessionID != issuing.SessionID ||
		state.ConversationID != issuing.ConversationID || uint64(len(all)) != state.LastOrdinal ||
		issuing.Ordinal > state.LastOrdinal || state.ClosedFrontier != issuing.Ordinal-1 ||
		issuing.State != domain.ExchangeOpen && issuing.State != domain.ExchangeExecuting {
		return invalid()
	}
	ordered := slices.Clone(all)
	slices.SortFunc(ordered, func(a, b domain.LogicalExchange) int { return cmp.Compare(a.Ordinal, b.Ordinal) })
	ids := make(map[string]bool, len(ordered))
	for n, x := range ordered {
		if x.Validate() != nil || x.SessionID != state.SessionID || x.ConversationID != state.ConversationID ||
			x.Principal != issuing.Principal || x.Ordinal != uint64(n)+1 || ids[x.ID] ||
			x.Ordinal < issuing.Ordinal && x.State != domain.ExchangeClosed ||
			x.Ordinal == issuing.Ordinal && x != issuing {
			return invalid()
		}
		ids[x.ID] = true
	}
	return ClosedPrefixSnapshot{
		ConversationID: state.ConversationID, MembershipRevision: state.Revision,
		ClosedFrontier: state.ClosedFrontier, Exchanges: ordered[:int(state.ClosedFrontier)],
	}, nil
}
