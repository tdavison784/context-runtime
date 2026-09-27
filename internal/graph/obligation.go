package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// obligationRetirement is one obligation version a supersession must
// retire, with the grant (if any) that authorized retiring it.
type obligationRetirement struct {
	version domain.ObligationVersion
	grantID string
	seq     uint64 // allocated once; authorization and retirement use this exact seq
}

// maxBoundObligations bounds the obligation versions one source may carry
// (D17, R9): domain.MaxObligationsPerSource, which declarations never exceed
// (Phase3Policy.ObligationDeclarationLimit, DUR-1.5); exceeding it fails the
// replacement (store.ErrLimitExceeded) rather than retiring only some.
const maxBoundObligations = domain.MaxObligationsPerSource

// planObligationRetirement finds every current obligation version bound to
// the source oldID (FR-OBL-006, D13) through the store's bounded
// by-source lookup (R9), and authorizes retiring each as an indirect effect
// of replacing that source (FR-AUTH-001): actor must access it and hold
// authority at least its source authority, or an in-force replace_directive
// grant naming it. It writes nothing, so a denial fails the replacement
// before anything is written.
//
// Before anything is planned, each current version is settled (M2, K1 A3):
// the injected settler writes a pending RESOURCE_INVALIDATION in this
// transaction, so history never reads SATISFIED -> retired over a pending
// settlement, and the versions are read again because settling bumps their
// revision. Without a settler a derived-invalid SATISFIED version is refused.
func planObligationRetirement(tx store.Tx, actor domain.Principal, oldID string, settler PendingSettler) ([]obligationRetirement, error) {
	bound, err := currentBoundVersions(tx, oldID)
	if err != nil || len(bound) == 0 {
		return nil, err
	}
	if err := settleBeforeRetirement(tx, settler, bound); err != nil {
		return nil, err
	}
	if settler != nil {
		if bound, err = currentBoundVersions(tx, oldID); err != nil || len(bound) == 0 {
			return nil, err
		}
	}

	plan := make([]obligationRetirement, 0, len(bound))
	for _, v := range bound {
		seq := tx.NextSeq()
		target := domain.ObligationGrantTarget(tx.SessionID(), v.ObligationID, v.Version)
		auth, err := AuthorizeAtSequence(tx, actor, domain.ActionReplaceDirective, []domain.GrantTarget{target}, nil, seq, maxBoundObligations)
		if err != nil {
			return nil, err
		}
		plan = append(plan, obligationRetirement{version: v, grantID: auth.GrantIDs[target.AuthorizationKey], seq: seq})
	}
	return plan, nil
}

// retireObligations retires each planned version with its audit record in
// one store write (RetireObligationVersion), preserving its status,
// evidence, and transition history (FR-OBL-006). Nothing replaces it here:
// a new UNRESOLVED version exists only if the new source declares one, and
// that is the ingesting caller's decision (D13, R9).
func retireObligations(tx store.Tx, actor domain.Principal, plan []obligationRetirement, oldID, newID, eventID string) error {
	for _, r := range plan {
		o := r.version
		ev := domain.LifecycleEvent{
			ID:         obligationAuditID(actor.SessionID, o.ObligationID, o.Version, "retired", eventID, newID),
			SessionID:  actor.SessionID,
			Seq:        r.seq,
			TargetKind: domain.TargetObligation,
			TargetID:   o.ObligationID,
			Action:     "retired",
			From:       oldID,
			To:         newID,
			Actor:      actor,
			GrantID:    r.grantID,
			EventID:    eventID,
			Reason:     "source directive superseded",
		}
		if _, err := tx.RetireObligationVersion(o.ObligationID, o.Version, o.Revision, ev); err != nil {
			return err
		}
	}
	return nil
}

// currentBoundVersions are the current obligation versions bound to oldID.
func currentBoundVersions(tx store.Tx, oldID string) ([]domain.ObligationVersion, error) {
	versions, err := tx.ObligationsBySource(oldID, maxBoundObligations)
	if err != nil {
		return nil, err
	}
	var bound []domain.ObligationVersion
	for _, v := range versions {
		if v.Current {
			bound = append(bound, v)
		}
	}
	return bound, nil
}

// settleBeforeRetirement hands every current version to the settler, which
// settles a pending invalidation or does nothing. With no settler it fails
// closed: a stored-SATISFIED version whose proof is derived invalid (or
// unreadable) is never retired (ErrPendingSettlement). This is settlement
// machinery, the one graph reader of stored status (K1 A2 allowlist).
func settleBeforeRetirement(tx store.Tx, settler PendingSettler, bound []domain.ObligationVersion) error {
	for _, v := range bound {
		if settler != nil {
			ref := domain.ObligationRef{SessionID: v.SessionID, ObligationID: v.ObligationID, Version: v.Version}
			if err := settler.SettleBeforeRetireTx(tx, ref); err != nil {
				return err
			}
			continue
		}
		if v.Status != domain.ObligationSatisfied || v.CurrentProofID == "" {
			continue
		}
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		valid, err := proofDerivedValid(r, v.CurrentProofID)
		if err != nil {
			return err
		}
		if !valid {
			return ErrPendingSettlement
		}
	}
	return nil
}
