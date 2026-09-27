package lifecycle

import (
	"context"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// MutationOutcome is the executor result for typed ingestion operations.
// CompleteTask has no single authorizing grant: every goal records its own.
type MutationOutcome struct {
	MutationReceiptID, GrantID string
	Result                     domain.MutationResult
}

func (s *Service) CompleteTask(tx store.Tx, p domain.Principal, i domain.CompleteTaskIntent, seq uint64) (out MutationOutcome, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = MutationOutcome{}
		}
	}()
	sem, args, prior, err := s.begin(tx, p, domain.MutationLifecycle, string(domain.ActionCompleteTask), i.RequestID, i)
	if err != nil {
		return out, err
	}
	if prior != nil {
		if prior.Result.Completion == nil {
			return out, domain.ErrIntegrity
		}
		return MutationOutcome{MutationReceiptID: prior.ID, Result: prior.Result.Clone()}, nil
	}
	if err = i.Validate(); err != nil {
		return out, err
	}
	if !tx.Allocated(seq) || seq == 0 {
		return out, domain.ErrInvalidRecord
	}
	if !p.Authority.CanHoldLifecycleAuthority() {
		return out, domain.ErrInvalidAuthorityPromotion
	}
	if p.TaskID != i.TaskID {
		return out, domain.ErrNotFound
	}
	task, err := tx.Task(i.TaskID)
	if err != nil {
		return out, err
	}
	if err = completionOwner(p, task); err != nil {
		return out, err
	}
	if task.Version == ^uint64(0) {
		return out, domain.ErrResourceLimit
	}
	b := workBudget{remaining: s.policy.MaxTransactionWork, pageSize: s.policy.MaxPageSize}
	goals, err := drain(&b, s.policy.MaxTargets, func(page store.Page) (store.ResultPage[domain.ContextItem], error) {
		return sem.OpenGoalsByTaskOwner(i.TaskID, page)
	})
	if err != nil {
		return out, err
	}
	plan, err := s.planCompletionGoals(tx, p, i.RequestID, goals, seq, &b)
	if err != nil {
		return out, err
	}
	if err = completionBlockers(sem, i.TaskID, &b); err != nil {
		return out, err
	}
	for _, n := range []int{len(plan), len(plan), 4} {
		if err = b.spend(n); err != nil {
			return out, err
		}
	}
	result, err := s.writeCompletion(tx, sem, p, i, task, plan, seq)
	if err != nil {
		return out, err
	}
	out.Result.Completion = &result
	if err = s.finish(tx, sem, p, domain.MutationLifecycle, string(domain.ActionCompleteTask), i.RequestID, args, out.Result); err != nil {
		return out, err
	}
	out.MutationReceiptID, err = domain.MutationReceiptID(tx, p, domain.MutationLifecycle, i.RequestID)
	return out, err
}

func (s *Service) CompleteTaskStandalone(ctx context.Context, p domain.Principal, i domain.CompleteTaskIntent) (domain.CompletionReceipt, error) {
	var out MutationOutcome
	if err := domain.ValidateCallerRequestID(i.RequestID); err != nil {
		return domain.CompletionReceipt{}, err // callers never name runtime namespaces (H5, SEC-2.2)
	}
	err := s.store.Update(ctx, p.SessionID, func(tx store.Tx) error {
		_, _, prior, err := s.begin(tx, p, domain.MutationLifecycle, string(domain.ActionCompleteTask), i.RequestID, i)
		if err != nil {
			return err
		}
		var seq uint64
		if prior == nil {
			seq = tx.NextSeq()
		}
		out, err = s.CompleteTask(tx, p, i, seq)
		return err
	})
	if err != nil {
		return domain.CompletionReceipt{}, err
	}
	return out.Result.Completion.Clone(), nil
}
