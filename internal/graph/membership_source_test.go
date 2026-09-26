package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestMembershipOutputRequiresCompletedCallAndOriginalSourceOwner(t *testing.T) {
	x, _, _, call := membershipAcknowledgmentFixture()
	it := storetest.NewItem("s", "output", 4, "output")
	it.Authority, it.CreatedTurn = domain.AuthorityAgent, x.Turn
	it.WorkflowID, it.TaskID, it.AgentID, it.TurnID = x.Principal.WorkflowID, x.Principal.TaskID, x.Principal.AgentID, x.TurnID
	intent := domain.RegisterExchangeMemberIntent{Role: domain.MemberOutput, Source: storetest.ContentRef(it), CallID: call.CallID}
	if err := checkMemberSource(x, intent, it, &call); err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{"authority", "turn", "agent", "content", "failed", "compaction", "dispatcher", "principal", "call-id"} {
		t.Run(change, func(t *testing.T) {
			source, c := it.Clone(), call.Clone()
			switch change {
			case "authority":
				source.Authority = domain.AuthorityRetrievedContent
			case "turn":
				source.CreatedTurn++
			case "agent":
				source.AgentID = "other"
			case "content":
				source.ContentHash = domain.HashBytes([]byte("other"))
			case "failed":
				c = storetest.Finish(c, domain.CallFailed, c.FinishedSeq)
			case "compaction":
				c.Operation = domain.OperationCompaction
			case "dispatcher":
				c.ServiceActor.Authority = domain.AuthorityAgent
			case "principal":
				c.Principal.AgentID = "other"
			case "call-id":
				c.CallID = "other"
			}
			c = storetest.Reseal(c)
			if err := checkMemberSource(x, intent, source, &c); err == nil {
				t.Fatal("unproven output admitted")
			}
		})
	}
	if err := checkMemberSource(x, intent, it, nil); err == nil {
		t.Fatal("missing call admitted")
	}
}
