package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// AuthorizeAtSequence resolves exact stored targets and authorizes the actual
// allocated mutation sequence. It never predicts LastSeq()+1, scans Grants(),
// opens a transaction, or treats a caller-supplied row as authority (P3-1/5).
func AuthorizeAtSequence(tx store.Tx, actor domain.Principal, action domain.Action, refs []domain.GrantTarget, matcher *domain.MatcherRef, seq uint64, maxGrants int) (domain.Authorization, error) {
	if !tx.Allocated(seq) || seq == 0 || maxGrants <= 0 || len(refs) == 0 {
		return domain.Authorization{}, domain.ErrInvalidRecord
	}
	if err := actor.Validate(); err != nil {
		return domain.Authorization{}, err
	}
	var targets []domain.MutationTarget
	var grants []domain.MutationGrant
	seen := map[string]bool{}
	grantIDs := map[string]bool{}
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return domain.Authorization{}, err
		}
		if ref.SessionID != tx.SessionID() || ref.SessionID != actor.SessionID {
			return domain.Authorization{}, domain.ErrNotFound
		}
		if seen[ref.AuthorizationKey] {
			return domain.Authorization{}, domain.ErrInvalidRecord
		}
		seen[ref.AuthorizationKey] = true
		target := domain.MutationTarget{Ref: ref}
		if ref.Kind == domain.GrantTargetItem {
			item, err := loadAccessible(tx, actor, ref.ItemID)
			if err != nil {
				return domain.Authorization{}, err
			}
			target.ID, target.Authority, target.Access = item.ID, item.Authority, item.Access
		} else {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return domain.Authorization{}, err
			}
			o, err := r.ExactObligation(domain.ObligationRef{SessionID: ref.SessionID, ObligationID: ref.ObligationID, Version: ref.Version})
			if err != nil {
				return domain.Authorization{}, err
			}
			if !o.Access.Permits(actor) {
				return domain.Authorization{}, domain.ErrNotFound
			}
			target.ID, target.Authority, target.Access = o.ObligationID, o.SourceAuthority, o.Access
		}
		targets = append(targets, target)
		if matcher == nil && actor.Authority.CanHoldLifecycleAuthority() && actor.Authority.AtLeast(target.Authority) {
			continue
		}
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return domain.Authorization{}, err
		}
		// Only grants in force at seq count toward maxGrants, so revoked or
		// expired history never wedges a live grant (G2, SEC-1.5, DUR-1.4).
		found, err := r.LiveGrantsFor(action, ref, seq, maxGrants)
		if err != nil {
			return domain.Authorization{}, err
		}
		for _, grant := range found {
			if !grantIDs[grant.ID] {
				if len(grants) >= maxGrants {
					return domain.Authorization{}, store.ErrLimitExceeded
				}
				grantIDs[grant.ID] = true
				grants = append(grants, grant)
			}
		}
	}
	return domain.AuthorizeMutation(domain.MutationRequest{Actor: actor, Action: action, Targets: targets, Matcher: matcher, Grants: grants, Seq: seq})
}
