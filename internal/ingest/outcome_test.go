package ingest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func agentPrincipal() domain.Principal { return principal(domain.AuthorityAgent) }

// bindingFor is the outcome binding of call callID issued in turn n.
func bindingFor(callID string, n uint64) domain.OutcomeBinding {
	p := agentPrincipal()
	return domain.OutcomeBinding{Principal: p, Turn: n, TurnID: domain.DerivedTurnID(sess, "T", n),
		ExchangeID: "exch_" + callID, ConversationID: domain.ConversationIDFor("T", "A"), CallID: callID}
}

func outcomeEvent(b domain.OutcomeBinding, text string) domain.Event {
	return domain.Event{EventID: OutcomeEventID(b), Kind: domain.EventAgent, Spans: []domain.Span{textSpan(domain.AuthorityAgent, false, text)}}
}

func (f *fixture) ingestOutcome(b domain.OutcomeBinding, e domain.Event) (domain.IngestReceipt, error) {
	return f.in.IngestOutcome(ctx, f.s, b, e)
}

func (f *fixture) task() domain.TaskState {
	f.t.Helper()
	var ts domain.TaskState
	f.view(func(tx store.ReadTx) error {
		var err error
		ts, err = tx.Task("T")
		return err
	})
	return ts
}

// TestOutcome_KeepsOriginatingTurn (P3-34): a provider outcome that
// arrives after its task opened a newer turn is stamped with the turn it
// was issued in, never the newest; it opens no turn, and its retry replays.
func TestOutcome_KeepsOriginatingTurn(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u1", "first question", false))
		b := bindingFor("call_x1", 1)
		f.mustIngest(user, userEvent("u2", "second question", false))
		r, err := f.ingestOutcome(b, outcomeEvent(b, "late answer to the first question"))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Items) != 1 || r.Items[0].CreatedTurn != 1 || r.Items[0].TurnID != b.TurnID || r.OpenedTurn != 0 {
			t.Fatalf("outcome items %+v, opened %d", r.Items, r.OpenedTurn)
		}
		if ts := f.task(); ts.Turn != 2 {
			t.Fatalf("task turn = %d", ts.Turn)
		}
		seq := f.lastSeq()
		again, err := f.ingestOutcome(b, outcomeEvent(b, "late answer to the first question"))
		if err != nil || !reflect.DeepEqual(normReceipt(again), normReceipt(r)) || f.lastSeq() != seq {
			t.Fatalf("retry: %v", err)
		}
	})
}

// TestOutcome_CompletedTaskAuditOnly (P3-34): a late outcome for a
// completed task is recorded as audit history at its originating turn and
// never reactivates the task.
func TestOutcome_CompletedTaskAuditOnly(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u1", "question", false))
		if err := f.s.Update(ctx, sess, func(tx store.Tx) error {
			ts, err := tx.Task("T")
			if err != nil {
				return err
			}
			done := ts
			done.Status, done.Version = domain.TaskCompleted, ts.Version+1
			seq := tx.NextSeq()
			done.CompletedSeq = seq
			_, err = tx.PutTask(done, ts.Version, domain.LifecycleEvent{ID: "complete", SessionID: sess, Seq: seq, TargetKind: domain.TargetTask, TargetID: "T", Action: "completed", Actor: principal(domain.AuthoritySystem)})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		b := bindingFor("call_x1", 1)
		r, err := f.ingestOutcome(b, outcomeEvent(b, "answer after completion"))
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Items) != 1 || r.Items[0].CreatedTurn != 1 {
			t.Fatalf("outcome items %+v", r.Items)
		}
		if ts := f.task(); ts.Status != domain.TaskCompleted || ts.Turn != 1 {
			t.Fatalf("task reactivated or advanced: %+v", ts)
		}
	})
}

// TestOutcome_BindingIsIdentity (P3-34, fail closed): an outcome's EventID
// is derived from its binding, so the same outcome cannot be replayed
// under another call or turn; bindings naming a turn the task never
// opened, an unknown task, a turn-opening event, or typed operations are
// rejected with nothing written.
func TestOutcome_BindingIsIdentity(t *testing.T) {
	phase3Stores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		b := bindingFor("call_x1", 1)
		f.requireAtomic(domain.ErrNotFound, func() error { _, err := f.ingestOutcome(b, outcomeEvent(b, "no task yet")); return err })
		f.mustIngest(user, userEvent("u1", "question", false))

		wrongID := outcomeEvent(b, "answer")
		wrongID.EventID = "chosen-by-caller"
		future := bindingFor("call_x2", 2)
		badTurnID := bindingFor("call_x3", 1)
		badTurnID.TurnID = domain.DerivedTurnID(sess, "T", 7)
		userKind := outcomeEvent(b, "answer")
		userKind.Kind, userKind.Spans[0].Authority = domain.EventUser, domain.AuthorityUser
		withOps := outcomeEvent(b, "answer")
		withOps.Operations = []domain.SemanticOperation{spanOp(0)}
		for name, tc := range map[string]struct {
			b domain.OutcomeBinding
			e domain.Event
		}{
			"caller EventID":  {b, wrongID},
			"unopened turn":   {future, outcomeEvent(future, "answer")},
			"turn ID":         {badTurnID, outcomeEvent(badTurnID, "answer")},
			"turn-opening":    {b, userKind},
			"typed operation": {b, withOps},
		} {
			before := f.lastSeq()
			if _, err := f.ingestOutcome(tc.b, tc.e); !errors.Is(err, domain.ErrInvalidRecord) || f.lastSeq() != before {
				t.Errorf("%s: err = %v", name, err)
			}
		}
	})
}
