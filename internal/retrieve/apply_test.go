package retrieve

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

type applyTx struct {
	store.Tx
	sem       *applySemantic
	source    domain.ContextItem
	extra     map[string]domain.ContextItem
	task      domain.TaskState
	conv      domain.Conversation
	seq       uint64
	items     []domain.ContextItem
	poisoned  error
	taskReads int
}

func (t *applyTx) SemanticTransaction() (store.SemanticTx, error) { return t.sem, nil }
func (t *applyTx) LastSeq() uint64                                { return t.seq }
func (t *applyTx) NextSeq() uint64                                { t.seq++; return t.seq }
func (t *applyTx) Poison(err error)                               { t.poisoned = err }
func (t *applyTx) Task(string) (domain.TaskState, error) {
	t.taskReads++
	return t.task, nil
}
func (t *applyTx) Conversation(string) (domain.Conversation, error) { return t.conv, nil }
func (t *applyTx) Item(id string) (domain.ContextItem, error) {
	if item, ok := t.extra[id]; ok {
		return item.Clone(), nil
	}
	if id != t.source.ID {
		return domain.ContextItem{}, domain.ErrNotFound
	}
	return t.source.Clone(), nil
}
func (t *applyTx) Relationships(store.RelationshipFilter) ([]domain.Relationship, error) {
	return nil, nil
}
func (t *applyTx) InsertItem(item domain.ContextItem) error {
	t.items = append(t.items, item)
	return nil
}

type applySemantic struct {
	store.SemanticTx
	leases         []domain.RetrievalLease
	results        map[string]domain.RetrievalResult
	receipts       map[string]domain.MutationReceipt
	coverages      []domain.CoverageRecord
	projections    []domain.ProjectionRecord
	events         []domain.RetrievalEvent
	failAt         string
	oldProjections map[string]domain.ProjectionRecord
	oldCoverages   map[string]domain.CoverageRecord
	oldMembers     map[string][]domain.CoverageMember
}

func (s *applySemantic) MutationReceipt(_ domain.MutationFamily, id string) (domain.MutationReceipt, error) {
	if r, ok := s.receipts[id]; ok {
		return r, nil
	}
	return domain.MutationReceipt{}, domain.ErrNotFound
}
func (s *applySemantic) RetrievalResult(id string) (domain.RetrievalResult, error) {
	if r, ok := s.results[id]; ok {
		return r, nil
	}
	return domain.RetrievalResult{}, domain.ErrNotFound
}
func (s *applySemantic) RetrievalLease(id string) (domain.RetrievalLease, error) {
	for _, lease := range s.leases {
		if lease.ID == id {
			return lease, nil
		}
	}
	return domain.RetrievalLease{}, domain.ErrNotFound
}
func (s *applySemantic) ProjectionByItem(id string) (domain.ProjectionRecord, error) {
	if p, ok := s.oldProjections[id]; ok {
		return p, nil
	}
	return domain.ProjectionRecord{}, domain.ErrNotFound
}
func (s *applySemantic) Coverage(id string) (domain.CoverageRecord, error) {
	if c, ok := s.oldCoverages[id]; ok {
		return c, nil
	}
	return domain.CoverageRecord{}, domain.ErrNotFound
}
func (s *applySemantic) CoverageMembers(id string, _ store.Page) (store.ResultPage[domain.CoverageMember], error) {
	return store.ResultPage[domain.CoverageMember]{Records: s.oldMembers[id]}, nil
}
func (s *applySemantic) LeasesByHolder(domain.Principal, string, string, store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	return store.ResultPage[domain.RetrievalLease]{Records: s.leases}, nil
}
func (s *applySemantic) InsertRetrievalLease(v domain.RetrievalLease) error {
	s.leases = append(s.leases, v)
	return nil
}
func (s *applySemantic) InsertCoverage(v domain.CoverageRecord, _ []domain.CoverageMember) error {
	s.coverages = append(s.coverages, v)
	return nil
}
func (s *applySemantic) InsertProjection(v domain.ProjectionRecord) error {
	if s.failAt == "projection" {
		return domain.ErrIntegrity
	}
	s.projections = append(s.projections, v)
	return nil
}
func (s *applySemantic) InsertRetrievalResult(v domain.RetrievalResult) error {
	s.results[v.ID] = v
	return nil
}
func (s *applySemantic) InsertRetrievalEvent(v domain.RetrievalEvent) error {
	if s.failAt == "event" {
		return domain.ErrIntegrity
	}
	s.events = append(s.events, v)
	return nil
}
func (s *applySemantic) InsertMutationReceipt(v domain.MutationReceipt) error {
	s.receipts[v.RequestID] = v
	return nil
}

