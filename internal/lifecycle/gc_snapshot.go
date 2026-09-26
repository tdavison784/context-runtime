package lifecycle

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
)

// gcSnapshot assembles policy.GCSnapshot for one candidate from exact and
// indexed bounded reads in the collecting transaction. Every protection fact
// comes from an exhausted read; overflow aborts, and a lease whose holder
// state is unknown counts as possibly live.
func (s *Service) gcSnapshot(tx store.Tx, sem store.SemanticReader, it domain.ContextItem, snapSeq uint64, b *workBudget) (policy.GCSnapshot, error) {
	out := policy.GCSnapshot{OwnerSnapshot: policy.OwnerSnapshot{Seq: snapSeq}, Item: domain.ItemRevisionRef{ItemID: it.ID, Version: it.Version}}
	if err := b.spend(4); err != nil {
		return out, err
	}
	var err error
	if out.Currentness, err = currentness(tx, it); err != nil {
		return out, err
	}
	if it.TaskID != "" {
		t, err := tx.Task(it.TaskID)
		switch {
		case err == nil:
			out.Task = &t
		case !errors.Is(err, domain.ErrNotFound):
			return out, err
		}
	}
	if it.Scope == domain.ScopeWorkflow || it.Scope == domain.ScopeAgent {
		kind, id := domain.OwnerWorkflow, it.Access.WorkflowID
		if it.Scope == domain.ScopeAgent {
			kind, id = domain.OwnerAgent, it.Access.AgentID
		}
		o, err := sem.OwnerRegistration(kind, id)
		switch {
		case err == nil:
			out.Owner = &o
		case !errors.Is(err, domain.ErrNotFound):
			return out, err
		}
	}
	obs, err := tx.ObligationsBySource(it.ID, s.policy.MaxTargets)
	if errors.Is(err, store.ErrLimitExceeded) {
		return out, domain.ErrResourceLimit
	}
	if err != nil {
		return out, err
	}
	if err := b.spend(len(obs)); err != nil {
		return out, err
	}
	out.ObligationsKnown = true
	for _, o := range obs {
		out.OpenObligationSource = out.OpenObligationSource || o.Current && (o.Status == domain.ObligationUnresolved || o.Status == domain.ObligationBlocked)
	}
	if out.OpenExchange, err = s.inOpenExchange(sem, it.ID, b); err != nil {
		return out, err
	}
	out.LiveLease, err = s.leasedContent(tx, sem, domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash}, snapSeq, b)
	return out, err
}

// inOpenExchange reports membership in an open, executing, or closed but
// unacknowledged exchange: pending input and results survive collection.
func (s *Service) inOpenExchange(sem store.SemanticReader, itemID string, b *workBudget) (bool, error) {
	members, err := drain(b, b.remaining, func(p store.Page) (store.ResultPage[domain.ExchangeMember], error) {
		return sem.MembershipsByItem(itemID, p)
	})
	if err != nil {
		return false, err
	}
	for _, m := range members {
		if err := b.spend(1); err != nil {
			return false, err
		}
		x, err := sem.LogicalExchange(m.ExchangeID)
		if err != nil {
			return false, err
		}
		if x.State == domain.ExchangeOpen || x.State == domain.ExchangeExecuting || x.State == domain.ExchangeClosed && x.AcknowledgmentID == "" {
			return true, nil
		}
	}
	return false, nil
}

// leasedContent checks every holder's lease on the exact content with the
// shared policy.LeaseLive predicate: the collector's own view is irrelevant.
func (s *Service) leasedContent(tx store.Tx, sem store.SemanticReader, src domain.ItemContentRef, snapSeq uint64, b *workBudget) (bool, error) {
	leases, err := drain(b, b.remaining, func(p store.Page) (store.ResultPage[domain.RetrievalLease], error) {
		return sem.LeasesBySource(src, p)
	})
	if err != nil {
		return false, err
	}
	for _, l := range leases {
		if err := b.spend(2); err != nil {
			return false, err
		}
		if l.Holder.TaskID == "" {
			continue // LeaseLive requires the holder's active task
		}
		task, err := tx.Task(l.Holder.TaskID)
		if errors.Is(err, domain.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		conv, err := tx.Conversation(l.ConversationID)
		if errors.Is(err, domain.ErrNotFound) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if policy.LeaseLive(l, policy.LeaseSnapshot{Seq: snapSeq, Source: src, Task: task, Conversation: conv}, l.Holder, task.TurnID) {
			return true, nil
		}
	}
	return false, nil
}
