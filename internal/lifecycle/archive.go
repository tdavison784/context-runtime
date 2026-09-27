package lifecycle

import (
	"context"
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Archive and Unarchive implement the transaction executor for explicit
// archival (P3-37). They change only Residency; generation, authority,
// content, goal/obligation status, currentness, TTL and ownership persist.
func (s *Service) Archive(tx store.Tx, p domain.Principal, i domain.ArchiveIntent, seq uint64) (LifecycleOutcome, error) {
	return s.executeItem(tx, p, s.residencyOp(i, domain.ActionArchive), seq)
}

func (s *Service) Unarchive(tx store.Tx, p domain.Principal, i domain.UnarchiveIntent, seq uint64) (LifecycleOutcome, error) {
	return s.executeItem(tx, p, s.residencyOp(i, domain.ActionUnarchive), seq)
}

func (s *Service) ArchiveStandalone(ctx context.Context, p domain.Principal, i domain.ArchiveIntent) (domain.ItemMutationResult, error) {
	return s.standaloneItem(ctx, p, s.residencyOp(i, domain.ActionArchive))
}

func (s *Service) UnarchiveStandalone(ctx context.Context, p domain.Principal, i domain.UnarchiveIntent) (domain.ItemMutationResult, error) {
	return s.standaloneItem(ctx, p, s.residencyOp(i, domain.ActionUnarchive))
}

func (s *Service) residencyOp(i domain.ItemMutationIntent, action domain.Action) itemOp {
	return itemOp{action: action, requestID: i.RequestID, intent: i, plan: func(tx store.Tx, p domain.Principal, seq uint64) (itemEffect, error) {
		return s.executeResidency(tx, p, i, action, seq)
	}}
}

// accessibleTarget loads an exact occurrence for a lifecycle actor. Missing
// and inaccessible targets are indistinguishable; AGENT, TOOL and
// RETRIEVED_CONTENT learn nothing further and never hold lifecycle power.
func accessibleTarget(tx store.Tx, p domain.Principal, i domain.ItemMutationIntent, seq uint64) (domain.ContextItem, error) {
	if err := i.Validate(); err != nil {
		return domain.ContextItem{}, err
	}
	if !tx.Allocated(seq) || seq == 0 {
		return domain.ContextItem{}, domain.ErrInvalidRecord
	}
	if p.SessionID != tx.SessionID() {
		return domain.ContextItem{}, domain.ErrNotFound
	}
	it, err := tx.Item(i.ItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ContextItem{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.ContextItem{}, err
	}
	if !it.Access.Permits(p) {
		return domain.ContextItem{}, domain.ErrNotFound
	}
	if !p.Authority.CanHoldLifecycleAuthority() {
		return domain.ContextItem{}, domain.ErrInvalidAuthorityPromotion
	}
	if it.Version != i.ExpectedVersion {
		return domain.ContextItem{}, domain.ErrVersionConflict
	}
	if it.Version == ^uint64(0) {
		return domain.ContextItem{}, domain.ErrResourceLimit
	}
	return it, nil
}

func (s *Service) executeResidency(tx store.Tx, p domain.Principal, i domain.ItemMutationIntent, action domain.Action, seq uint64) (itemEffect, error) {
	it, err := accessibleTarget(tx, p, i, seq)
	if err != nil {
		return itemEffect{}, err
	}
	from, to := domain.ResidencyResident, domain.ResidencyArchived
	if action == domain.ActionUnarchive {
		from, to = to, from
	} else if action != domain.ActionArchive {
		return itemEffect{}, domain.ErrInvalidRecord
	}
	if it.Residency != from {
		return itemEffect{}, graph.ErrLifecycleTargetMismatch
	}
	current, err := currentness(tx, it)
	if err != nil {
		return itemEffect{}, err
	}
	protected := false
	if action == domain.ActionArchive {
		if protected, err = s.protectedRequirement(tx, it, current); err != nil {
			return itemEffect{}, err
		}
	}
	target := domain.ItemGrantTarget(p.SessionID, it.ID)
	auth, err := graph.AuthorizeAtSequence(tx, p, action, []domain.GrantTarget{target}, nil, seq, s.policy.MaxTargets)
	if err != nil {
		return itemEffect{}, err
	}
	id := "life_" + domain.NewCanonicalEncoder("context-runtime/lifecycle-audit/v1").String(p.SessionID).String(i.RequestID).String(string(action)).String(it.ID).Hash()
	ev := domain.LifecycleEvent{ID: id, SessionID: p.SessionID, Seq: seq, TargetKind: domain.TargetItem, TargetID: it.ID,
		Action: string(action), From: string(from), To: string(to), Actor: p, GrantID: auth.GrantIDs[target.AuthorizationKey]}
	after, err := tx.UpdateItem(it.ID, i.ExpectedVersion, domain.ItemChange{Residency: &to}, ev)
	if err != nil {
		return itemEffect{}, err
	}
	return itemEffect{before: it, after: after, audit: ev, current: current, protectedRemoval: protected}, nil
}

// currentness classifies an occurrence for frozen results. Unkeyed items are
// current merely by not being superseded; duplicate detection never retires
// them (D10).
func currentness(tx store.ReadTx, it domain.ContextItem) (domain.ItemCurrentness, error) {
	current, err := graph.IsCurrent(tx, it.ID)
	if err != nil {
		return "", err
	}
	switch {
	case it.DirectiveID == "" && current:
		return domain.ItemUnkeyed, nil
	case current:
		return domain.ItemCurrent, nil
	case it.DirectiveID == "":
		return domain.ItemHistorical, nil
	}
	dup, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDuplicateOf, FromID: it.ID})
	if err != nil {
		return "", err
	}
	if len(dup) != 0 {
		return domain.ItemDuplicate, nil
	}
	return domain.ItemHistorical, nil
}

// protectedRequirement reports whether archiving it removes a current
// requirement from ordinary new selection: a pin, an OPEN goal, a SYSTEM
// instruction/constraint, or the source of a current unresolved/blocked
// obligation. The flag only discloses; it never substitutes for authority.
// Overflowing the bounded source read fails rather than guessing.
func (s *Service) protectedRequirement(tx store.ReadTx, it domain.ContextItem, current domain.ItemCurrentness) (bool, error) {
	if current != domain.ItemCurrent && current != domain.ItemUnkeyed {
		return false, nil
	}
	if it.Generation == domain.GenerationPinned || it.Kind == domain.KindGoal && it.GoalStatus != nil && *it.GoalStatus == domain.GoalOpen ||
		it.Authority == domain.AuthoritySystem && (it.Kind == domain.KindInstruction || it.Kind == domain.KindConstraint) {
		return true, nil
	}
	obs, err := tx.ObligationsBySource(it.ID, s.policy.MaxTargets)
	if errors.Is(err, store.ErrLimitExceeded) {
		return false, domain.ErrResourceLimit
	}
	if err != nil {
		return false, err
	}
	for _, o := range obs {
		if o.Current && (o.Status == domain.ObligationUnresolved || o.Status == domain.ObligationBlocked) {
			return true, nil
		}
	}
	return false, nil
}
