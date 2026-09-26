package retrieve

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestBuildRetrievalRecordsPreservesSourceAndExactLease(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	source := storetest.NewItem("s", "source", 1, "historical evidence")
	source.Residency = domain.ResidencyArchived
	task := domain.TaskState{SessionID: "s", TaskID: p.TaskID, WorkflowID: p.WorkflowID, Status: domain.TaskActive, Turn: 2, TurnID: "turn-2", Version: 1}
	conv := domain.Conversation{SessionID: "s", ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TaskID: p.TaskID, AgentID: p.AgentID, LogicalCalls: 3, Version: 1, Revision: 1}
	origin := domain.RetrievalOrigin{Holder: p, ConversationID: conv.ConversationID, TurnID: task.TurnID}
	observed := domain.ObservedItemState{Source: domain.ItemContentRef{ItemID: source.ID, ContentHash: source.ContentHash}, Version: source.Version, Currentness: domain.ItemHistorical, Generation: source.Generation, Residency: source.Residency, Authority: source.Authority, Expiry: domain.ExpiryExpired}
	intent := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "request", ItemID: source.ID}, Origin: origin, Method: "rehydrate"}
	args, err := retrievalArguments(intent, leasePolicy())
	if err != nil {
		t.Fatal(err)
	}
	in := recordInput{Source: source, Observed: observed, Task: task, Conversation: conv, Actor: p, Intent: intent, Policy: leasePolicy(), Arguments: args, Allowance: 2, Seqs: recordSeqs{Lease: 2, Coverage: 3, Item: 4, Projection: 5, Result: 6, Event: 7, Receipt: 8}}
	records, err := buildRetrievalRecords(in)
	if err != nil {
		t.Fatal(err)
	}
	if source.Residency != domain.ResidencyArchived || records.Lease.Source != observed.Source || records.Lease.IssuedCompletedInferenceIndex != 3 || records.Lease.CallAllowance != 2 {
		t.Fatalf("source or lease changed: %+v", records.Lease)
	}
	if records.Item.Role != domain.RoleProjection || records.Item.Authority != domain.AuthorityTool || records.Item.Access != records.Projection.Access || records.Coverage.MemberCount != 1 || records.Member.LeaseID != records.Lease.ID || *records.Member.Source != observed.Source || records.Projection.DependencyCoverageID != records.Coverage.ID || records.Projection.RetrievalResultID != records.Result.ID || records.Result.RetrievalEventID != records.Event.ID || records.Receipt.Result.Tool.RetrievalResultID != records.Result.ID {
		t.Fatalf("broken persisted chain: %+v", records)
	}
	if err := records.Result.ValidateOriginEvent(records.Event); err != nil {
		t.Fatal(err)
	}
	for _, validate := range []func() error{records.Item.ValidateSemantic, records.Coverage.Validate, records.Member.Validate, records.Projection.Validate, records.Lease.Validate, records.Result.Validate, records.Event.Validate, records.Receipt.Validate} {
		if err := validate(); err != nil {
			t.Fatalf("invalid record: %v", err)
		}
	}
}
