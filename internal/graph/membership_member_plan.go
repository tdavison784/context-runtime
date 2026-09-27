package graph

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func planExchangeMember(tx store.ReadTx, sem store.SemanticReader, x domain.LogicalExchange, intent domain.RegisterExchangeMemberIntent, policy domain.Phase3Policy) (domain.ConversationMembershipState, error) {
	var state domain.ConversationMembershipState
	if x.Revision != intent.ExpectedRevision {
		return state, domain.ErrVersionConflict
	}
	if x.State != domain.ExchangeOpen && x.State != domain.ExchangeExecuting {
		return state, domain.ErrInvalidTransition
	}
	// Reserve the fixed reads/writes; the rest bounds the complete member list.
	if policy.MaxTransactionWork <= 12 || intent.Position > uint64(policy.MaxCoverageMembers) {
		return state, domain.ErrResourceLimit
	}
	limit := min(policy.MaxCoverageMembers, policy.MaxTransactionWork-12)
	members, err := collectMembershipPages(policy.MaxPageSize, limit, func(page store.Page) (store.ResultPage[domain.ExchangeMember], error) {
		return sem.ExchangeMembers(x.ID, page)
	})
	if err != nil {
		return state, err
	}
	if err = checkMemberAssociation(x, intent, members); err != nil {
		return state, err
	}
	source, err := tx.Item(intent.Source.ItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return state, domain.ErrNotFound
	}
	if err != nil {
		return state, err
	}
	var call *domain.CallRecord
	if intent.CallID != "" {
		c, err := tx.Call(intent.CallID)
		if err != nil {
			return state, incompleteMembership(err)
		}
		call = &c
	}
	if err = checkMemberSource(x, intent, source, call); err != nil {
		return state, err
	}
	state, err = sem.ConversationMembership(x.ConversationID)
	if err != nil {
		return state, incompleteMembership(err)
	}
	if state.Validate() != nil || state.SessionID != x.SessionID || state.ConversationID != x.ConversationID || state.LastOrdinal < x.Ordinal || state.ClosedFrontier >= x.Ordinal {
		return state, domain.ErrIncompleteCoverage
	}
	if state.Revision == ^uint64(0) || x.Revision == ^uint64(0) {
		return state, domain.ErrResourceLimit
	}
	if intent.AdmissionID != "" {
		m, err := sem.AdmissionManifest(intent.AdmissionID)
		if err != nil {
			return state, incompleteMembership(err)
		}
		if m.Validate() != nil || m.ID != intent.AdmissionID || m.SessionID != x.SessionID || m.ExchangeID != x.ID || m.ConversationID != x.ConversationID || m.Principal != x.Principal || m.TurnID != x.TurnID || m.MembershipRevision > state.Revision {
			return state, domain.ErrIncompleteCoverage
		}
	}
	return state, nil
}
