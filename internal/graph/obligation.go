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
// (D17, R9). A Pinned directive declares at most one obligation slot, so a
// real source never approaches it; exceeding it fails the replacement
// (store.ErrLimitExceeded) rather than retiring only some.
const maxBoundObligations = 256

// planObligationRetirement finds every current obligation version bound to
// the source oldID (FR-OBL-006, D13) through the store's bounded
// by-source lookup (R9), and authorizes retiring each as an indirect effect
// of replacing that source (FR-AUTH-001): actor must access it and hold
// authority at least its source authority, or an in-force replace_directive
// grant naming it. It writes nothing, so a denial fails the replacement
// before anything is written.
func planObligationRetirement(tx store.Tx, actor domain.Principal, oldID string) ([]obligationRetirement, error) {
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
	if len(bound) == 0 {
		return nil, nil
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
