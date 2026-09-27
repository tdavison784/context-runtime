package storetest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Builders for retrieval leases, results, projections, and events
// (P3-28..30).

// AgentOrigin is the tool-call retrieval origin of agent "agent" in task
// "task" at turn "turn-1".
func AgentOrigin(sess string) domain.RetrievalOrigin {
	holder := AgentPrincipal(sess, "task", "agent")
	conv := domain.ConversationIDFor("task", "agent")
	inv := domain.ToolInvocation{SessionID: sess, ConversationID: conv, CallID: "call-1", ToolCallID: "tc-1", ExchangeID: "x1", TurnID: "turn-1", Principal: holder}
	return domain.RetrievalOrigin{Holder: holder, ConversationID: conv, TurnID: "turn-1", Invocation: &inv}
}

// AgentBoundary is the private boundary of agent "agent" in task "task".
func AgentBoundary(sess string) domain.AccessBoundary {
	return domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, TaskID: "task", AgentID: "agent"}
}

// NewLease leases source to origin's holder for one completed inference.
func NewLease(origin domain.RetrievalOrigin, id string, seq uint64, source domain.ItemContentRef) domain.RetrievalLease {
	return domain.RetrievalLease{SemanticMeta: Meta(origin.Holder.SessionID, id, seq), Holder: origin.Holder, ConversationID: origin.ConversationID,
		TurnID: origin.TurnID, Source: source, IssuedCompletedInferenceIndex: 3, CallAllowance: 1, PolicyVersion: domain.Phase3PolicyVersion}
}

// retrievalBundle is one successful retrieval: the lease, the TOOL
// projection item and its record, the result, and the audit event.
type retrievalBundle struct {
	lease      domain.RetrievalLease
	item       domain.ContextItem
	projection domain.ProjectionRecord
	result     domain.RetrievalResult
	event      domain.RetrievalEvent
	coverage   domain.CoverageRecord
	members    []domain.CoverageMember
}

// newRetrievalBundle builds a complete bundle for source at seq; the
// projection's dependency coverage names the source under the new lease.
func newRetrievalBundle(t *testing.T, source domain.ContextItem, id string, seq uint64) retrievalBundle {
	t.Helper()
	sess := source.SessionID
	origin := AgentOrigin(sess)
	ref := ContentRef(source)
	b := retrievalBundle{lease: NewLease(origin, "lease-"+id, seq, ref)}
	b.item = NewItem(sess, "proj-"+id, seq, "retrieved: "+id)
	b.item.Authority, b.item.Role, b.item.Scope, b.item.Access = domain.AuthorityTool, domain.RoleProjection, domain.ScopeAgent, AgentBoundary(sess)
	src := ref
	b.coverage, b.members = signCoverage(t, domain.CoverageRecord{SemanticMeta: Meta(sess, "depcov-"+id, seq), Purpose: domain.CoverageLeaseDependency, Access: AgentBoundary(sess)},
		[]domain.CoverageMember{{SemanticMeta: Meta(sess, "", seq), CoverageID: "depcov-" + id, Source: &src, LeaseID: b.lease.ID}})
	invID, err := origin.Invocation.ID()
	if err != nil {
		t.Fatal(err)
	}
	b.projection = domain.ProjectionRecord{SemanticMeta: Meta(sess, "pr-"+id, seq), ItemID: b.item.ID, Source: ref, LeaseID: b.lease.ID,
		RetrievalResultID: "res-" + id, DependencyCoverageID: b.coverage.ID, Origin: origin, Access: AgentBoundary(sess), DeliveryPolicyVersion: "delivery/1"}
	b.result = domain.RetrievalResult{SemanticMeta: Meta(sess, "res-"+id, seq), RequestID: "req-" + id, LeaseID: b.lease.ID, ProjectionID: b.projection.ID,
		RetrievalEventID: "rev-" + id, Origin: origin, Observed: domain.ObservedItemState{Source: ref, Version: 1, Currentness: domain.ItemUnkeyed,
			Generation: source.Generation, Residency: source.Residency, Authority: source.Authority, Expiry: domain.ExpiryLive},
		Access: AgentBoundary(sess), PolicyVersion: domain.Phase3PolicyVersion}
	b.event = domain.RetrievalEvent{SemanticMeta: Meta(sess, "rev-"+id, seq), RequestID: "req-" + id, Principal: origin.Holder, TriggeringActor: origin.Holder,
		InvocationID: invID, ResultID: b.result.ID, Source: &src}
	return b
}

