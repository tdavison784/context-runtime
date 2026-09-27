package lifecycle

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/gcqueue"
	"github.com/tdavison784/context-runtime/internal/store"
)

func (s *Service) writeCompletion(tx store.Tx, sem store.SemanticTx, p domain.Principal, i domain.CompleteTaskIntent, task domain.TaskState, plan []itemEffect, seq uint64) (out domain.CompletionReceipt, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = domain.CompletionReceipt{}
		}
	}()
	status, retention := domain.GoalResolved, domain.RetentionHigh
	for _, effect := range plan {
		effect.after, err = tx.UpdateItem(effect.before.ID, effect.before.Version, domain.ItemChange{GoalStatus: &status, Retention: &retention}, effect.audit)
		if err != nil {
			return out, err
		}
		effect.current = domain.ItemCurrent
		if effect.before.DirectiveID == "" {
			effect.current = domain.ItemUnkeyed
		}
		if err = recordEffect(tx, sem, effect); err != nil {
			return out, err
		}
		out.ResolvedGoals = append(out.ResolvedGoals, domain.ItemRevisionRef{ItemID: effect.after.ID, Version: effect.after.Version})
	}
	if len(plan) != 0 {
		seq = tx.NextSeq()
	}
	id := "life_" + domain.NewCanonicalEncoder("context-runtime/task-completion-audit/v1").String(p.SessionID).String(i.RequestID).String(i.TaskID).Hash()
	event := domain.LifecycleEvent{ID: id, SessionID: p.SessionID, Seq: seq, TargetKind: domain.TargetTask, TargetID: task.TaskID, Action: string(domain.ActionCompleteTask), From: string(domain.TaskActive), To: string(domain.TaskCompleted), Actor: p}
	out.TaskID, out.BeforeVersion, out.AuditID = task.TaskID, task.Version, id
	task.Status, task.CompletedSeq = domain.TaskCompleted, seq
	after, err := tx.PutTask(task, task.Version, event)
	if err != nil {
		return out, err
	}
	out.AfterVersion = after.Version
	// A task completes once, so its ID is the trigger identity.
	out.GCRequestID, err = gcqueue.Enqueue(tx, s.policy, p, domain.GCTaskCompletion, task.TaskID, task.TaskID)
	return out, err
}

func gcRequestID(session, request string) string { return gcqueue.RequestID(session, request) }
