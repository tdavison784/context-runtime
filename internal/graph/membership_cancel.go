package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// CancelExchange records a trusted terminal outcome without claiming receipt
// by an inference. It cannot advance ClosedFrontier or relabel the old turn.
func (s *MembershipService) CancelExchange(tx store.Tx, actor domain.Principal, intent domain.CancelExchangeIntent, seq uint64) (result domain.RecordResult, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
		}
	}()
	if err = checkOperationSeq(tx, seq); err != nil {
		return result, err
	}
	sem, receipt, replay, err := prepareMembershipReceipt(tx, actor, intent.RequestID, "CancelExchange", intent, s.policy)
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
	if s.policy.MaxTransactionWork < 7 {
		return result, domain.ErrResourceLimit
	}
	x, err := readControlledExchange(sem, tx.SessionID(), actor, intent.ExchangeID)
	if err != nil {
		return result, err
	}
	if x.Revision != intent.ExpectedRevision {
		return result, domain.ErrVersionConflict
	}
	if x.State != domain.ExchangeOpen && x.State != domain.ExchangeExecuting {
		return result, domain.ErrInvalidTransition
	}
	state, err := sem.ConversationMembership(x.ConversationID)
	if err != nil {
		return result, incompleteMembership(err)
	}
	if state.Validate() != nil || state.SessionID != tx.SessionID() || state.ConversationID != x.ConversationID || state.LastOrdinal < x.Ordinal || state.ClosedFrontier >= x.Ordinal {
		return result, domain.ErrIncompleteCoverage
	}
	if state.Revision == ^uint64(0) || x.Revision == ^uint64(0) {
		return result, domain.ErrResourceLimit
	}
	ack := domain.ExchangeAcknowledgment{
		SemanticMeta: membershipMeta(tx.SessionID(), membershipID("cancellation", tx.SessionID(), receipt.ID), seq),
		ExchangeID:   x.ID, CancellationReason: intent.Reason, Cancelled: true, Actor: actor,
	}
	if err = sem.InsertExchangeAcknowledgment(ack); err != nil {
		return result, err
	}
	x.State, x.AcknowledgmentID = domain.ExchangeCancelled, ack.ID
	if _, err = sem.PutLogicalExchange(x, intent.ExpectedRevision); err != nil {
		return result, err
	}
	state.Seq = seq
	if _, err = sem.PutConversationMembership(state, state.Revision); err != nil {
		return result, err
	}
	result = domain.RecordResult{Kind: "MEMBERSHIP", IDs: []string{ack.ID}}
	err = finishMembershipReceipt(tx, sem, receipt, result, s.policy)
	return result, err
}
