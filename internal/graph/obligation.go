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
}

// planObligationRetirement finds every current obligation version bound to
// the source oldID (FR-OBL-006, D13) and authorizes retiring each as an
// indirect effect of replacing that source (FR-AUTH-001): actor must access
// it and hold authority at least its source authority, or an in-force
// replace_directive grant naming it. It writes nothing, so a denial fails
// the replacement before anything is written. Every version is considered,
// not only the latest, since an older version may still be current.
func planObligationRetirement(tx store.ReadTx, actor domain.Principal, oldID string) ([]obligationRetirement, error) {
	latest, err := tx.Obligations("")
	if err != nil {
		return nil, err
	}
	var bound []domain.ObligationVersion
	for _, l := range latest {
		versions, err := tx.ObligationVersions(l.ObligationID)
		if err != nil {
			return nil, err
		}
		for _, v := range versions {
			if v.Current && v.SourceItemID == oldID {
				bound = append(bound, v)
			}
		}
	}
	if len(bound) == 0 {
		return nil, nil
	}

	grants, err := tx.Grants()
	if err != nil {
		return nil, err
	}
	plan := make([]obligationRetirement, 0, len(bound))
	for _, v := range bound {
		auth, err := domain.AuthorizeMutation(domain.MutationRequest{
			Actor:   actor,
			Action:  domain.ActionReplaceDirective,
			Targets: []domain.MutationTarget{{ID: v.ObligationID, Authority: v.SourceAuthority, Access: v.Access}},
			Grants:  grants,
			Seq:     tx.LastSeq() + 1,
		})
		if err != nil {
			return nil, err
		}
		plan = append(plan, obligationRetirement{version: v, grantID: auth.GrantIDs[v.ObligationID]})
	}
	return plan, nil
}

// retireObligations marks each planned version noncurrent at a fresh
// sequence number and appends its audit record, preserving its status,
// evidence, and transition history (FR-OBL-006). Nothing replaces it here:
// a new UNRESOLVED version exists only if the new source declares one, and
// that is the ingesting caller's decision (D13).
func retireObligations(tx store.Tx, actor domain.Principal, plan []obligationRetirement, oldID, newID, eventID string) error {
	for _, r := range plan {
		seq := tx.NextSeq()
		o := r.version.Clone()
		o.Current = false
		o.RetiredSeq = seq
		if _, err := tx.UpdateObligationVersion(o, r.version.Revision); err != nil {
			return err
		}
		ev := domain.LifecycleEvent{
			ID:         obligationAuditID(actor.SessionID, o.ObligationID, o.Version, "retired", eventID, newID),
			SessionID:  actor.SessionID,
			Seq:        seq,
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
		if err := tx.AppendLifecycleEvent(ev); err != nil {
			return err
		}
	}
	return nil
}
