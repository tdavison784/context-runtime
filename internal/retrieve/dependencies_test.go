package retrieve

import (
	"cmp"
	"errors"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func dependencyFixture(t *testing.T) DependencySnapshot {
	t.Helper()
	p := storetest.NewPrincipal("s", domain.AuthorityAgent)
	ref := domain.ItemContentRef{ItemID: "source", ContentHash: domain.ContentHash([]domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: "source text"}})}
	item := storetest.NewItem("s", "source", 1, "source text")
	item.Scope = domain.ScopeAgent
	item.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "agent"}
	lease := domain.RetrievalLease{
		SemanticMeta: domain.SemanticMeta{ID: "lease", SessionID: "s", Seq: 2, SchemaVersion: domain.SemanticSchemaV1},
		Holder:       p, ConversationID: domain.ConversationIDFor("task", "agent"), TurnID: "turn-1",
		Source: ref, CallAllowance: 2, PolicyVersion: "policy",
	}
	member := domain.CoverageMember{
		SemanticMeta: domain.SemanticMeta{ID: "member", SessionID: "s", Seq: 3, SchemaVersion: domain.SemanticSchemaV1},
		CoverageID:   "coverage", Source: &ref, LeaseID: "lease",
	}
	coverage := domain.CoverageRecord{
		SemanticMeta: domain.SemanticMeta{ID: "coverage", SessionID: "s", Seq: 3, SchemaVersion: domain.SemanticSchemaV1},
		Purpose:      domain.CoverageLeaseDependency, Access: item.Access, MemberCount: 1,
	}
	var err error
	coverage.Signature, err = domain.CoverageSignature(coverage, []domain.CoverageMember{member})
	if err != nil {
		t.Fatal(err)
	}
	invocation := domain.ToolInvocation{SessionID: "s", ConversationID: lease.ConversationID, CallID: "call", ToolCallID: "tool-call", ExchangeID: "exchange", TurnID: "turn-1", Principal: p}
	projection := domain.ProjectionRecord{
		SemanticMeta: domain.SemanticMeta{ID: "projection", SessionID: "s", Seq: 4, SchemaVersion: domain.SemanticSchemaV1},
		ItemID:       "projection-item", Source: ref, LeaseID: "lease", RetrievalResultID: "result", DependencyCoverageID: "coverage",
		Origin: domain.RetrievalOrigin{Holder: p, ConversationID: lease.ConversationID, TurnID: "turn-1", Invocation: &invocation},
		Access: item.Access, DeliveryPolicyVersion: "delivery",
	}
	return DependencySnapshot{
		Principal: p, Projection: projection, MaxMembers: 4, SnapshotSeq: 4, DispatchTurn: "turn-1",
		Task:         domain.TaskState{SessionID: "s", TaskID: "task", WorkflowID: "wf", Status: domain.TaskActive, Turn: 1, TurnID: "turn-1", Version: 1},
		Conversation: domain.Conversation{SessionID: "s", ConversationID: lease.ConversationID, TaskID: "task", AgentID: "agent", Version: 1, Revision: 1},
		Coverages:    map[string]domain.CoverageRecord{"coverage": coverage},
		Members:      map[string][]domain.CoverageMember{"coverage": {member}},
		Sources:      map[string]domain.ContextItem{"source": item},
		Leases:       map[string]domain.RetrievalLease{"lease": lease},
	}
}

func TestProjectionRequiresOriginalLiveLease(t *testing.T) {
	d := dependencyFixture(t)
	if err := CheckProjectionDependencies(d); err != nil {
		t.Fatal(err)
	}
	d.Conversation.LogicalCalls = 2
	if err := CheckProjectionDependencies(d); !errors.Is(err, domain.ErrLeaseExpired) {
		t.Fatalf("expired original lease: %v", err)
	}
	d.Conversation.LogicalCalls = 0
	delete(d.Leases, "lease")
	d.Leases["new-lease"] = domain.RetrievalLease{SemanticMeta: domain.SemanticMeta{ID: "new-lease"}}
	if err := CheckProjectionDependencies(d); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("new lease substituted for old: %v", err)
	}
}

func TestProjectionRejectsMissingOrChangedSource(t *testing.T) {
	d := dependencyFixture(t)
	d.Members["coverage"] = nil
	if err := CheckProjectionDependencies(d); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("missing member: %v", err)
	}
	d = dependencyFixture(t)
	source := d.Sources["source"]
	source.ContentHash = domain.HashBytes([]byte("different"))
	d.Sources["source"] = source
	if err := CheckProjectionDependencies(d); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("changed content: %v", err)
	}
}

