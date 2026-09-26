package lifecycle

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

type itemEffect struct {
	before, after domain.ContextItem
	audit         domain.LifecycleEvent
	// current is the target's currentness, unchanged by every lifecycle
	// effect; protectedRemoval discloses an explicit archival of a protected
	// current requirement (P3-37).
	current          domain.ItemCurrentness
	protectedRemoval bool
}

// executeDirective is private: its caller must persist the effect receipt and
// semantic-change companion in this transaction before publishing success.
func (s *Service) executeDirective(tx store.Tx, p domain.Principal, i domain.ItemMutationIntent, action domain.Action, seq uint64) (itemEffect, error) {
	if err := i.Validate(); err != nil {
		return itemEffect{}, err
	}
	if !tx.Allocated(seq) || seq == 0 {
		return itemEffect{}, domain.ErrInvalidRecord
	}
	if p.SessionID != tx.SessionID() {
		return itemEffect{}, domain.ErrNotFound
	}
	it, err := tx.Item(i.ItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return itemEffect{}, domain.ErrNotFound
	}
	if err != nil {
		return itemEffect{}, err
	}
	if !it.Access.Permits(p) {
		return itemEffect{}, domain.ErrNotFound
	}
	if !p.Authority.CanHoldLifecycleAuthority() {
		return itemEffect{}, domain.ErrInvalidAuthorityPromotion
	}
	ns, keyed := it.DirectiveNamespace()
	if !keyed || ns != domain.NamespaceDirective {
		return itemEffect{}, domain.ErrNotFound
	}
	current, err := graph.IsCurrent(tx, it.ID)
	if err != nil {
		return itemEffect{}, err
	}
	if !current {
		return itemEffect{}, domain.ErrNotFound
	}
	if it.Version != i.ExpectedVersion {
		return itemEffect{}, domain.ErrVersionConflict
	}
	retention := domain.RetentionHigh
	change := domain.ItemChange{Retention: &retention}
	var from, to string
	switch action {
	case domain.ActionResolve:
		if it.Kind != domain.KindGoal || it.GoalStatus == nil || *it.GoalStatus != domain.GoalOpen {
			return itemEffect{}, graph.ErrLifecycleTargetMismatch
		}
		status := domain.GoalResolved
		change.GoalStatus, from, to = &status, string(domain.GoalOpen), string(status)
	case domain.ActionUnpin:
		if it.Generation != domain.GenerationPinned {
			return itemEffect{}, graph.ErrLifecycleTargetMismatch
		}
		generation := domain.GenerationDurable
		change.Generation, from, to = &generation, string(it.Generation), string(generation)
	default:
		return itemEffect{}, domain.ErrInvalidRecord
	}
	target := domain.ItemGrantTarget(p.SessionID, it.ID)
	auth, err := graph.AuthorizeAtSequence(tx, p, action, []domain.GrantTarget{target}, nil, seq, s.policy.MaxTargets)
	if err != nil {
		return itemEffect{}, err
	}
	id := "life_" + domain.NewCanonicalEncoder("context-runtime/lifecycle-audit/v1").String(p.SessionID).String(i.RequestID).String(string(action)).String(it.ID).Hash()
	ev := domain.LifecycleEvent{ID: id, SessionID: p.SessionID, Seq: seq, TargetKind: domain.TargetItem, TargetID: it.ID,
		Action: string(action), From: from, To: to, Actor: p, GrantID: auth.GrantIDs[target.AuthorizationKey]}
	after, err := tx.UpdateItem(it.ID, i.ExpectedVersion, change, ev)
	if err != nil {
		return itemEffect{}, err
	}
	return itemEffect{before: it, after: after, audit: ev, current: domain.ItemCurrent}, nil
}