func TestApplyPersistsLeaseAndReplaysWithoutRenewal(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	source := storetest.NewItem("s", "source", 1, "historical content")
	source.Residency = domain.ResidencyArchived
	sem := &applySemantic{results: map[string]domain.RetrievalResult{}, receipts: map[string]domain.MutationReceipt{}}
	tx := &applyTx{sem: sem, source: source, seq: 1,
		task: domain.TaskState{SessionID: "s", TaskID: p.TaskID, WorkflowID: p.WorkflowID, Status: domain.TaskActive, Turn: 1, TurnID: "turn", Version: 1},
		conv: domain.Conversation{SessionID: "s", ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TaskID: p.TaskID, AgentID: p.AgentID, Version: 1, Revision: 1}}
	i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "request", ItemID: source.ID}, Origin: domain.RetrievalOrigin{Holder: p, ConversationID: tx.conv.ConversationID, TurnID: "turn"}, Method: "rehydrate"}
	first, err := Apply(tx, p, i, leasePolicy(), false)
	if err != nil || len(sem.leases) != 1 || len(tx.items) != 1 || len(sem.coverages) != 1 || len(sem.projections) != 1 || len(sem.events) != 1 || source.Residency != domain.ResidencyArchived {
		t.Fatalf("persisted retrieval = %+v, %v", first, err)
	}
	oldLease := sem.leases[0]
	i.Rehydrate.RequestID = "coalesced-request"
	coalesced, err := Apply(tx, p, i, leasePolicy(), false)
	if err != nil || len(sem.leases) != 1 || coalesced.LeaseID != oldLease.ID || sem.leases[0] != oldLease || sem.projections[1].LeaseID != oldLease.ID {
		t.Fatalf("active request extended lease: %+v, %v", coalesced, err)
	}
	i.Rehydrate.RequestID = "request"
	seq, reads := tx.seq, tx.taskReads
	tx.conv.LogicalCalls = 2
	tx.task.Status, tx.task.CompletedSeq = domain.TaskCompleted, seq
	replayed, err := Apply(tx, p, i, leasePolicy(), false)
	if err != nil || replayed.ID != first.ID || tx.seq != seq || tx.taskReads != reads || len(sem.leases) != 1 {
		t.Fatalf("expired retry renewed lease: %+v, %v", replayed, err)
	}
	tx.task.Status, tx.task.CompletedSeq = domain.TaskActive, 0
	i.Rehydrate.RequestID = "new-request"
	newResult, err := Apply(tx, p, i, leasePolicy(), false)
	if err != nil || newResult.ID == first.ID || len(sem.leases) != 2 || sem.leases[0].CallAllowance != 2 || sem.leases[0].IssuedCompletedInferenceIndex != 0 || sem.leases[1].IssuedCompletedInferenceIndex != 2 {
		t.Fatalf("new request lease = %+v, %v", newResult, err)
	}
}

func TestApplyPoisonsTransactionAfterPartialWrite(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	source := storetest.NewItem("s", "source", 1, "content")
	sem := &applySemantic{results: map[string]domain.RetrievalResult{}, receipts: map[string]domain.MutationReceipt{}, failAt: "projection"}
	tx := &applyTx{sem: sem, source: source, seq: 1,
		task: domain.TaskState{SessionID: "s", TaskID: p.TaskID, WorkflowID: p.WorkflowID, Status: domain.TaskActive, Turn: 1, TurnID: "turn", Version: 1},
		conv: domain.Conversation{SessionID: "s", ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TaskID: p.TaskID, AgentID: p.AgentID, Version: 1, Revision: 1}}
	i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "request", ItemID: source.ID}, Origin: domain.RetrievalOrigin{Holder: p, ConversationID: tx.conv.ConversationID, TurnID: "turn"}, Method: "rehydrate"}
	_, err := Apply(tx, p, i, leasePolicy(), false)
	if !errors.Is(err, domain.ErrIntegrity) || !errors.Is(tx.poisoned, domain.ErrIntegrity) {
		t.Fatalf("partial write was not poisoned: %v / %v", err, tx.poisoned)
	}
}

