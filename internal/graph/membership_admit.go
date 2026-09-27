package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// AdmitExchange freezes what the recipient actually received for one consumed
// exchange (P3-7). GENERATION_INPUT names the completed inference that consumed
// it; the manifest is not a claim that a provider request was transmitted.
func (s *MembershipService) AdmitExchange(tx store.Tx, actor domain.Principal, intent domain.AdmitExchangeIntent, seq uint64) (result domain.RecordResult, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
		}
	}()
	if err = checkOperationSeq(tx, seq); err != nil {
		return result, err
	}
	sem, receipt, replay, err := prepareMembershipReceipt(tx, actor, intent.RequestID, "AdmitExchange", intent, s.policy)
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
	if intent.Purpose == domain.AdmissionGenerationInput && intent.CallID == "" {
		return result, domain.ErrInvalidRecord
	}
	// Fixed reads/writes, then one read per complete member and its source.
	if s.policy.MaxTransactionWork <= 10 {
		return result, domain.ErrResourceLimit
	}
	x, err := readControlledExchange(sem, tx.SessionID(), actor, intent.ExchangeID)
	if err != nil {
		return result, err
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
	if state.Revision != intent.ExpectedMembershipRevision {
		return result, domain.ErrVersionConflict
	}
	if state.Revision == ^uint64(0) {
		return result, domain.ErrResourceLimit
	}
	if intent.CallID != "" {
		call, err := tx.Call(intent.CallID)
		if err != nil {
			return result, incompleteMembership(err)
		}
		if err = checkConsumingInference(x, call); err != nil {
			return result, err
		}
	}
	if err = checkAdmissionCoverage(tx, sem, x, intent.CoverageID, s.policy.MaxPageSize, min(s.policy.MaxCoverageMembers, (s.policy.MaxTransactionWork-10)/2)); err != nil {
		return result, err
	}
	m := domain.AdmissionManifest{
		SemanticMeta:   membershipMeta(tx.SessionID(), membershipID("admission", tx.SessionID(), receipt.ID), seq),
		ConversationID: x.ConversationID, ExchangeID: x.ID, CallID: intent.CallID, Principal: x.Principal,
		TurnID: x.TurnID, Purpose: intent.Purpose, CoverageID: intent.CoverageID,
		MembershipRevision: state.Revision, PolicyVersion: s.policy.Version,
	}
	if err = sem.InsertAdmissionManifest(m); err != nil {
		return result, err
	}
	state.Seq = seq
	if _, err = sem.PutConversationMembership(state, state.Revision); err != nil {
		return result, err
	}
	result = domain.RecordResult{Kind: "MEMBERSHIP", IDs: []string{m.ID}}
	err = finishMembershipReceipt(tx, sem, receipt, result, s.policy)
	return result, err
}

// A consuming inference is a completed inference of the same recipient,
// dispatched by an exact-owner trusted actor. Other operations never admit.
func checkConsumingInference(x domain.LogicalExchange, call domain.CallRecord) error {
	if call.Validate() != nil || call.SessionID != x.SessionID || call.ConversationID != x.ConversationID || call.Principal != x.Principal ||
		call.Operation != domain.OperationInference || call.State != domain.CallCompleted ||
		checkMembershipControl(x.SessionID, call.ServiceActor, x.Principal) != nil {
		return domain.ErrInvalidTransition
	}
	return nil
}

// Admission coverage is exactly GENERATION_INPUT at the recipient's boundary,
// complete within finite bounds, and names only sources the recipient can read.
func checkAdmissionCoverage(tx store.ReadTx, sem store.SemanticReader, x domain.LogicalExchange, id string, pageSize, limit int) error {
	c, err := sem.Coverage(id)
	if err != nil {
		return incompleteMembership(err)
	}
	if c.Validate() != nil || c.ID != id || c.SessionID != x.SessionID || c.Purpose != domain.CoverageGenerationInput || !c.Access.Permits(x.Principal) {
		return domain.ErrIncompleteCoverage
	}
	if c.MemberCount > uint64(max(limit, 0)) {
		return domain.ErrResourceLimit
	}
	members, err := collectMembershipPages(pageSize, limit, func(page store.Page) (store.ResultPage[domain.CoverageMember], error) {
		return sem.CoverageMembers(id, page)
	})
	if err != nil {
		return err
	}
	if uint64(len(members)) != c.MemberCount {
		return domain.ErrIncompleteCoverage
	}
	if sig, err := domain.CoverageSignature(c, members); err != nil || sig != c.Signature {
		return domain.ErrIncompleteCoverage
	}
	for _, m := range members {
		if m.ExchangeID != "" {
			return domain.ErrIncompleteCoverage // admission names exact sources, never a round
		}
		if m.Source == nil {
			continue // nested lease coverage is verified by its own producer
		}
		source, err := tx.Item(m.Source.ItemID)
		if err != nil {
			return incompleteMembership(err)
		}
		if source.SessionID != x.SessionID || !source.Access.Permits(x.Principal) || source.ContentHash != m.Source.ContentHash {
			return domain.ErrIncompleteCoverage
		}
	}
	return nil
}
