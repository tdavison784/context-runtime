package lifecycle

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
)

// gcCache memoizes the per-collection task and owner reads that many
// candidates share; every value comes from the collecting transaction.
type gcCache struct {
	tasks  map[string]*domain.TaskState
	owners map[domain.OwnerKind]map[string]*domain.OwnerRegistration
}

func newGCCache() *gcCache {
	return &gcCache{tasks: map[string]*domain.TaskState{}, owners: map[domain.OwnerKind]map[string]*domain.OwnerRegistration{}}
}

// gcBase reads the cheap facts every candidate needs: currentness, the
// originating task and the declared broad-scope owner. Protection facts are
// left absent; policy.MayArchive decides whether they must be read.
func (s *Service) gcBase(tx store.Tx, sem store.SemanticReader, it domain.ContextItem, snapSeq uint64, c *gcCache, b *workBudget) (policy.GCSnapshot, error) {
	out := policy.GCSnapshot{OwnerSnapshot: policy.OwnerSnapshot{Seq: snapSeq}, Item: domain.ItemRevisionRef{ItemID: it.ID, Version: it.Version}}
	if err := b.spend(1); err != nil {
		return out, err
	}
	var err error
	if out.Currentness, err = currentness(tx, it); err != nil {
		return out, err
	}
	if it.TaskID != "" {
		t, seen := c.tasks[it.TaskID]
		if !seen {
			if err := b.spend(1); err != nil {
				return out, err
			}
			v, err := tx.Task(it.TaskID)
			switch {
			case err == nil:
				t = &v
			case !errors.Is(err, domain.ErrNotFound):
				return out, err
			}
			c.tasks[it.TaskID] = t
		}
		out.Task = t
	}
	if it.Scope == domain.ScopeWorkflow || it.Scope == domain.ScopeAgent {
		kind, id := domain.OwnerWorkflow, it.Access.WorkflowID
		if it.Scope == domain.ScopeAgent {
			kind, id = domain.OwnerAgent, it.Access.AgentID
		}
		if c.owners[kind] == nil {
			c.owners[kind] = map[string]*domain.OwnerRegistration{}
		}
		o, seen := c.owners[kind][id]
		if !seen {
			if err := b.spend(1); err != nil {
				return out, err
			}
			v, err := sem.OwnerRegistration(kind, id)
			switch {
			case err == nil:
				o = &v
			case !errors.Is(err, domain.ErrNotFound):
				return out, err
			}
			c.owners[kind][id] = o
		}
		out.Owner = o
	}
	return out, nil
}

// gcProtection completes a possibly-archivable candidate's snapshot from
// exhausted indexed reads: the complete source-obligation set, newest
// checkpoint, open/unacknowledged exchange membership and every holder's
// lease via policy.LeaseLive, all at the current sequence. Overflow aborts;
// unknown holder state protects.
func (s *Service) gcProtection(tx store.Tx, sem store.SemanticReader, it domain.ContextItem, out *policy.GCSnapshot, b *workBudget) error {
	if err := b.spend(3); err != nil {
		return err
	}
	obs, err := tx.ObligationsBySource(it.ID, s.policy.MaxTargets)
	if errors.Is(err, store.ErrLimitExceeded) {
		return domain.ErrResourceLimit
	}
	if err != nil {
		return err
	}
	if err := b.spend(len(obs)); err != nil {
		return err
	}
	out.ObligationsKnown = true
	for _, o := range obs {
		out.OpenObligationSource = out.OpenObligationSource || o.Current && (o.Status == domain.ObligationUnresolved || o.Status == domain.ObligationBlocked)
	}
	if it.Role == domain.RoleCheckpoint {
		if out.NewestCheckpoint, err = s.newestCheckpoint(tx, it, b); err != nil {
			return err
		}
	}
	if out.OpenExchange, err = s.inOpenExchange(sem, it.ID, b); err != nil {
		return err
	}
	// J2 freezes only the candidate set: protections read current state, so a
	// lease issued after the request's snapshot still protects (SEC-4.2).
	out.LiveLease, err = s.leasedContent(tx, sem, domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash}, tx.LastSeq(), b)
	return err
}

// checkpointOfItem is W5's bounded item→checkpoint lookup; tests replace it.
var checkpointOfItem = graph.CheckpointOfItem

// newestCheckpoint asks, as the conversation's own agent (a session-level or
// collector principal cannot see task/agent-scoped checkpoints), whether it is
// its conversation's newest checkpoint. An item that viewer cannot resolve
// counts as newest, so uncertainty protects; bounds and integrity abort.
func (s *Service) newestCheckpoint(tx store.ReadTx, it domain.ContextItem, b *workBudget) (bool, error) {
	if err := b.spend(1); err != nil {
		return false, err
	}
	agent := domain.Principal{SessionID: it.SessionID, WorkflowID: it.WorkflowID, TaskID: it.TaskID, AgentID: it.AgentID, Authority: domain.AuthorityAgent}
	_, newest, err := checkpointOfItem(tx, agent, it.ID, s.policy.MaxPageSize, b.remaining)
	if errors.Is(err, domain.ErrNotFound) {
		return true, nil
	}
	return newest, err
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