func TestApplyRejectsProjectionSourceWithoutInheritedCoverage(t *testing.T) {
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	source := storetest.NewItem("s", "old-projection", 1, "copied content")
	source.Role, source.Kind, source.Authority = domain.RoleProjection, domain.KindToolResult, domain.AuthorityTool
	sem := &applySemantic{results: map[string]domain.RetrievalResult{}, receipts: map[string]domain.MutationReceipt{}}
	tx := &applyTx{sem: sem, source: source, seq: 1,
		task: domain.TaskState{SessionID: "s", TaskID: p.TaskID, WorkflowID: p.WorkflowID, Status: domain.TaskActive, Turn: 1, TurnID: "turn", Version: 1},
		conv: domain.Conversation{SessionID: "s", ConversationID: domain.ConversationIDFor(p.TaskID, p.AgentID), TaskID: p.TaskID, AgentID: p.AgentID, Version: 1, Revision: 1}}
	i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "request", ItemID: source.ID}, Origin: domain.RetrievalOrigin{Holder: p, ConversationID: tx.conv.ConversationID, TurnID: "turn"}, Method: "rehydrate"}
	_, err := Apply(tx, p, i, leasePolicy(), false)
	if !errors.Is(err, domain.ErrIncompleteCoverage) || tx.seq != 1 || len(sem.leases) != 0 {
		t.Fatalf("projection renewed without inherited lease: %v", err)
	}
}

func TestApplyProjectionSourceCarriesOldLeaseAndRejectsExpiry(t *testing.T) {
	d := dependencyFixture(t)
	p := d.Principal
	p.Authority = domain.AuthorityHarness
	d.Projection.Origin.Holder, d.Projection.Origin.Invocation = p, nil
	oldLease := d.Leases["lease"]
	oldLease.Holder = p
	d.Leases[oldLease.ID] = oldLease
	projected := storetest.NewItem("s", d.Projection.ItemID, 4, "copied source")
	projected.Role, projected.Kind, projected.Authority = domain.RoleProjection, domain.KindToolResult, domain.AuthorityTool
	projected.Scope, projected.Access = domain.ScopeAgent, d.Projection.Access
	projected.Source = &domain.SourceRef{Kind: domain.SourceItem, Locator: d.Projection.Source.ItemID, ContentHash: d.Projection.Source.ContentHash}
	sem := &applySemantic{results: map[string]domain.RetrievalResult{}, receipts: map[string]domain.MutationReceipt{},
		leases: []domain.RetrievalLease{oldLease}, oldProjections: map[string]domain.ProjectionRecord{projected.ID: d.Projection},
		oldCoverages: d.Coverages, oldMembers: d.Members}
	tx := &applyTx{sem: sem, source: projected, extra: d.Sources, task: d.Task, conv: d.Conversation, seq: 4}
	i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: "new", ItemID: projected.ID}, Origin: d.Projection.Origin, Method: "rehydrate"}
	result, err := Apply(tx, p, i, leasePolicy(), false)
	if err != nil || result.LeaseID == oldLease.ID || len(sem.leases) != 2 || len(sem.coverages) != 1 || sem.coverages[0].MemberCount != 3 {
		t.Fatalf("projection retrieval lost nested lease: %+v, %v", result, err)
	}
	seq := tx.seq
	tx.conv.LogicalCalls = oldLease.CallAllowance
	i.Rehydrate.RequestID = "after-expiry"
	_, err = Apply(tx, p, i, leasePolicy(), false)
	if !errors.Is(err, domain.ErrLeaseExpired) || tx.seq != seq || len(sem.leases) != 2 {
		t.Fatalf("expired original lease renewed: %v", err)
	}
}
