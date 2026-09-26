package lifecycle

import (
	"context"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func (s *Service) ResolveStandalone(ctx context.Context, p domain.Principal, i domain.ResolveIntent) (domain.ItemMutationResult, error) {
	return s.runDirective(ctx, p, i, domain.ActionResolve)
}

func (s *Service) UnpinStandalone(ctx context.Context, p domain.Principal, i domain.UnpinIntent) (domain.ItemMutationResult, error) {
	return s.runDirective(ctx, p, i, domain.ActionUnpin)
}

// Resolve and Unpin implement ingestion's transaction executor. The caller
// supplies the authenticated source actor and the actual allocated effect seq.
func (s *Service) Resolve(tx store.Tx, p domain.Principal, i domain.ItemMutationIntent, seq uint64) (LifecycleOutcome, error) {
	return s.executeCommand(tx, p, i, seq, domain.ActionResolve)
}

func (s *Service) Unpin(tx store.Tx, p domain.Principal, i domain.ItemMutationIntent, seq uint64) (LifecycleOutcome, error) {
	return s.executeCommand(tx, p, i, seq, domain.ActionUnpin)
}

func (s *Service) executeCommand(tx store.Tx, p domain.Principal, i domain.ItemMutationIntent, seq uint64, action domain.Action) (out LifecycleOutcome, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = LifecycleOutcome{}
		}
	}()
	sem, _, prior, err := s.begin(tx, p, domain.MutationLifecycle, string(action), i.RequestID, i)
	if err != nil {
		return out, err
	}
	if prior != nil {
		return replayCommand(sem, p, *prior)
	}
	if _, err = s.applyDirective(tx, p, i, seq, action); err != nil {
		return out, err
	}
	// Return the same immutable receipt and attribution for first execution and
	// retry. No current-item/grant lookup follows the committed-result lookup.
	receipt, err := sem.MutationReceipt(domain.MutationLifecycle, i.RequestID)
	if err != nil {
		return out, err
	}
	return replayCommand(sem, p, receipt)
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
	// lifecycle/v1 replay locates this immutable companion at receipt.Seq-1.
	// Do not allocate another sequence between recordEffect and finish.
	out = effect.result(domain.ItemCurrent)
	err = s.finish(tx, sem, p, domain.MutationLifecycle, string(action), i.RequestID, args, domain.MutationResult{Item: &out})
	return out, err
}
