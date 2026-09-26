package tools

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

type stubIntent struct{ RequestID, Value string }

func testService(t *testing.T) *Service {
	t.Helper()
	s, err := NewService(testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func runStub(s *Service, tx store.Tx, i domain.ToolInvocation, intent stubIntent, calls *int) (domain.ToolResult, error) {
	return execute(s, tx, i, "stub", intent.RequestID, intent, func(tx store.Tx, _ store.SemanticTx, state invocationState) (domain.ToolResult, error) {
		*calls++
		if intent.Value == "fail" {
			return domain.ToolResult{}, errors.New("private store detail")
		}
		return domain.ToolResult{CheckpointID: "effect-" + state.exchange.ID[:8]}, nil
	})
}

func TestExecuteCommitsResultMembershipAndReceiptsOnce(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	intent := stubIntent{RequestID: "request", Value: "v"}
	var calls int
	var first domain.ToolResult
	update(t, st, func(tx store.Tx) error {
		var err error
		first, err = runStub(s, tx, i, intent, &calls)
		return err
	})
	update(t, st, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		id, _ := i.ID()
		receipt, err := sem.ToolExecutionReceipt(id)
		if err != nil || receipt.Result != first || receipt.Method != "stub" {
			t.Fatalf("receipt: %+v, %v", receipt, err)
		}
		members, err := sem.ExchangeMembers(i.ExchangeID, store.Page{Limit: 8})
		if err != nil || len(members.Records) != 3 {
			t.Fatalf("members: %+v, %v", members, err)
		}
		m := members.Records[2]
		result, err := tx.Item(m.Source.ItemID)
		if err != nil || m.Role != domain.MemberToolResult || m.CallID != i.CallID || m.ToolCallID != i.ToolCallID || m.Position != 3 {
			t.Fatalf("result member: %+v, %v", m, err)
		}
		if result.Authority != domain.AuthorityTool || result.Role != domain.RoleTranscript || result.Parts[0].Text != ResultText(first) || !result.Access.Permits(i.Principal) {
			t.Fatalf("result item: %+v", result)
		}
		// Replay precedes current state: a later turn and completed task do not matter.
		task, _ := tx.Task(i.Principal.TaskID)
		seq := tx.NextSeq()
		task.Turn, task.TurnID, task.Status, task.CompletedSeq = 2, "turn-2", domain.TaskCompleted, seq
		_, err = tx.PutTask(task, task.Version, storetest.NewLifecycleEvent("s", "done", seq, domain.TargetTask, task.TaskID))
		return err
	})
	update(t, st, func(tx store.Tx) error {
		before := tx.LastSeq()
		again, err := runStub(s, tx, i, intent, &calls)
		if err != nil || again != first || tx.LastSeq() != before || calls != 1 {
			t.Fatalf("replay: %+v, %v, calls %d", again, err, calls)
		}
		return nil
	})
	for name, change := range map[string]func(*domain.ToolInvocation, *stubIntent){
		"arguments": func(_ *domain.ToolInvocation, in *stubIntent) { in.Value = "other" },
		"request":   func(_ *domain.ToolInvocation, in *stubIntent) { in.RequestID = "other" },
		"reused":    func(i *domain.ToolInvocation, _ *stubIntent) { i.ToolCallID = "tool-2" },
	} {
		t.Run(name, func(t *testing.T) {
			ii, in := i, intent
			change(&ii, &in)
			err := st.Update(testContext, "s", func(tx store.Tx) error { _, err := runStub(s, tx, ii, in, &calls); return err })
			if !errors.Is(err, domain.ErrEventIDConflict) || FixedError(err).Error() != domain.ToolErrorConflict.Message() {
				t.Fatalf("conflict: %v", err)
			}
		})
	}
}

func TestExecuteFailureLeavesNoEffectEvenWhenIgnored(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	var calls int
	var before uint64
	err := st.Update(testContext, "s", func(tx store.Tx) error {
		before = tx.LastSeq()
		_, _ = runStub(s, tx, i, stubIntent{RequestID: "request", Value: "fail"}, &calls)
		return nil // the application ignores the failure
	})
	if err == nil {
		t.Fatal("ignored failure committed")
	}
	update(t, st, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		id, _ := i.ID()
		if _, err := sem.ToolExecutionReceipt(id); !errors.Is(err, domain.ErrNotFound) || tx.LastSeq() != before {
			t.Fatal("partial execution committed", err)
		}
		_, err := runStub(s, tx, i, stubIntent{RequestID: "request", Value: "ok"}, &calls)
		return err
	})
}

func TestExecuteDistinguishesToolCallsOfOneOutput(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	second := i
	second.ToolCallID = "tool-2"
	update(t, st, func(tx store.Tx) error {
		membership, _ := graph.NewMembershipService(testPolicy())
		sem, _ := store.Semantic(tx)
		x, _ := sem.LogicalExchange(i.ExchangeID)
		output, _ := tx.Item("output")
		_, err := membership.RegisterExchangeMember(tx, storetest.NewPrincipal("s", domain.AuthorityHarness), domain.RegisterExchangeMemberIntent{RequestID: "tool-2", ExchangeID: x.ID, ExpectedRevision: x.Revision, Position: 3, Role: domain.MemberToolCall, Source: storetest.ContentRef(output), CallID: i.CallID, ToolCallID: second.ToolCallID})
		return err
	})
	var calls int
	update(t, st, func(tx store.Tx) error {
		a, err := runStub(s, tx, i, stubIntent{RequestID: "a", Value: "v"}, &calls)
		if err != nil {
			return err
		}
		b, err := runStub(s, tx, second, stubIntent{RequestID: "b", Value: "v"}, &calls)
		if err != nil || calls != 2 || a != b {
			t.Fatalf("second call: %+v %+v, %v", a, b, err)
		}
		return nil
	})
	err := st.Update(testContext, "s", func(tx store.Tx) error {
		_, err := runStub(s, tx, second, stubIntent{RequestID: "c", Value: "v"}, &calls)
		return err
	})
	if !errors.Is(err, domain.ErrEventIDConflict) {
		t.Fatalf("second execution of one tool call: %v", err)
	}
}