func TestNestedOldLeaseCannotBeRenewedByNewRootLease(t *testing.T) {
	d := dependencyFixture(t)
	old := storetest.NewItem("s", "old-source", 5, "old copied text")
	old.Scope, old.Access = domain.ScopeAgent, d.Sources["source"].Access
	ref := domain.ItemContentRef{ItemID: old.ID, ContentHash: old.ContentHash}
	lease := d.Leases["lease"]
	lease.ID, lease.Source, lease.Seq = "old-lease", ref, 5
	lease.CallAllowance = 1
	rootLease := d.Leases["lease"]
	rootLease.IssuedCompletedInferenceIndex = 1
	d.Leases["lease"] = rootLease
	d.Conversation.LogicalCalls, d.SnapshotSeq = 1, 6
	d.Sources[old.ID], d.Leases[lease.ID] = old, lease
	childMember := domain.CoverageMember{SemanticMeta: domain.SemanticMeta{ID: "old-member", SessionID: "s", Seq: 6, SchemaVersion: domain.SemanticSchemaV1}, CoverageID: "old-coverage", Source: &ref, LeaseID: lease.ID}
	child := domain.CoverageRecord{SemanticMeta: domain.SemanticMeta{ID: "old-coverage", SessionID: "s", Seq: 6, SchemaVersion: domain.SemanticSchemaV1}, Purpose: domain.CoverageLeaseDependency, Access: old.Access, MemberCount: 1}
	child.Signature, _ = domain.CoverageSignature(child, []domain.CoverageMember{childMember})
	d.Coverages[child.ID], d.Members[child.ID] = child, []domain.CoverageMember{childMember}
	root := d.Coverages["coverage"]
	nested := domain.CoverageMember{SemanticMeta: domain.SemanticMeta{ID: "nested", SessionID: "s", Seq: root.Seq, SchemaVersion: domain.SemanticSchemaV1}, CoverageID: root.ID, NestedCoverageID: child.ID}
	members := append(d.Members[root.ID], nested)
	slices.SortFunc(members, func(a, b domain.CoverageMember) int { x, _ := a.Key(); y, _ := b.Key(); return cmp.Compare(x, y) })
	root.MemberCount = uint64(len(members))
	root.Signature, _ = domain.CoverageSignature(root, members)
	d.Coverages[root.ID], d.Members[root.ID] = root, members
	if err := CheckProjectionDependencies(d); !errors.Is(err, domain.ErrLeaseExpired) {
		t.Fatalf("expired nested lease with new root lease = %v", err)
	}
	lease.CallAllowance = 2
	d.Leases[lease.ID] = lease
	if err := CheckProjectionDependencies(d); err != nil {
		t.Fatalf("both leases live: %v", err)
	}
}

func TestProjectionRejectsCyclicAndOverBudgetCoverage(t *testing.T) {
	d := dependencyFixture(t)
	root := d.Coverages["coverage"]
	child := domain.CoverageRecord{SemanticMeta: domain.SemanticMeta{ID: "child", SessionID: "s", Seq: 5, SchemaVersion: domain.SemanticSchemaV1},
		Purpose: domain.CoverageLeaseDependency, Access: root.Access, MemberCount: 1}
	back := domain.CoverageMember{SemanticMeta: domain.SemanticMeta{ID: "back", SessionID: "s", Seq: 5, SchemaVersion: domain.SemanticSchemaV1}, CoverageID: child.ID, NestedCoverageID: root.ID}
	child.Signature, _ = domain.CoverageSignature(child, []domain.CoverageMember{back})
	d.Coverages[child.ID], d.Members[child.ID] = child, []domain.CoverageMember{back}
	forward := domain.CoverageMember{SemanticMeta: domain.SemanticMeta{ID: "forward", SessionID: "s", Seq: root.Seq, SchemaVersion: domain.SemanticSchemaV1}, CoverageID: root.ID, NestedCoverageID: child.ID}
	members := append(d.Members[root.ID], forward)
	slices.SortFunc(members, func(a, b domain.CoverageMember) int { x, _ := a.Key(); y, _ := b.Key(); return cmp.Compare(x, y) })
	root.MemberCount = uint64(len(members))
	root.Signature, _ = domain.CoverageSignature(root, members)
	d.Coverages[root.ID], d.Members[root.ID] = root, members
	d.SnapshotSeq = 5
	if err := CheckProjectionDependencies(d); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("cyclic coverage = %v", err)
	}
	d.MaxMembers = 1
	if err := CheckProjectionDependencies(d); !errors.Is(err, domain.ErrResourceLimit) {
		t.Fatalf("over-budget coverage = %v", err)
	}
}

