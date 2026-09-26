package lifecycle

import (
	"context"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func (s *Service) Resolve(ctx context.Context, p domain.Principal, i domain.ResolveIntent) (domain.ItemMutationResult, error) {
	return s.runDirective(ctx, p, i, domain.ActionResolve)
}

func (s *Service) Unpin(ctx context.Context, p domain.Principal, i domain.UnpinIntent) (domain.ItemMutationResult, error) {
	return s.runDirective(ctx, p, i, domain.ActionUnpin)
}

func (s *Service) runDirective(ctx context.Context, p domain.Principal, i domain.ItemMutationIntent, action domain.Action) (domain.ItemMutationResult, error) {
	var out domain.ItemMutationResult
	err := s.store.Update(ctx, p.SessionID, func(tx store.Tx) error {
		// Look up replay before allocating even the caller-supplied effect seq.
		_, _, prior, err := s.begin(tx, p, domain.MutationLifecycle, string(action), i.RequestID, i)
		if err != nil {
			return err
		}
		if prior != nil {
			if prior.Result.Item == nil {
				return domain.ErrIntegrity
			}
			out = prior.Result.Item.Clone()
			return nil
		}
		out, err = s.applyDirective(tx, p, i, tx.NextSeq(), action)
		return err
	})
	if err != nil {
		return domain.ItemMutationResult{}, err
	}
	return out, nil
}

// ApplyResolve/ApplyUnpin execute exact typed intents, not textual lookup.
// Ingest must resolve NOT_FOUND/AMBIGUOUS/MISMATCH diagnostics before calling
// these methods; an executable intent's failure poisons its enclosing event.
// Callers allocate seq in this transaction and never reuse TargetCall sequences.
func (s *Service) ApplyResolve(tx store.Tx, p domain.Principal, i domain.ResolveIntent, seq uint64) (domain.ItemMutationResult, error) {
	return s.applyDirective(tx, p, i, seq, domain.ActionResolve)
}

func (s *Service) ApplyUnpin(tx store.Tx, p domain.Principal, i domain.UnpinIntent, seq uint64) (domain.ItemMutationResult, error) {
	return s.applyDirective(tx, p, i, seq, domain.ActionUnpin)
}

func (s *Service) applyDirective(tx store.Tx, p domain.Principal, i domain.ItemMutationIntent, seq uint64, action domain.Action) (out domain.ItemMutationResult, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = domain.ItemMutationResult{}
		}
	}()
	sem, args, prior, err := s.begin(tx, p, domain.MutationLifecycle, string(action), i.RequestID, i)
	if err != nil {
		return out, err
	}
	if prior != nil {
		if prior.Result.Item == nil {
			return out, domain.ErrIntegrity
		}
		return prior.Result.Item.Clone(), nil
	}
	effect, err := s.executeDirective(tx, p, i, action, seq)
	if err != nil {
		return out, err
	}
	if err = recordEffect(tx, sem, effect, domain.ItemCurrent); err != nil {
		return out, err
	}
	out = effect.result(domain.ItemCurrent)
	err = s.finish(tx, sem, p, domain.MutationLifecycle, string(action), i.RequestID, args, domain.MutationResult{Item: &out})
	return out, err
}
