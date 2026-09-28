package lifecycle

import (
	"context"
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Promote and Demote apply the closed generation/v1 table (P3-10/X5).
// PINNED→DURABLE is exclusively Unpin; no raw ItemChange is exposed.
func (s *Service) Promote(tx store.Tx, p domain.Principal, i domain.PromoteIntent, seq uint64) (LifecycleOutcome, error) {
	return s.executeItem(tx, p, s.generationOp(i, domain.ActionPromote), seq)
}

func (s *Service) Demote(tx store.Tx, p domain.Principal, i domain.DemoteIntent, seq uint64) (LifecycleOutcome, error) {
	return s.executeItem(tx, p, s.generationOp(i, domain.ActionDemote), seq)
}

func (s *Service) PromoteStandalone(ctx context.Context, p domain.Principal, i domain.PromoteIntent) (domain.ItemMutationResult, error) {
	return s.standaloneItem(ctx, p, s.generationOp(i, domain.ActionPromote))
}

func (s *Service) DemoteStandalone(ctx context.Context, p domain.Principal, i domain.DemoteIntent) (domain.ItemMutationResult, error) {
	return s.standaloneItem(ctx, p, s.generationOp(i, domain.ActionDemote))
}

func (s *Service) generationOp(i domain.PromoteIntent, action domain.Action) itemOp {
	return itemOp{action: action, requestID: i.RequestID, intent: i, plan: func(tx store.Tx, p domain.Principal, seq uint64) (itemEffect, error) {
		return s.executeGeneration(tx, p, i, action, seq)
	}}
}

func (s *Service) executeGeneration(tx store.Tx, p domain.Principal, i domain.PromoteIntent, action domain.Action, seq uint64) (itemEffect, error) {
	if err := i.Validate(); err != nil {
		return itemEffect{}, err
	}
	it, err := accessibleTarget(tx, p, i.ItemMutationIntent, seq)
	if err != nil {
		return itemEffect{}, err
	}
	current, err := currentness(tx, it)
	if err != nil {
		return itemEffect{}, err
	}
	// The complete source-obligation set, whatever its status or
	// materialization, decides exclusion; overflow never guesses "none".
	obs, err := tx.ObligationsBySource(it.ID, s.policy.MaxTargets)
	if errors.Is(err, store.ErrLimitExceeded) {
		return itemEffect{}, domain.ErrResourceLimit
	}
	if err != nil {
		return itemEffect{}, err
	}
	snap := policy.GenerationSnapshot{Item: domain.ItemRevisionRef{ItemID: it.ID, Version: it.Version}, Currentness: current, ObligationsKnown: true}
	for _, o := range obs {
		snap.CurrentObligationSource = snap.CurrentObligationSource || o.Current
	}
	change, err := policy.GenerationChange(it, snap, action, i.Generation)
	if err != nil {
		return itemEffect{}, err
	}
	target := domain.ItemGrantTarget(p.SessionID, it.ID)
	auth, err := graph.AuthorizeAtSequence(tx, p, action, []domain.GrantTarget{target}, nil, seq, s.policy.MaxTargets)
	if err != nil {
		return itemEffect{}, err
	}
	id := "life_" + domain.NewCanonicalEncoder("context-runtime/lifecycle-audit/v1").String(p.SessionID).String(i.RequestID).String(string(action)).String(it.ID).Hash()
	ev := domain.LifecycleEvent{ID: id, SessionID: p.SessionID, Seq: seq, TargetKind: domain.TargetItem, TargetID: it.ID,
		Action: string(action), From: string(it.Generation), To: string(i.Generation), Actor: p, GrantID: auth.GrantIDs[target.AuthorizationKey]}
	after, err := tx.UpdateItem(it.ID, i.ExpectedVersion, change, ev)
	if err != nil {
		return itemEffect{}, err
	}
	return itemEffect{before: it, after: after, audit: ev, current: current}, nil
}
