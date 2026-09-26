package ingest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Tests for ADR 19 clauses SPEC-1.10 found unlocked. Each asserts the
// behavior on both stores and that a rejection writes nothing.

// TestCompletedTaskNeverReactivated (ADR 19 section 14, D18): once a task is
// COMPLETED, no ingestion (turn-opening or not) reactivates it or opens a
// turn; the event is rejected with nothing written.
func TestCompletedTaskNeverReactivated(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("u1", "hello", false))
		if err := f.s.Update(ctx, sess, func(tx store.Tx) error {
			task, err := tx.Task("T")
			if err != nil {
				return err
			}
			seq := tx.NextSeq()
			done := task
			done.Status, done.CompletedSeq, done.Version = domain.TaskCompleted, seq, task.Version+1
			_, err = tx.PutTask(done, task.Version, domain.LifecycleEvent{ID: "complete-T", SessionID: sess, Seq: seq,
				TargetKind: domain.TargetTask, TargetID: "T", Action: "completed", Actor: principal(domain.AuthoritySystem)})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		before := f.snapshot()
		var task domain.TaskState
		f.view(func(tx store.ReadTx) error { var err error; task, err = tx.Task("T"); return err })
		for _, e := range []domain.Event{userEvent("u2", "## Goal\nReopen me.\n", true), sysEvent("s1", "## Pinned\n- x\n")} {
			p := user
			if e.Kind == domain.EventSystem {
				p = principal(domain.AuthoritySystem)
			}
			if _, err := f.ingest(p, e); !errors.Is(err, domain.ErrInvalidTransition) {
				t.Errorf("%s on a completed task: err = %v, want ErrInvalidTransition", e.EventID, err)
			}
		}
		var after domain.TaskState
		f.view(func(tx store.ReadTx) error { var err error; after, err = tx.Task("T"); return err })
		if !reflect.DeepEqual(after, task) || !reflect.DeepEqual(f.snapshot(), before) {
			t.Fatalf("completed task changed: %+v -> %+v", task, after)
		}
	})
}

// TestRetrievedBeforeFirstTurn (R19): like TOOL output, RETRIEVED_CONTENT
// is TURN-scoped evidence and needs an owning turn, so before any turn it is
// rejected; after a turn opens it is accepted.
func TestRetrievedBeforeFirstTurn(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		e := domain.Event{EventID: "r0", Kind: domain.EventRetrievedContent, Spans: []domain.Span{textSpan(domain.AuthorityRetrievedContent, false, "web page")}}
		before := f.snapshot()
		if _, err := f.ingest(sys, e); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Fatalf("retrieved content before any turn: err = %v, want ErrInvalidRecord", err)
		}
		if !reflect.DeepEqual(f.snapshot(), before) {
			t.Fatal("rejected event wrote state")
		}
		f.mustIngest(sys, userEvent("u1", "hi", false))
		f.mustIngest(sys, e)
	})
}

// TestToolCallIDCreatesNoEdge (ADR 19 section 16, R2): a TOOL span's
// ToolCallID is recorded in SourceRef only; in Phase 2 it creates no
// DEPENDS_ON or any other edge (tool-call items belong to the call ledger).
func TestToolCallIDCreatesNoEdge(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, userEvent("u1", "run the tests", false))
		span := textSpan(domain.AuthorityTool, false, "PASS")
		span.Source = &domain.SourceRef{Kind: domain.SourceTool, Locator: "go test", ToolCallID: "call-1"}
		base := f.snapshot()
		r := f.mustIngest(sys, domain.Event{EventID: "t1", Kind: domain.EventTool, Spans: []domain.Span{span}})
		if len(r.Items) != 1 || r.Items[0].Source == nil || r.Items[0].Source.ToolCallID != "call-1" {
			t.Fatalf("tool transcript: %+v", r.Items)
		}
		if got := f.snapshot(); got.rels != base.rels {
			t.Fatalf("ToolCallID created %d relationships", got.rels-base.rels)
		}
	})
}
