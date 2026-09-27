package obligation

import (
	"errors"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Rule versions of runtime-caused transitions.
const (
	ResourceInvalidationRule = "resource-invalidation/v1"
	ProofRejectionRule       = "proof-rejection/v1"
)

// under reports whether p is q or lies inside directory q.
func under(p, q string) bool {
	return p == q || strings.HasPrefix(p, q+"/")
}

// invalidation is the recorded cause of a restricted runtime transition.
type invalidation struct {
	cause       domain.TransitionCause // RESOURCE_INVALIDATION or PROOF_REJECTED
	causeRecord string                 // resource update or rejecting observation
	requestID   string
	reason      domain.ObligationReasonCode
	rule        string
}

// invalidateProof is the restricted consequence path (C-10): it can only move
// the exact current SATISFIED version whose current proof is p to UNRESOLVED.
// It exercises no grant; the original authorization is recorded as history,
// separately from the actual actor. It cannot waive, block, satisfy, or touch
// any other target, and a version no longer resting on p is left alone.
func (s *Service) invalidateProof(tx store.Tx, sem store.SemanticTx, work *budget, actor domain.Principal, seq uint64, p domain.ApplicabilityProof, inv invalidation) error {
	o, err := sem.ExactObligation(p.Target)
	if err != nil {
		return err
	}
	id := recordID("otr_", string(inv.cause), p.Target.Target().AuthorizationKey, inv.causeRecord)
	_, err = s.releaseProof(tx, sem, work, actor, seq, o, p, inv, id)
	return err
}

// releaseProof writes the restricted SATISFIED->UNRESOLVED transition of
// version o off proof p with the given identity and returns the updated
// version. A version that is not current or no longer rests on p is
// returned unchanged: a current proof is recorded only while SATISFIED, so
// no stored-status comparison is needed (K1 A2).
func (s *Service) releaseProof(tx store.Tx, sem store.SemanticTx, work *budget, actor domain.Principal, seq uint64, o domain.ObligationVersion, p domain.ApplicabilityProof, inv invalidation, id string) (domain.ObligationVersion, error) {
	if !o.Current || o.CurrentProofID == "" || o.CurrentProofID != p.ID {
		return o, nil
	}
	origin, err := s.originOf(sem, work, o, p.TransitionID)
	if err != nil {
		return o, err
	}
	t := domain.ObligationTransition{
		Cause:                  inv.cause,
		PriorProofID:           p.ID,
		CauseRecordID:          inv.causeRecord,
		RequestID:              inv.requestID,
		OriginAuthorizationRef: &origin,
		ReasonCode:             inv.reason,
		ID:                     id,
		SessionID:              o.SessionID,
		ObligationID:           o.ObligationID,
		Version:                o.Version,
		Seq:                    seq,
		From:                   domain.ObligationSatisfied,
		To:                     domain.ObligationUnresolved,
		Action:                 domain.ActionAssertObligation,
		Actor:                  actor,
	}
	d := domain.TransitionDetail{
		SemanticMeta:        domain.SemanticMeta{ID: t.ID, SessionID: o.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Target:              p.Target,
		TransitionID:        t.ID,
		Cause:               inv.cause,
		PreviousProofID:     p.ID,
		RuleVersion:         inv.rule,
		OriginAuthorization: &origin,
	}
	if inv.cause == domain.CauseResourceInvalidation {
		d.ResourceUpdateID = inv.causeRecord
	} else {
		d.ObservationID = inv.causeRecord
	}
	after, err := appendTransition(tx, sem, o, t, d, o.Revision)
	if err != nil {
		tx.Poison(err)
		return o, err
	}
	return after, nil
}

// originOf returns the historical authorization of the transition that
// installed a proof: one keyed read, whatever the version's accumulated
// history (H2, DUR-2.3, XREV-2.1).
func (s *Service) originOf(r store.SemanticReader, work *budget, o domain.ObligationVersion, transitionID string) (domain.OriginAuthorizationRef, error) {
	ref := domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version}
	if err := work.spend(1); err != nil {
		return domain.OriginAuthorizationRef{}, err
	}
	found, err := r.ObligationTransition(transitionID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.OriginAuthorizationRef{}, domain.ErrIntegrity
	}
	if err != nil {
		return domain.OriginAuthorizationRef{}, err
	}
	if found.SessionID != ref.SessionID || found.ObligationID != ref.ObligationID || found.Version != ref.Version {
		return domain.OriginAuthorizationRef{}, domain.ErrIntegrity
	}
	return domain.OriginAuthorizationRef{
		TransitionID: found.ID, GrantID: found.GrantID, Actor: found.Actor,
		Target: ref,
		Seq:    found.Seq,
	}, nil
}
