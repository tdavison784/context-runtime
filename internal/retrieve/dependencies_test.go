package retrieve

import (
	"errors"
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
		Invocation: invocation, Access: item.Access, DeliveryPolicyVersion: "delivery",
	}
	return DependencySnapshot{Principal: p, Projection: projection, Coverage: coverage, Members: []domain.CoverageMember{member}, Source: item, Lease: lease}
}

func TestProjectionRequiresOriginalLiveLease(t *testing.T) {
	d := dependencyFixture(t)
	if err := CheckProjectionDependencies(d, func(domain.RetrievalLease) bool { return true }); err != nil {
		t.Fatal(err)
	}
	if err := CheckProjectionDependencies(d, func(domain.RetrievalLease) bool { return false }); !errors.Is(err, domain.ErrLeaseExpired) {
		t.Fatalf("expired original lease: %v", err)
	}
	d.Lease.ID = "new-lease"
	if err := CheckProjectionDependencies(d, func(domain.RetrievalLease) bool { return true }); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("new lease substituted for old: %v", err)
	}
}

func TestProjectionRejectsMissingOrChangedSource(t *testing.T) {
	d := dependencyFixture(t)
	d.Members = nil
	if err := CheckProjectionDependencies(d, func(domain.RetrievalLease) bool { return true }); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("missing member: %v", err)
	}
	d = dependencyFixture(t)
	d.Source.ContentHash = domain.HashBytes([]byte("different"))
	if err := CheckProjectionDependencies(d, func(domain.RetrievalLease) bool { return true }); !errors.Is(err, domain.ErrIncompleteCoverage) {
		t.Fatalf("changed content: %v", err)
	}
}
