package obligation

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// SetMaterializationTx records or clears an FR-OBL-003 materialization
// exception on an exact current obligation version (P3-18). The actor needs
// direct authority at least the obligation's source authority, or a live
// set_obligation_materialization grant naming that exact version; AGENT,
// TOOL, and RETRIEVED_CONTENT never hold either. The exception affects
// rendering only: status, proof, waiver, protection, and completion checks
// ignore it.
func (s *Service) SetMaterializationTx(tx store.Tx, actor domain.Principal, in domain.SetObligationMaterializationIntent, seq uint64) (domain.MutationResult, error) {
	if err := in.Validate(); err != nil {
		return domain.MutationResult{}, err
	}
	sem, err := begin(tx, actor, seq)
	if err != nil {
		return domain.MutationResult{}, err
	}
	req, err := s.newRequest(domain.MutationObligationMaterialization, in.RequestID, "SetObligationMaterialization", in)
	if err != nil {
		return domain.MutationResult{}, err
	}
	if res, ok, err := replay(tx, sem, actor, req); ok || err != nil {
		return res, err
	}
	if in.Target.SessionID != actor.SessionID {
		return domain.MutationResult{}, domain.ErrNotFound
	}
	target := in.Target.Target()
	auth, err := graph.AuthorizeAtSequence(tx, actor, domain.ActionSetObligationMaterialization, []domain.GrantTarget{target}, nil, seq, s.policy.MaxTargets)
	if err != nil {
		return domain.MutationResult{}, err
	}
	o, err := sem.ExactObligation(in.Target)
	if err != nil {
		return domain.MutationResult{}, notFound(err)
	}
	if o.Revision != in.ExpectedRevision {
		return domain.MutationResult{}, domain.ErrVersionConflict
	}
	if !o.Current || o.MaterializationDisabled == in.Disabled {
		return domain.MutationResult{}, domain.ErrInvalidTransition
	}
	from, to := "enabled", "disabled"
	if !in.Disabled {
		from, to = to, from
	}
	ev := domain.LifecycleEvent{
		ID:         recordID("oev_", "materialization", target.AuthorizationKey, in.RequestID),
		SessionID:  actor.SessionID,
		Seq:        seq,
		TargetKind: domain.TargetObligation,
		TargetID:   o.ObligationID,
		Action:     string(domain.ActionSetObligationMaterialization),
		From:       from,
		To:         to,
		Actor:      actor,
		GrantID:    auth.GrantIDs[target.AuthorizationKey],
		EventID:    in.RequestID,
		Reason:     "materialization exception",
	}
	w := &writes{tx: tx}
	w.start()
	if _, err := sem.SetObligationMaterialization(in.Target, in.Disabled, in.ExpectedRevision, ev); err != nil {
		return domain.MutationResult{}, w.fail(err)
	}
	result := domain.MutationResult{Records: &domain.RecordResult{Kind: "MATERIALIZATION", IDs: []string{ev.ID}}}
	if err := s.recordReceipt(tx, sem, actor, req, seq, result); err != nil {
		return domain.MutationResult{}, w.fail(err)
	}
	return result, nil
}
