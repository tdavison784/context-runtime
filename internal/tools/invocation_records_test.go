package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
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
