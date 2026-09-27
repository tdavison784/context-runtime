package retrieve

import (
	"errors"
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
	for _, member := range records.Members {
		key, err := member.Key()
		if err != nil || member.ID != key {
			t.Fatalf("noncanonical coverage member ID %q, key %q: %v", member.ID, key, err)
		}
	}
}

func TestBuildRetrievalRecordsInheritsOldProjectionLease(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	source := storetest.NewItem("s", "old-projection", 1, "copied evidence")
	source.Role, source.Kind, source.Authority = domain.RoleProjection, domain.KindToolResult, domain.AuthorityTool
	source.Source = &domain.SourceRef{Kind: domain.SourceItem, Locator: "original", ContentHash: domain.HashBytes([]byte("original"))}
	task := domain.TaskState{SessionID: "s", TaskID: p.TaskID, WorkflowID: p.WorkflowID, Status: domain.TaskActive, Turn: 1, TurnID: "turn", Version: 1}
	conv := domain.Conversation{SessionID: "s", ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TaskID: p.TaskID, AgentID: p.AgentID, Version: 1, Revision: 1}
	origin := domain.RetrievalOrigin{Holder: p, ConversationID: conv.ConversationID, TurnID: task.TurnID}
	old := domain.ProjectionRecord{SemanticMeta: domain.SemanticMeta{ID: "old-record", SessionID: "s", Seq: 1, SchemaVersion: domain.SemanticSchemaV1},
		ItemID: source.ID, Source: domain.ItemContentRef{ItemID: "original", ContentHash: source.Source.ContentHash},
		LeaseID: "old-lease", DependencyCoverageID: "old-coverage", RetrievalResultID: "old-result", Origin: origin,
		Access: source.Access, DeliveryPolicyVersion: "retrieval-full/v1"}
	observed := domain.ObservedItemState{Source: domain.ItemContentRef{ItemID: source.ID, ContentHash: source.ContentHash}, Version: 1,
		Currentness: domain.ItemUnkeyed, Generation: source.Generation, Residency: source.Residency, Authority: source.Authority, Expiry: domain.ExpiryLive}
	i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "new", ItemID: source.ID}, Origin: origin, Method: "rehydrate"}
	args, _ := retrievalArguments(i, leasePolicy())
	oldMember := domain.CoverageMember{SemanticMeta: domain.SemanticMeta{ID: "old-member", SessionID: "s", Seq: 1, SchemaVersion: domain.SemanticSchemaV1},
		CoverageID: old.DependencyCoverageID, Source: &old.Source, LeaseID: old.LeaseID}
	in := recordInput{Source: source, Observed: observed, Task: task, Conversation: conv, Actor: p,
		Intent: i, Policy: leasePolicy(), Arguments: args, Allowance: 2, Inherited: &old,
		Seqs: recordSeqs{Lease: 2, Coverage: 3, Item: 4, Projection: 5, Result: 6, Event: 7, Receipt: 8}}
	for name, members := range map[string][]domain.CoverageMember{
		"missing closure": nil,
		"foreign member":  {func() domain.CoverageMember { m := oldMember; m.CoverageID = "other"; return m }()},
	} {
		in.InheritedMembers = members
		if _, err := buildRetrievalRecords(in); !errors.Is(err, domain.ErrIncompleteCoverage) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
	in.InheritedMembers = []domain.CoverageMember{oldMember}
	r, err := buildRetrievalRecords(in)
	if err != nil || r.Coverage.MemberCount != 3 || len(r.Members) != 3 {
		t.Fatalf("inherited coverage = %+v, %v", r.Coverage, err)
	}
	oldPair, nested := false, false
	for _, m := range r.Members {
		key, err := m.Key()
		if err != nil || m.ID != key {
			t.Fatalf("noncanonical nested member ID %q, key %q: %v", m.ID, key, err)
		}
		oldPair = oldPair || m.Source != nil && *m.Source == old.Source && m.LeaseID == old.LeaseID
		nested = nested || m.NestedCoverageID == old.DependencyCoverageID
	}
	if !oldPair || !nested {
		t.Fatalf("original dependencies lost: %+v", r.Members)
	}
}
