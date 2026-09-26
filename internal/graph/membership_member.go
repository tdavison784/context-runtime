package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// RegisterExchangeMember authenticates explicit receipt/output membership. An
// OUTPUT starts EXECUTING; TOOL_CALLs name that exact output source and call.
// Further members advance conversation membership, not the exchange state CAS.
func (s *MembershipService) RegisterExchangeMember(tx store.Tx, actor domain.Principal, intent domain.RegisterExchangeMemberIntent, seq uint64) (result domain.RecordResult, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
		}
	}()
	if err = checkOperationSeq(tx, seq); err != nil {
		return result, err
	}
	sem, receipt, replay, err := prepareMembershipReceipt(tx, actor, intent.RequestID, "RegisterExchangeMember", intent, s.policy)
	if err != nil {
		return result, err
	}
	if replay {
		return receipt.Result.Records.Clone(), nil
	}
	if err = intent.Validate(); err != nil {
		return result, err
	}
	x, err := readControlledExchange(sem, tx.SessionID(), actor, intent.ExchangeID)
	if err != nil {
		return result, err
	}
	state, err := planExchangeMember(tx, sem, x, intent, s.policy)
	if err != nil {
		return result, err
	}
	m := domain.ExchangeMember{
		SemanticMeta: membershipMeta(tx.SessionID(), membershipID("member", tx.SessionID(), receipt.ID), seq),
		ExchangeID:   x.ID, Position: intent.Position, Role: intent.Role, Source: intent.Source,
		CallID: intent.CallID, ToolCallID: intent.ToolCallID, AdmissionID: intent.AdmissionID,
	}
	if err = sem.InsertExchangeMember(m); err != nil {
		return result, err
	}
	if intent.Role == domain.MemberOutput {
		x.State = domain.ExchangeExecuting
		if _, err = sem.PutLogicalExchange(x, intent.ExpectedRevision); err != nil {
			return result, err
		}
	}
	state.Seq = seq
	if _, err = sem.PutConversationMembership(state, state.Revision); err != nil {
		return result, err
	}
	result = domain.RecordResult{Kind: "MEMBERSHIP", IDs: []string{m.ID}}
	err = finishMembershipReceipt(tx, sem, receipt, result, s.policy)
	return result, err
}
