package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func invocationRecords() (domain.ToolInvocation, domain.LogicalExchange, domain.CallRecord, domain.TaskState) {
	x := storetest.NewExchange("s", "exchange", "task", "agent", 1, 1)
	x.State = domain.ExchangeExecuting
	c := storetest.NewCall("s", "producing", x.ConversationID, 2)
	c.Attempts = 1
	c = storetest.Finish(c, domain.CallCompleted, 3)
	i := domain.ToolInvocation{SessionID: "s", ConversationID: x.ConversationID, CallID: c.CallID, ToolCallID: "tool-1", ExchangeID: x.ID, TurnID: x.TurnID, Principal: x.Principal}
	return i, x, c, storetest.NewTask("s", "task")
}

func TestInvocationRequiresCompletedOutputAndCurrentExactOwner(t *testing.T) {
	i, x, c, task := invocationRecords()
	if err := checkInvocationRecords(i, x, c, task); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"partial", "failed", "compaction", "closed", "task", "turn", "workflow", "agent", "dispatcher"} {
		t.Run(change, func(t *testing.T) {
			x, c, task := x, c.Clone(), task
			switch change {
			case "partial":
				c.State = domain.CallSent
			case "failed":
				c = storetest.Finish(c, domain.CallFailed, c.FinishedSeq)
			case "compaction":
				c.Operation = domain.OperationCompaction
				c = storetest.Reseal(c)
			case "closed":
				x.State, x.AcknowledgmentID = domain.ExchangeClosed, "ack"
			case "task":
				task.Status = domain.TaskCompleted
			case "turn":
				task.Turn, task.TurnID = 2, "turn-2"
			case "workflow":
				task.WorkflowID = "other"
			case "agent":
				x.Principal.AgentID = "other"
			case "dispatcher":
				c.ServiceActor.AgentID = "other"
				c = storetest.Reseal(c)
			}
			if err := checkInvocationRecords(i, x, c, task); err == nil {
				t.Fatal("unauthenticated invocation accepted")
			}
		})
	}
}

// TestPartialAssistantOutputRefusesToolExecutionOnBothStores (P3-24, ADR 8
// :1355): the record-level test above proves the predicate in isolation; this
// is its store-level half on memory and SQLite. A tool request whose issuing
// inference is still in flight — the call sits at CallSent with no outcome —
// is refused before any work runs, leaves no state, and executes normally
// once the same conversation's completed output carries it.
func TestPartialAssistantOutputRefusesToolExecutionOnBothStores(t *testing.T) {
	p342bEachStore(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		// The control: a fully seeded invocation whose output completed.
		i := seedToolFixture(t, st)
		// A second exchange of the same agent whose producing call was sent
		// but never finished, exactly as a partially streamed assistant
		// output leaves it.
		var partial domain.ToolInvocation
		update(t, st, func(tx store.Tx) error {
			p := i.Principal
			actor := dispatcher(i)
			membership, err := graph.NewMembershipService(testPolicy())
			if err != nil {
				return err
			}
			task, err := tx.Task(p.TaskID)
			if err != nil {
				return err
			}
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			state, err := sem.ConversationMembership(i.ConversationID)
			if err != nil {
				return err
			}
			x, err := membership.RegisterExchange(tx, actor, domain.RegisterExchangeIntent{RequestID: "exchange-partial", Principal: p, TurnID: task.TurnID, Turn: task.Turn, ExpectedMembershipRevision: state.Revision}, tx.NextSeq())
			if err != nil {
				return err
			}
			partial = domain.ToolInvocation{SessionID: "s", Principal: p, ConversationID: i.ConversationID, ExchangeID: x.IDs[0], CallID: "producing-partial", ToolCallID: "tool", TurnID: task.TurnID}
			call := storetest.NewCall("s", partial.CallID, partial.ConversationID, tx.NextSeq())
			call.Principal, call.ServiceActor = p, actor
			call = storetest.Reseal(call)
			if err := tx.InsertCall(call); err != nil {
				return err
			}
			attempt := storetest.NewAttempt("s", call.CallID, 1, tx.NextSeq())
			if err := tx.PutCallAttempt(attempt); err != nil {
				return err
			}
			call.State, call.Attempts = domain.CallSent, 1
			if _, err = tx.UpdateCall(call, call.Revision); err != nil {
				return err
			}
			// The exchange is otherwise complete — output item, output
			// member, tool-call member — so the partial call state is the
			// only thing that can distinguish refusal from execution. The
			// membership service itself refuses to register members of an
			// unfinished call, so these two members are inserted raw, the
			// way rawDuplicateOf models service-bypassing state in graph:
			// the predicate must cope with it however it was produced.
			output := storetest.NewItem("s", "output-partial", tx.NextSeq(), "partial assistant output")
			output.Authority, output.CreatedTurn, output.Role, output.AgentID = domain.AuthorityAgent, task.Turn, domain.RoleTranscript, p.AgentID
			output.Scope, output.Access = domain.ScopeTask, conversationBoundary(p)
			if err := tx.InsertItem(output); err != nil {
				return err
			}
			source := storetest.ContentRef(output)
			for _, m := range []domain.ExchangeMember{
				{SemanticMeta: storetest.Meta("s", "output-partial", tx.NextSeq()), ExchangeID: x.IDs[0], Position: 1, Role: domain.MemberOutput, Source: source, CallID: call.CallID},
				{SemanticMeta: storetest.Meta("s", "tool-partial", tx.NextSeq()), ExchangeID: x.IDs[0], Position: 2, Role: domain.MemberToolCall, Source: source, CallID: call.CallID, ToolCallID: partial.ToolCallID},
			} {
				if err := sem.InsertExchangeMember(m); err != nil {
					return err
				}
			}
			// The output's registration is what starts EXECUTING; apply the
			// same transition so the open state is not a second deviation.
			stored, err := sem.LogicalExchange(x.IDs[0])
			if err != nil {
				return err
			}
			stored.State = domain.ExchangeExecuting
			if _, err = sem.PutLogicalExchange(stored, stored.Revision); err != nil {
				return err
			}
			return nil
		})
		var before uint64
		err := FixedError(st.Update(testContext, "s", func(tx store.Tx) error {
			before = tx.LastSeq()
			_, err := s.Remember(tx, dispatcher(partial), Request[domain.KeyedWriteIntent]{partial, keyed("partial", "k", "v")}, tx.NextSeq())
			return err
		}))
		if err == nil || err.Error() != domain.ToolErrorUnavailable.Message() {
			t.Fatalf("partial output executed: %v", err)
		}
		update(t, st, func(tx store.Tx) error {
			if tx.LastSeq() != before {
				t.Fatalf("refused partial execution wrote state: seq %d -> %d", before, tx.LastSeq())
			}
			return nil
		})
		// Control on the same store: the completed output's request executes.
		r := remember(t, st, s, i, keyed("control", "k", "v"))
		if r.ItemID == "" {
			t.Fatalf("control filing: %+v", r)
		}
	})
}
