package graph

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// MembershipService accepts trusted typed intents in the caller's transaction.
// Failed operations poison that transaction, including errors a caller ignores.
type MembershipService struct{ policy domain.Phase3Policy }

func NewMembershipService(policy domain.Phase3Policy) (*MembershipService, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return &MembershipService{policy: policy}, nil
}

// RegisterExchange records a new originating turn and dense conversation ordinal.
// The returned immutable identity is replayed without re-reading the exchange.
func (s *MembershipService) RegisterExchange(tx store.Tx, actor domain.Principal, intent domain.RegisterExchangeIntent, seq uint64) (result domain.RecordResult, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
		}
	}()
	if err = checkOperationSeq(tx, seq); err != nil {
		return result, err
	}
	sem, receipt, replay, err := prepareMembershipReceipt(tx, actor, intent.RequestID, "RegisterExchange", intent, s.policy)
	if err != nil {
		return result, err
	}
	if replay {
		return receipt.Result.Records.Clone(), nil
	}
	seq = operationSeq(tx, seq)
	if err = intent.Validate(); err != nil {
		return result, err
	}
	if s.policy.MaxTransactionWork < 6 {
		return result, domain.ErrResourceLimit
	}
	if err = checkMembershipControl(tx.SessionID(), actor, intent.Principal); err != nil {
		return result, err
	}
	if _, err = checkMembershipTurn(tx, intent.Principal, intent.TurnID, intent.Turn); err != nil {
		return result, err
	}
	conv := domain.ConversationIDFor(intent.Principal.TaskID, intent.Principal.AgentID)
	state, err := sem.ConversationMembership(conv)
	if errors.Is(err, domain.ErrNotFound) {
		state = domain.ConversationMembershipState{SemanticMeta: membershipMeta(tx.SessionID(), membershipID("conversation", tx.SessionID(), conv), seq), ConversationID: conv}
	} else if err != nil {
		return result, err
	} else if state.Validate() != nil || state.SessionID != tx.SessionID() || state.ConversationID != conv {
		return result, domain.ErrIntegrity
	}
	if state.Revision != intent.ExpectedMembershipRevision {
		return result, domain.ErrVersionConflict
	}
	if state.Revision == ^uint64(0) || state.LastOrdinal == ^uint64(0) {
		return result, domain.ErrResourceLimit
	}
	x := domain.LogicalExchange{
		SemanticMeta:   membershipMeta(tx.SessionID(), membershipID("exchange", tx.SessionID(), receipt.ID), seq),
		ConversationID: conv, Ordinal: state.LastOrdinal + 1, Principal: intent.Principal,
		TurnID: intent.TurnID, Turn: intent.Turn, State: domain.ExchangeOpen, Revision: 1,
	}
	if err = sem.InsertLogicalExchange(x); err != nil {
		return result, err
	}
	state.Seq, state.LastOrdinal, state.Revision = seq, x.Ordinal, state.Revision+1
	if _, err = sem.PutConversationMembership(state, intent.ExpectedMembershipRevision); err != nil {
		return result, err
	}
	result = domain.RecordResult{Kind: "MEMBERSHIP", IDs: []string{x.ID}}
	err = finishMembershipReceipt(tx, sem, receipt, result, s.policy)
	return result, err
}
