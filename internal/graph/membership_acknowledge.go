package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// AcknowledgeExchange closes one complete round with the GENERATION_INPUT
// manifest of the later completed inference that consumed it, and advances
// the contiguous closed frontier in the same write. Rounds close in order, so
// a gap never becomes a prefix; cancellation is never closure (P3-7).
func (s *MembershipService) AcknowledgeExchange(tx store.Tx, actor domain.Principal, intent domain.AcknowledgeExchangeIntent) (result domain.RecordResult, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
		}
	}()
	sem, receipt, replay, err := prepareMembershipReceipt(tx, actor, intent.RequestID, "AcknowledgeExchange", intent, s.policy)
	if err != nil {
		return result, err
	}
	if replay {
		return receipt.Result.Records.Clone(), nil
	}
	if err = intent.Validate(); err != nil {
		return result, err
	}
	// Reserve the fixed reads/writes; the rest bounds the complete member list.
	if s.policy.MaxTransactionWork <= 12 {
		return result, domain.ErrResourceLimit
	}
	seq := tx.NextSeq()
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
	if state.Validate() != nil || state.SessionID != tx.SessionID() || state.ConversationID != x.ConversationID || state.LastOrdinal < x.Ordinal {
		return result, domain.ErrIncompleteCoverage
	}
	if state.ClosedFrontier != x.Ordinal-1 {
		return result, domain.ErrInvalidTransition
	}
	if state.Revision == ^uint64(0) || x.Revision == ^uint64(0) {
		return result, domain.ErrResourceLimit
	}
	manifest, err := sem.AdmissionManifest(intent.ManifestID)
	if err != nil {
		return result, incompleteMembership(err)
	}
	call, err := tx.Call(intent.ConsumingCallID)
	if err != nil {
		return result, incompleteMembership(err)
	}
	members, err := collectMembershipPages(s.policy.MaxPageSize, min(s.policy.MaxCoverageMembers, s.policy.MaxTransactionWork-12), func(page store.Page) (store.ResultPage[domain.ExchangeMember], error) {
		return sem.ExchangeMembers(x.ID, page)
	})
	if err != nil {
		return result, err
	}
	if err = checkRoundConsumed(x, members, call); err != nil {
		return result, err
	}
	ack := domain.ExchangeAcknowledgment{
		SemanticMeta: membershipMeta(tx.SessionID(), membershipID("acknowledgment", tx.SessionID(), receipt.ID), seq),
		ExchangeID:   x.ID, ManifestID: manifest.ID, ConsumingCallID: call.CallID, Actor: actor,
	}
	closed := x
	closed.State, closed.AcknowledgmentID = domain.ExchangeClosed, ack.ID
	if manifest.ID != intent.ManifestID || manifest.MembershipRevision > state.Revision {
		return result, domain.ErrIncompleteCoverage
	}
	if err = checkMembershipAcknowledgment(closed, ack, manifest, call); err != nil {
		return result, err
	}
	if err = sem.InsertExchangeAcknowledgment(ack); err != nil {
		return result, err
	}
	if _, err = sem.PutLogicalExchange(closed, intent.ExpectedRevision); err != nil {
		return result, err
	}
	state.Seq, state.ClosedFrontier = seq, x.Ordinal
	if _, err = sem.PutConversationMembership(state, state.Revision); err != nil {
		return result, err
	}
	result = domain.RecordResult{Kind: "MEMBERSHIP", IDs: []string{ack.ID}}
	err = finishMembershipReceipt(tx, sem, receipt, result, s.policy)
	return result, err
}

// A round is consumed only when it is complete: one output, a result for every
// tool call, and a later inference prepared after its last recorded member.
// The round's own producing inference never acknowledges it.
func checkRoundConsumed(x domain.LogicalExchange, members []domain.ExchangeMember, call domain.CallRecord) error {
	var output *domain.ExchangeMember
	calls, results := map[string]bool{}, map[string]bool{}
	positions := make(map[uint64]bool, len(members))
	for n := range members {
		m := &members[n]
		if m.Validate() != nil || m.ExchangeID != x.ID || m.SessionID != x.SessionID || m.Position == 0 || m.Position > uint64(len(members)) || positions[m.Position] {
			return domain.ErrIncompleteCoverage
		}
		positions[m.Position] = true
		if m.Seq >= call.PreparedSeq {
			return domain.ErrIncompleteCoverage
		}
		key := m.CallID + "\x00" + m.ToolCallID
		switch m.Role {
		case domain.MemberOutput:
			if output != nil {
				return domain.ErrIncompleteCoverage
			}
			output = m
		case domain.MemberToolCall:
			calls[key] = true
		case domain.MemberToolResult:
			results[key] = true
		}
	}
	if output == nil || output.CallID == call.CallID || len(calls) != len(results) {
		return domain.ErrIncompleteCoverage
	}
	for key := range calls {
		if !results[key] {
			return domain.ErrIncompleteCoverage
		}
	}
	return nil
}
