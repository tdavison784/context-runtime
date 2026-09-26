package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"testing"
)

type projectionDependencyFixture struct {
	store.ReadTx
	store.SemanticReader
	projection domain.ProjectionRecord
	lease      domain.RetrievalLease
	coverage   domain.CoverageRecord
	original   domain.ContextItem
}

func (f *projectionDependencyFixture) SemanticReadBackend() store.SemanticReader { return f }
func (f *projectionDependencyFixture) ProjectionByItem(string) (domain.ProjectionRecord, error) {
	return f.projection, nil
}
func (f *projectionDependencyFixture) RetrievalLease(string) (domain.RetrievalLease, error) {
	return f.lease, nil
}
func (f *projectionDependencyFixture) Coverage(string) (domain.CoverageRecord, error) {
	return f.coverage, nil
}
func (f *projectionDependencyFixture) Item(string) (domain.ContextItem, error) {
	return f.original, nil
}

func TestProjectionCoverageNeverSubstitutesANewerLease(t *testing.T) {
	actor := principal("s", domain.AuthorityHarness)
	original := taskItem("s", "original", 1, domain.AuthorityUser)
	source := taskItem("s", "projection-item", 2, domain.AuthorityTool)
	source.Role = domain.RoleProjection
	meta := func(id string) domain.SemanticMeta {
		return domain.SemanticMeta{ID: id, SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: 1}
	}
	ref := domain.ItemContentRef{ItemID: original.ID, ContentHash: original.ContentHash}
	origin := domain.RetrievalOrigin{Holder: actor, ConversationID: domain.ConversationIDFor(actor.TaskID, actor.AgentID), TurnID: "turn"}
	f := &projectionDependencyFixture{original: original,
		projection: domain.ProjectionRecord{SemanticMeta: meta("p"), ItemID: source.ID, Source: ref, LeaseID: "old-lease", RetrievalResultID: "result", DependencyCoverageID: "nested", Origin: origin, Access: source.Access, DeliveryPolicyVersion: "delivery"},
		lease:      domain.RetrievalLease{SemanticMeta: meta("old-lease"), Holder: actor, ConversationID: origin.ConversationID, TurnID: origin.TurnID, Source: ref, CallAllowance: 1, PolicyVersion: "policy"},
		coverage:   domain.CoverageRecord{SemanticMeta: meta("nested"), Purpose: domain.CoverageLeaseDependency, Access: source.Access, MemberCount: 1, Signature: domain.HashBytes(nil)},
	}
	deps, err := projectionDependencies(f, actor, source, source.Access)
	if err != nil || len(deps) != 2 || deps[0].LeaseID != "old-lease" || *deps[0].Source != ref || deps[1].NestedCoverageID != "nested" {
		t.Fatalf("lost original dependencies: %v, %v", deps, err)
	}
	f.lease.ID = "new-lease"
	if _, err := projectionDependencies(f, actor, source, source.Access); err == nil {
		t.Fatal("newer source lease substituted")
	}
}