// insert writes the bundle through the facet in a fixed order: lease,
// projection item, coverage, projection, result, event.
func (b retrievalBundle) insert(t *testing.T, tx store.Tx) error {
	t.Helper()
	sem := semantic(t, tx)
	for _, step := range []func() error{
		func() error { return sem.InsertRetrievalLease(b.lease) },
		func() error { return tx.InsertItem(b.item) },
		func() error { return sem.InsertCoverage(b.coverage, b.members) },
		func() error { return sem.InsertProjection(b.projection) },
		func() error { return sem.InsertRetrievalResult(b.result) },
		func() error { return sem.InsertRetrievalEvent(b.event) },
	} {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

// testSemanticRetrieval checks a retrieval bundle (P3-28..30): lease,
// projection, result, and event commit together, their links are checked
// at commit, a lease-backed coverage member names its lease's exact
// source, and the reads find each record without changing the source.
func testSemanticRetrieval(t *testing.T, s store.Store) {
	var source domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		source = NewItem(sessA, "src", tx.NextSeq(), "historical fact")
		source.Scope, source.Access = domain.ScopeAgent, AgentBoundary(sessA)
		return tx.InsertItem(source)
	})
	var b retrievalBundle
	update(t, s, sessA, func(tx store.Tx) error {
		b = newRetrievalBundle(t, source, "1", tx.NextSeq())
		return b.insert(t, tx)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		lease, err := r.RetrievalLease(b.lease.ID)
		noErr(t, err)
		assertEqual(t, "RetrievalLease", lease, b.lease)
		res, err := r.RetrievalResult(b.result.ID)
		noErr(t, err)
		assertEqual(t, "RetrievalResult", res, b.result)
		ev, err := r.RetrievalEvent(b.event.ID)
		noErr(t, err)
		assertEqual(t, "RetrievalEvent", ev, b.event)
		pr, err := r.Projection(b.projection.ID)
		noErr(t, err)
		assertEqual(t, "Projection", pr, b.projection)
		byItem, err := r.ProjectionByItem(b.item.ID)
		noErr(t, err)
		assertEqual(t, "ProjectionByItem", byItem, b.projection)
		held, err := r.LeasesByHolder(b.lease.Holder, b.lease.ConversationID, b.lease.TurnID, store.Page{Limit: 5})
		noErr(t, err)
		assertEqual(t, "LeasesByHolder", held.Records, []domain.RetrievalLease{b.lease})
		other, err := r.LeasesByHolder(AgentPrincipal(sessA, "task", "other"), domain.ConversationIDFor("task", "other"), "turn-1", store.Page{Limit: 5})
		noErr(t, err)
		if len(other.Records) != 0 {
			t.Errorf("another holder's leases = %+v", other.Records)
		}
		bySource, err := r.LeasesBySource(ContentRef(source), store.Page{Limit: 5})
		noErr(t, err)
		assertEqual(t, "LeasesBySource", bySource.Records, []domain.RetrievalLease{b.lease})
		events, err := r.RetrievalEventsByRequest(b.event.Principal, "req-1", store.Page{Limit: 5})
		noErr(t, err)
		assertEqual(t, "RetrievalEventsByRequest", events.Records, []domain.RetrievalEvent{b.event})
		hidden, err := r.RetrievalEventsByRequest(AgentPrincipal(sessA, "task", "other"), "req-1", store.Page{Limit: 5})
		noErr(t, err)
		if len(hidden.Records) != 0 || hidden.More {
			t.Errorf("another agent sees retrieval events: %+v", hidden)
		}
		// Retrieval never changes the source (P3-28).
		got, err := tx.Item("src")
		noErr(t, err)
		assertEqual(t, "source after retrieval", got, source)
		return nil
	})
	// A denial carries no source identity and needs no result.
	update(t, s, sessA, func(tx store.Tx) error {
		deny := b.event
		deny.ID, deny.RequestID, deny.ResultID, deny.Source, deny.ErrorCode = "rev-deny", "req-2", "", nil, domain.ToolErrorNotFound
		deny.Seq = tx.NextSeq()
		return semantic(t, tx).InsertRetrievalEvent(deny)
	})
	for _, tc := range []struct {
		name string
		edit func(b *retrievalBundle)
		want error
	}{
		{"result without its event", func(b *retrievalBundle) { b.event.ID, b.result.RetrievalEventID = "rev-x", "rev-missing" }, domain.ErrInvalidRecord},
		{"event names another request", func(b *retrievalBundle) { b.event.RequestID = "req-other" }, domain.ErrInvalidRecord},
		{"projection origin differs from the result's", func(b *retrievalBundle) {
			o := AgentOrigin(sessA)
			inv := *o.Invocation
			inv.ToolCallID = "tc-9"
			o.Invocation = &inv
			b.projection.Origin = o
		}, domain.ErrInvalidRecord},
		{"result names another projection", func(b *retrievalBundle) { b.result.ProjectionID = "pr-missing" }, domain.ErrInvalidRecord},
		{"lease of another source", func(b *retrievalBundle) {
			b.lease.Source.ContentHash = domain.HashBytes([]byte("other content"))
		}, domain.ErrInvalidRecord},
		{"projection item not a TOOL projection", func(b *retrievalBundle) { b.item.Role = domain.RoleSemantic }, domain.ErrInvalidRecord},
		{"result broader than its source", func(b *retrievalBundle) {
			b.result.Access = domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sessA}
		}, domain.ErrInvalidRecord},
		{"lease reused", func(b *retrievalBundle) { b.lease.ID = "lease-1" }, domain.ErrImmutable},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			nb := newRetrievalBundle(t, source, "2", tx.NextSeq())
			tc.edit(&nb)
			if tc.name == "lease reused" {
				return semantic(t, tx).InsertRetrievalLease(nb.lease)
			}
			return nb.insert(t, tx)
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	// A lease-backed coverage member must name its lease's exact source.
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		other := NewItem(sessA, "other", tx.NextSeq(), "other")
		noErr(t, tx.InsertItem(other))
		ref := ContentRef(other)
		seq := tx.NextSeq()
		c, m := signCoverage(t, domain.CoverageRecord{SemanticMeta: Meta(sessA, "badcov", seq), Purpose: domain.CoverageLeaseDependency, Access: AgentBoundary(sessA)},
			[]domain.CoverageMember{{SemanticMeta: Meta(sessA, "", seq), CoverageID: "badcov", Source: &ref, LeaseID: b.lease.ID}})
		return semantic(t, tx).InsertCoverage(c, m)
	})
}