func TestDerivedRepresentationRetainsProjectionLeaseAndNestedCoverage(t *testing.T) {
	d := dependencyFixture(t)
	projected := storetest.NewItem("s", d.Projection.ItemID, 6, "copied history")
	projected.Role, projected.Kind, projected.Authority = domain.RoleProjection, domain.KindToolResult, domain.AuthorityTool
	projected.Scope, projected.Access = domain.ScopeAgent, d.Projection.Access
	projected.Source = &domain.SourceRef{Kind: domain.SourceItem, Locator: d.Projection.Source.ItemID, ContentHash: d.Projection.Source.ContentHash}
	derived := storetest.NewItem("s", "derived", 7, "summary")
	derived.Scope, derived.Access = domain.ScopeAgent, d.Projection.Access
	root := domain.CoverageRecord{SemanticMeta: domain.SemanticMeta{ID: "representation", SessionID: "s", Seq: 8, SchemaVersion: domain.SemanticSchemaV1},
		Purpose: domain.CoverageRepresentation, Access: derived.Access, MemberCount: 3}
	projectedRef := domain.ItemContentRef{ItemID: projected.ID, ContentHash: projected.ContentHash}
	originalRef := d.Projection.Source
	members := []domain.CoverageMember{
		{SemanticMeta: domain.SemanticMeta{ID: "projected-member", SessionID: "s", Seq: 8, SchemaVersion: domain.SemanticSchemaV1}, CoverageID: root.ID, Source: &projectedRef},
		{SemanticMeta: domain.SemanticMeta{ID: "lease-member", SessionID: "s", Seq: 8, SchemaVersion: domain.SemanticSchemaV1}, CoverageID: root.ID, Source: &originalRef, LeaseID: d.Projection.LeaseID},
		{SemanticMeta: domain.SemanticMeta{ID: "nested-member", SessionID: "s", Seq: 8, SchemaVersion: domain.SemanticSchemaV1}, CoverageID: root.ID, NestedCoverageID: d.Projection.DependencyCoverageID},
	}
	slices.SortFunc(members, func(a, b domain.CoverageMember) int { x, _ := a.Key(); y, _ := b.Key(); return cmp.Compare(x, y) })
	root.Signature, _ = domain.CoverageSignature(root, members)
	d.Coverages[root.ID], d.Members[root.ID] = root, members
	d.Sources[projected.ID] = projected
	d.Projections = map[string]domain.ProjectionRecord{projected.ID: d.Projection}
	d.Derived, d.RootCoverageID, d.SnapshotSeq = derived, root.ID, 8
	d.Link = storetest.NewRelationship("s", "derived-link", domain.RelDerivedFrom, derived.ID, projected.ID, 8)
	d.Link.CoverageID = root.ID
	if err := CheckRepresentationDependencies(d); err != nil {
		t.Fatalf("complete inherited coverage = %v", err)
	}
	d.Conversation.LogicalCalls = 2
	if err := CheckRepresentationDependencies(d); !errors.Is(err, domain.ErrLeaseExpired) {
		t.Fatalf("expired inherited lease = %v", err)
	}
	d.Conversation.LogicalCalls = 0
	delete(d.Projections, projected.ID)
	if err := CheckRepresentationDependencies(d); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("missing projection companion = %v", err)
	}
}

func TestProjectionSourceRequiresItsOriginalNestedLease(t *testing.T) {
	d := dependencyFixture(t)
	item := d.Sources["source"]
	item.Role, item.Kind, item.Authority = domain.RoleProjection, domain.KindToolResult, domain.AuthorityTool
	oldRef := domain.ItemContentRef{ItemID: "original", ContentHash: domain.HashBytes([]byte("original"))}
	item.Source = &domain.SourceRef{Kind: domain.SourceItem, Locator: oldRef.ItemID, ContentHash: oldRef.ContentHash}
	d.Sources[item.ID] = item
	d.Projections = map[string]domain.ProjectionRecord{item.ID: {
		SemanticMeta: domain.SemanticMeta{ID: "original-projection", SessionID: "s", Seq: 1, SchemaVersion: domain.SemanticSchemaV1},
		ItemID:       item.ID, Source: oldRef, LeaseID: "original-lease", RetrievalResultID: "original-result",
		DependencyCoverageID: "original-coverage", Origin: d.Projection.Origin, Access: item.Access, DeliveryPolicyVersion: "delivery",
	}}
	if err := CheckProjectionDependencies(d); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("copied projection omitted original lease = %v", err)
	}
}
