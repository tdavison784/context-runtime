package lifecycle

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

func (s *Service) planCompletionGoals(tx store.Tx, p domain.Principal, requestID string, goals []domain.ContextItem, seq uint64, b *workBudget) ([]itemEffect, error) {
	if !tx.Allocated(seq) || seq == 0 {
		return nil, domain.ErrInvalidRecord
	}
	if len(goals) > s.policy.MaxTargets {
		return nil, domain.ErrResourceLimit
	}
	var plan []itemEffect
	seen := map[string]bool{}
	for n, it := range goals {
		if !it.Access.Permits(p) {
			return nil, domain.ErrInvalidAuthorityPromotion
		}
		if seen[it.ID] || it.SessionID != p.SessionID || it.TaskID != p.TaskID || it.Scope != domain.ScopeTurn && it.Scope != domain.ScopeTask || it.Kind != domain.KindGoal || it.GoalStatus == nil || *it.GoalStatus != domain.GoalOpen {
			return nil, domain.ErrIntegrity
		}
		seen[it.ID] = true
		if err := b.spend(4); err != nil {
			return nil, err
		}
		current, err := graph.IsCurrent(tx, it.ID)
		if err != nil {
			return nil, err
		}
		if !current {
			return nil, domain.ErrIntegrity
		}
		if n != 0 {
			seq = tx.NextSeq()
		}
		if !p.Authority.AtLeast(it.Authority) {
			if err := b.spend(s.policy.MaxTargets); err != nil {
				return nil, err
			}
		}
		target := domain.ItemGrantTarget(p.SessionID, it.ID)
		auth, err := graph.AuthorizeAtSequence(tx, p, domain.ActionResolve, []domain.GrantTarget{target}, nil, seq, s.policy.MaxTargets)
		if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			return nil, domain.ErrInvalidAuthorityPromotion
		}
		if err != nil {
			return nil, err
		}
		id := "life_" + domain.NewCanonicalEncoder("context-runtime/lifecycle-audit/v1").String(p.SessionID).String(requestID).String(string(domain.ActionResolve)).String(it.ID).Hash()
		plan = append(plan, itemEffect{before: it, audit: domain.LifecycleEvent{ID: id, SessionID: p.SessionID, Seq: seq, TargetKind: domain.TargetItem, TargetID: it.ID, Action: string(domain.ActionResolve), From: string(domain.GoalOpen), To: string(domain.GoalResolved), Actor: p, GrantID: auth.GrantIDs[target.AuthorizationKey]}})
	}
	return plan, nil
}
