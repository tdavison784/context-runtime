package graph

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestMembershipCancellationReplaysAndNeverBecomesCoverage(t *testing.T) {
	s, service, actor, registration := membershipTestStore(t)
	var intent domain.CancelExchangeIntent
	update(t, s, "s", func(tx store.Tx) error {
		result, err := service.RegisterExchange(tx, actor, registration)
		if err != nil {
			return err
		}
		intent = domain.CancelExchangeIntent{RequestID: "cancel", ExchangeID: result.IDs[0], ExpectedRevision: 1, Reason: domain.ExchangeAbandoned}
		// A late cancellation must still refer to the original turn.
		task, _ := tx.Task(actor.TaskID)
		task.Turn, task.TurnID = 2, "turn-2"
		_, err = tx.PutTask(task, task.Version, domain.LifecycleEvent{ID: "advance", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: task.TaskID, Action: "advance", Actor: actor})
		return err
	})
	var original domain.RecordResult
	update(t, s, "s", func(tx store.Tx) error {
		var err error
		original, err = service.CancelExchange(tx, actor, intent)
		return err
	})
	update(t, s, "s", func(tx store.Tx) error {
		before := tx.LastSeq()
		got, err := service.CancelExchange(tx, actor, intent)
		if err != nil || !reflect.DeepEqual(got, original) || tx.LastSeq() != before {
			t.Fatalf("replay: %+v, %v", got, err)
		}
		sem, _ := store.Semantic(tx)
		x, err := sem.LogicalExchange(intent.ExchangeID)
		if err != nil || x.State != domain.ExchangeCancelled || x.TurnID != "turn-1" || x.Revision != 2 {
			t.Fatalf("exchange: %+v, %v", x, err)
		}
		ack, err := sem.ExchangeAcknowledgment(x.AcknowledgmentID)
		if err != nil || !ack.Cancelled || ack.ID != original.IDs[0] || ack.ManifestID != "" || ack.ConsumingCallID != "" || ack.Actor != actor {
			t.Fatalf("cancellation: %+v, %v", ack, err)
		}
		state, err := sem.ConversationMembership(x.ConversationID)
		if err != nil || state.Revision != 2 || state.ClosedFrontier != 0 {
			t.Fatalf("frontier: %+v, %v", state, err)
		}
		return nil
	})
}
