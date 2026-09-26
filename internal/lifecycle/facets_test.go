package lifecycle

import (
	"context"
	"maps"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// facets supplies record families that no backend implements yet: the
// all-owner goal index, GC requests/results, collect receipts, GC candidates
// and source leases. Writes stage per transaction and publish only when the
// enclosing Update commits, mirroring backend atomicity. Reads never filter
// beyond each interface's documented contract.
type facets struct {
	items    []string // tracked item IDs; reads resolve them through the tx
	requests map[string]domain.GCRequest
	results  map[string]domain.GCResult // keyed by GC request ID
	collects map[string]domain.CollectReceipt
	leases   map[string][]domain.RetrievalLease // keyed by source item ID
}

func newFacets(items ...string) *facets {
	return &facets{items: items, requests: map[string]domain.GCRequest{}, results: map[string]domain.GCResult{},
		collects: map[string]domain.CollectReceipt{}, leases: map[string][]domain.RetrievalLease{}}
}

func (f *facets) update(mem store.Store, fn func(store.Tx) error) error {
	w := newFacets()
	err := mem.Update(context.Background(), "s", func(tx store.Tx) error { return fn(facetTx{Tx: tx, f: f, w: w}) })
	if err == nil {
		maps.Copy(f.requests, w.requests)
		maps.Copy(f.results, w.results)
		maps.Copy(f.collects, w.collects)
	}
	return err
}

type facetTx struct {
	store.Tx
	f, w *facets
}
type facetSem struct {
	store.SemanticTx
	x facetTx
}
type facetRead struct {
	store.SemanticReader
	x facetTx
}

func (t facetTx) SemanticTransaction() (store.SemanticTx, error) {
	sem, err := store.Semantic(t.Tx)
	return facetSem{SemanticTx: sem, x: t}, err
}
func (t facetTx) SemanticReadBackend() store.SemanticReader {
	r, err := store.ReadSemantic(t.Tx)
	if err != nil {
		return nil
	}
	return facetRead{SemanticReader: r, x: t}
}

// Forward the faked families; every other method reaches the real backend.
func (s facetSem) OpenGoalsByTaskOwner(task string, p store.Page) (store.ResultPage[domain.ContextItem], error) {
	return s.x.OpenGoalsByTaskOwner(task, p)
}
func (s facetSem) GCRequest(id string) (domain.GCRequest, error) { return s.x.GCRequest(id) }
func (s facetSem) GCResult(id string) (domain.GCResult, error)   { return s.x.GCResult(id) }
func (s facetSem) CollectReceipt(id string) (domain.CollectReceipt, error) {
	return s.x.CollectReceipt(id)
}
func (s facetSem) PendingGCRequests(p store.Page) (store.ResultPage[domain.GCRequest], error) {
	return s.x.PendingGCRequests(p)
}
func (s facetSem) GCCandidates(f store.GCCandidateFilter) (store.ResultPage[domain.ContextItem], error) {
	return s.x.GCCandidates(f)
}
func (s facetSem) LeasesBySource(src domain.ItemContentRef, p store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	return s.x.LeasesBySource(src, p)
}
func (s facetSem) InsertGCRequest(r domain.GCRequest) error { return s.x.InsertGCRequest(r) }
func (s facetSem) InsertGCResult(r domain.GCResult) error   { return s.x.InsertGCResult(r) }
func (s facetSem) InsertCollectReceipt(r domain.CollectReceipt) error {
	return s.x.InsertCollectReceipt(r)
}
func (r facetRead) OpenGoalsByTaskOwner(task string, p store.Page) (store.ResultPage[domain.ContextItem], error) {
	return r.x.OpenGoalsByTaskOwner(task, p)
}

func (r facetRead) GCRequest(id string) (domain.GCRequest, error) { return r.x.GCRequest(id) }
func (r facetRead) GCResult(id string) (domain.GCResult, error)   { return r.x.GCResult(id) }
func (r facetRead) CollectReceipt(id string) (domain.CollectReceipt, error) {
	return r.x.CollectReceipt(id)
}
func (r facetRead) PendingGCRequests(p store.Page) (store.ResultPage[domain.GCRequest], error) {
	return r.x.PendingGCRequests(p)
}
func (r facetRead) GCCandidates(f store.GCCandidateFilter) (store.ResultPage[domain.ContextItem], error) {
	return r.x.GCCandidates(f)
}
func (r facetRead) LeasesBySource(src domain.ItemContentRef, p store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	return r.x.LeasesBySource(src, p)
}

func page[T any](all []T, key func(T) store.Cursor, p store.Page) store.ResultPage[T] {
	var out store.ResultPage[T]
	for _, v := range all {
		k := key(v)
		if k.Seq < p.After.Seq || k.Seq == p.After.Seq && k.ID <= p.After.ID {
			continue
		}
		if len(out.Records) == p.Limit {
			out.More, out.Next = true, key(out.Records[len(out.Records)-1])
			return out
		}
		out.Records = append(out.Records, v)
	}
	return out
}

func (t facetTx) tracked(keep func(domain.ContextItem) bool) ([]domain.ContextItem, error) {
	var out []domain.ContextItem
	for _, id := range t.f.items {
		it, err := t.Tx.Item(id)
		if err != nil {
			return nil, err
		}
		if keep(it) {
			out = append(out, it)
		}
	}
	slices.SortFunc(out, func(a, b domain.ContextItem) int {
		if a.Seq != b.Seq {
			return int(a.Seq) - int(b.Seq)
		}
		return compare(a.ID, b.ID)
	})
	return out, nil
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func itemKey(it domain.ContextItem) store.Cursor { return store.Cursor{Seq: it.Seq, ID: it.ID} }

func (t facetTx) OpenGoalsByTaskOwner(task string, p store.Page) (store.ResultPage[domain.ContextItem], error) {
	all, err := t.tracked(func(it domain.ContextItem) bool {
		return it.Kind == domain.KindGoal && it.GoalStatus != nil && *it.GoalStatus == domain.GoalOpen && it.TaskID == task && (it.Scope == domain.ScopeTask || it.Scope == domain.ScopeTurn)
	})
	return page(all, itemKey, p), err
}

func (t facetTx) GCCandidates(f store.GCCandidateFilter) (store.ResultPage[domain.ContextItem], error) {
	if err := f.Validate(); err != nil {
		return store.ResultPage[domain.ContextItem]{}, err
	}
	all, err := t.tracked(func(it domain.ContextItem) bool {
		return it.Access.Permits(f.Viewer) && it.Residency == domain.ResidencyResident && it.Seq <= f.SnapshotSeq && (f.Scope == domain.CollectSession || it.TaskID == f.TaskID)
	})
	return page(all, itemKey, f.Page), err
}

func (t facetTx) LeasesBySource(src domain.ItemContentRef, p store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	return page(t.f.leases[src.ItemID], func(l domain.RetrievalLease) store.Cursor { return store.Cursor{Seq: l.Seq, ID: l.ID} }, p), nil
}

func (t facetTx) GCRequest(id string) (domain.GCRequest, error) {
	if r, ok := t.w.requests[id]; ok {
		return r, nil
	}
	if r, ok := t.f.requests[id]; ok {
		return r, nil
	}
	return domain.GCRequest{}, domain.ErrNotFound
}
func (t facetTx) GCResult(requestID string) (domain.GCResult, error) {
	if r, ok := t.w.results[requestID]; ok {
		return r, nil
	}
	if r, ok := t.f.results[requestID]; ok {
		return r, nil
	}
	return domain.GCResult{}, domain.ErrNotFound
}
func (t facetTx) CollectReceipt(id string) (domain.CollectReceipt, error) {
	if r, ok := t.w.collects[id]; ok {
		return r.Clone(), nil
	}
	if r, ok := t.f.collects[id]; ok {
		return r.Clone(), nil
	}
	return domain.CollectReceipt{}, domain.ErrNotFound
}

// PendingGCRequests lists requests without a committed result in (Seq, ID) order.
func (t facetTx) PendingGCRequests(p store.Page) (store.ResultPage[domain.GCRequest], error) {
	var all []domain.GCRequest
	for _, m := range []map[string]domain.GCRequest{t.f.requests, t.w.requests} {
		for id, r := range m {
			if _, err := t.GCResult(id); err != nil {
				all = append(all, r)
			}
		}
	}
	slices.SortFunc(all, func(a, b domain.GCRequest) int {
		if a.Seq != b.Seq {
			return int(a.Seq) - int(b.Seq)
		}
		return compare(a.ID, b.ID)
	})
	return page(all, func(r domain.GCRequest) store.Cursor { return store.Cursor{Seq: r.Seq, ID: r.ID} }, p), nil
}

func (t facetTx) sequenced(meta domain.SemanticMeta, validate func() error) error {
	if meta.SessionID != t.Tx.SessionID() || !t.Tx.Allocated(meta.Seq) {
		return domain.ErrInvalidRecord
	}
	return validate()
}

func (t facetTx) InsertGCRequest(r domain.GCRequest) error {
	if err := t.sequenced(r.SemanticMeta, r.Validate); err != nil {
		return err
	}
	if _, err := t.GCRequest(r.ID); err == nil {
		return domain.ErrImmutable
	}
	t.w.requests[r.ID] = r
	return nil
}
func (t facetTx) InsertGCResult(r domain.GCResult) error {
	if err := t.sequenced(r.SemanticMeta, r.Validate); err != nil {
		return err
	}
	if _, err := t.GCRequest(r.GCRequestID); err != nil {
		return domain.ErrInvalidRecord
	}
	if _, err := t.CollectReceipt(r.CollectReceiptID); err != nil {
		return domain.ErrInvalidRecord
	}
	if _, err := t.GCResult(r.GCRequestID); err == nil {
		return domain.ErrImmutable
	}
	t.w.results[r.GCRequestID] = r
	return nil
}
func (t facetTx) InsertCollectReceipt(r domain.CollectReceipt) error {
	if err := t.sequenced(r.SemanticMeta, r.Validate); err != nil {
		return err
	}
	if _, err := t.CollectReceipt(r.ID); err == nil {
		return domain.ErrImmutable
	}
	t.w.collects[r.ID] = r.Clone()
	return nil
}
