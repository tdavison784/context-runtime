package lifecycle

import (
	"context"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// itemOp is one typed single-item lifecycle request. plan validates the
// target, authorizes action at the allocated seq and applies the CAS update;
// it runs only when no receipt exists for requestID.
type itemOp struct {
	action    domain.Action
	requestID string
	intent    any // canonical request arguments
	plan      func(tx store.Tx, p domain.Principal, seq uint64) (itemEffect, error)
}

func (s *Service) directiveOp(i domain.ItemMutationIntent, action domain.Action) itemOp {
	return itemOp{action: action, requestID: i.RequestID, intent: i, plan: func(tx store.Tx, p domain.Principal, seq uint64) (itemEffect, error) {
		return s.executeDirective(tx, p, i, action, seq)
	}}
}

func (s *Service) ResolveStandalone(ctx context.Context, p domain.Principal, i domain.ResolveIntent) (domain.ItemMutationResult, error) {
	return s.standaloneItem(ctx, p, s.directiveOp(i, domain.ActionResolve))
}

func (s *Service) UnpinStandalone(ctx context.Context, p domain.Principal, i domain.UnpinIntent) (domain.ItemMutationResult, error) {
	return s.standaloneItem(ctx, p, s.directiveOp(i, domain.ActionUnpin))
}

// Resolve and Unpin implement ingestion's transaction executor. The caller
// supplies the authenticated source actor and the actual allocated effect seq.
func (s *Service) Resolve(tx store.Tx, p domain.Principal, i domain.ItemMutationIntent, seq uint64) (LifecycleOutcome, error) {
	return s.executeItem(tx, p, s.directiveOp(i, domain.ActionResolve), seq)
}

func (s *Service) Unpin(tx store.Tx, p domain.Principal, i domain.ItemMutationIntent, seq uint64) (LifecycleOutcome, error) {
	return s.executeItem(tx, p, s.directiveOp(i, domain.ActionUnpin), seq)
}

func (s *Service) executeItem(tx store.Tx, p domain.Principal, op itemOp, seq uint64) (out LifecycleOutcome, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = LifecycleOutcome{}
		}
	}()
	sem, _, prior, err := s.begin(tx, p, domain.MutationLifecycle, string(op.action), op.requestID, op.intent)
	if err != nil {
		return out, err
	}
	if prior != nil {
		return replayCommand(sem, p, *prior)
	}
	if _, err = s.applyItem(tx, p, op, seq); err != nil {
		return out, err
	}
	// Return the same immutable receipt and attribution for first execution and
	// retry. No current-item/grant lookup follows the committed-result lookup.
	receipt, err := sem.MutationReceipt(domain.MutationLifecycle, op.requestID)
	if err != nil {
		return out, err
	}
	return replayCommand(sem, p, receipt)
}

func (s *Service) standaloneItem(ctx context.Context, p domain.Principal, op itemOp) (domain.ItemMutationResult, error) {
	var out domain.ItemMutationResult
	err := s.store.Update(ctx, p.SessionID, func(tx store.Tx) error {
		// Look up replay before allocating even the caller-supplied effect seq.
		_, _, prior, err := s.begin(tx, p, domain.MutationLifecycle, string(op.action), op.requestID, op.intent)
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
		out, err = s.applyItem(tx, p, op, tx.NextSeq())
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
	return s.applyItem(tx, p, s.directiveOp(i, domain.ActionResolve), seq)
}

func (s *Service) ApplyUnpin(tx store.Tx, p domain.Principal, i domain.UnpinIntent, seq uint64) (domain.ItemMutationResult, error) {
	return s.applyItem(tx, p, s.directiveOp(i, domain.ActionUnpin), seq)
}

func (s *Service) applyItem(tx store.Tx, p domain.Principal, op itemOp, seq uint64) (out domain.ItemMutationResult, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = domain.ItemMutationResult{}
		}
	}()
	sem, args, prior, err := s.begin(tx, p, domain.MutationLifecycle, string(op.action), op.requestID, op.intent)
	if err != nil {
		return out, err
	}
	if prior != nil {
		if prior.Result.Item == nil {
			return out, domain.ErrIntegrity
		}
		return prior.Result.Item.Clone(), nil
	}
	effect, err := op.plan(tx, p, seq)
	if err != nil {
		return out, err
	}
	if err = recordEffect(tx, sem, effect); err != nil {
		return out, err
	}
	// lifecycle/v1 replay locates this immutable companion at receipt.Seq-1.
	// Do not allocate another sequence between recordEffect and finish.
	out = effect.result()
	err = s.finish(tx, sem, p, domain.MutationLifecycle, string(op.action), op.requestID, args, domain.MutationResult{Item: &out})
	return out, err
}
