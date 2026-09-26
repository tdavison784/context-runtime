package retrieve

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

type dependencyTx struct {
	store.ReadTx
	items map[string]domain.ContextItem
	seq   uint64
}

func (t dependencyTx) LastSeq() uint64 { return t.seq }
func (t dependencyTx) Item(id string) (domain.ContextItem, error) {
	if item, ok := t.items[id]; ok {
		return item, nil
	}
	return domain.ContextItem{}, domain.ErrNotFound
}

type dependencyReader struct {
	store.SemanticReader
	d DependencySnapshot
}

func (r dependencyReader) Coverage(id string) (domain.CoverageRecord, error) {
	if c, ok := r.d.Coverages[id]; ok {
		return c, nil
	}
	return domain.CoverageRecord{}, domain.ErrNotFound
}
func (r dependencyReader) CoverageMembers(id string, _ store.Page) (store.ResultPage[domain.CoverageMember], error) {
	return store.ResultPage[domain.CoverageMember]{Records: r.d.Members[id]}, nil
}
func (r dependencyReader) RetrievalLease(id string) (domain.RetrievalLease, error) {
	if lease, ok := r.d.Leases[id]; ok {
		return lease, nil
	}
	return domain.RetrievalLease{}, domain.ErrNotFound
}
func (r dependencyReader) ProjectionByItem(id string) (domain.ProjectionRecord, error) {
	if p, ok := r.d.Projections[id]; ok {
		return p, nil
	}
	return domain.ProjectionRecord{}, domain.ErrNotFound
}

func TestStoredProjectionDependenciesUseOneBoundedSnapshot(t *testing.T) {
	d := dependencyFixture(t)
	tx := dependencyTx{items: d.Sources, seq: d.SnapshotSeq}
	r := dependencyReader{d: d}
	if err := CheckStoredProjectionDependencies(tx, r, d.Projection, d.Principal, d.Task, d.Conversation, 4, 16); err != nil {
		t.Fatal(err)
	}
	d.Conversation.LogicalCalls = 2
	if err := CheckStoredProjectionDependencies(tx, r, d.Projection, d.Principal, d.Task, d.Conversation, 4, 16); !errors.Is(err, domain.ErrLeaseExpired) {
		t.Fatalf("expired persisted lease = %v", err)
	}
	if err := CheckStoredProjectionDependencies(tx, r, d.Projection, d.Principal, d.Task, d.Conversation, 1, 1); !errors.Is(err, domain.ErrResourceLimit) {
		t.Fatalf("unbounded stored traversal = %v", err)
	}
}
