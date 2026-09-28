package memory

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Retrieval leases, results, projections, and events (P3-28..30). A lease
// names a stored item's exact content. A result, its projection and its
// successful event name each other and are inserted as one bundle, so
// their links are checked at commit: the event must be the result's origin
// event (domain.RetrievalResult.ValidateOriginEvent) and the projection
// must carry the result's exact origin. Results and projections are never
// broader than their source. Retrieval never changes the source item.

type holderKey struct {
	holder     domain.Principal
	conv, turn string
}

type retState struct {
	leases     map[string]domain.RetrievalLease
	byHolder   map[holderKey][]seqRef
	bySource   map[domain.ItemContentRef][]seqRef
	results    map[string]domain.RetrievalResult
	projs      map[string]domain.ProjectionRecord
	projByItem map[string]string
	events     map[string]domain.RetrievalEvent
	evByReq    map[string][]seqRef
}

func newRetState() retState {
	return retState{leases: map[string]domain.RetrievalLease{}, byHolder: map[holderKey][]seqRef{}, bySource: map[domain.ItemContentRef][]seqRef{},
		results: map[string]domain.RetrievalResult{}, projs: map[string]domain.ProjectionRecord{}, projByItem: map[string]string{},
		events: map[string]domain.RetrievalEvent{}, evByReq: map[string][]seqRef{}}
}

type retView struct {
	leases     table[string, domain.RetrievalLease]
	byHolder   orderedIndex[holderKey]
	bySource   orderedIndex[domain.ItemContentRef]
	results    table[string, domain.RetrievalResult]
	projs      table[string, domain.ProjectionRecord]
	projByItem table[string, string]
	events     table[string, domain.RetrievalEvent]
	evByReq    orderedIndex[string]
}

func newRetView(st *retState, w bool) retView {
	return retView{leases: newTable(st.leases, w, domain.RetrievalLease.Clone), byHolder: newOrderedIndex(st.byHolder, w),
		bySource: newOrderedIndex(st.bySource, w), results: newTable(st.results, w, domain.RetrievalResult.Clone),
		projs: newTable(st.projs, w, domain.ProjectionRecord.Clone), projByItem: newTable(st.projByItem, w, same[string]),
		events: newTable(st.events, w, domain.RetrievalEvent.Clone), evByReq: newOrderedIndex(st.evByReq, w)}
}

func (v *retView) dirty() bool {
	return v.leases.dirty() || v.results.dirty() || v.projs.dirty() || v.events.dirty()
}

func (v *retView) commit() {
	v.leases.commit()
	v.byHolder.commit()
	v.bySource.commit()
	v.results.commit()
	v.projs.commit()
	v.projByItem.commit()
	v.events.commit()
	v.evByReq.commit()
}

// sameOrigin compares retrieval origins by value, including the invocation.
func sameOrigin(a, b domain.RetrievalOrigin) bool {
	return a.Holder == b.Holder && a.ConversationID == b.ConversationID && a.TurnID == b.TurnID && samePtr(a.Invocation, b.Invocation)
}

// sourceWithin requires ref to name a stored item with that exact content
// whose boundary contains access.
func (t *semTx) sourceWithin(ref domain.ItemContentRef, access domain.AccessBoundary) bool {
	it, ok := t.r.items.peek(ref.ItemID)
	return ok && it.ContentHash == ref.ContentHash && access.Within(it.Access)
}

func (t *semTx) InsertRetrievalLease(l domain.RetrievalLease) error {
	if err := t.t.companion("retrieval lease", l.SemanticMeta, l.Validate); err != nil {
		return err
	}
	if t.r.sem.ret.leases.has(l.ID) {
		return immutable("retrieval lease", l.ID)
	}
	if err := t.checkContent(l.Source); err != nil {
		return invalid("retrieval lease %s: %v", l.ID, err)
	}
	ref := seqRef{l.Seq, l.ID}
	t.r.sem.ret.leases.put(l.ID, l)
	t.r.sem.ret.byHolder.add(holderKey{l.Holder, l.ConversationID, l.TurnID}, ref)
	t.r.sem.ret.bySource.add(l.Source, ref)
	t.t.sequencedWrite(l.Seq)
	return nil
}

func (t *semTx) InsertProjection(p domain.ProjectionRecord) error {
	if err := t.t.companion("projection", p.SemanticMeta, p.Validate); err != nil {
		return err
	}
	if t.r.sem.ret.projs.has(p.ID) || t.r.sem.ret.projByItem.has(p.ItemID) {
		return immutable("projection", p.ID)
	}
	it, ok := t.r.items.peek(p.ItemID)
	if !ok || it.Role != domain.RoleProjection || it.Authority != domain.AuthorityTool {
		return invalid("projection %s: item %s is not a stored TOOL projection", p.ID, p.ItemID)
	}
	// Reads filter on the item's access: it must be the record's
	// intersected boundary, never broader (SEC-1.12).
	if it.Access != p.Access {
		return invalid("projection %s: item %s access is not the projection's boundary", p.ID, p.ItemID)
	}
	if !t.sourceWithin(p.Source, p.Access) {
		return invalid("projection %s: source is not stored content containing its boundary", p.ID)
	}
	if l, ok := t.r.sem.ret.leases.peek(p.LeaseID); !ok || l.Source != p.Source {
		return invalid("projection %s: lease %s is not a stored lease of its source", p.ID, p.LeaseID)
	}
	if c, ok := t.r.sem.coverages.peek(p.DependencyCoverageID); !ok || c.Purpose != domain.CoverageLeaseDependency {
		return invalid("projection %s: dependency coverage is not stored lease-dependency coverage", p.ID)
	}
	t.r.sem.ret.projs.put(p.ID, p)
	t.r.sem.ret.projByItem.put(p.ItemID, p.ID)
	t.t.sequencedWrite(p.Seq)
	t.t.deferCheck(func() error {
		r, ok := t.r.sem.ret.results.peek(p.RetrievalResultID)
		if !ok || r.ProjectionID != p.ID || r.LeaseID != p.LeaseID || r.Observed.Source != p.Source || !sameOrigin(r.Origin, p.Origin) {
			return invalid("projection %s: result %s is not its stored result with the same origin", p.ID, p.RetrievalResultID)
		}
		return nil
	})
	return nil
}

func (t *semTx) InsertRetrievalResult(r domain.RetrievalResult) error {
	if err := t.t.companion("retrieval result", r.SemanticMeta, r.Validate); err != nil {
		return err
	}
	if t.r.sem.ret.results.has(r.ID) {
		return immutable("retrieval result", r.ID)
	}
	l, ok := t.r.sem.ret.leases.peek(r.LeaseID)
	if !ok || l.Source != r.Observed.Source || l.Holder != r.Origin.Holder || l.ConversationID != r.Origin.ConversationID || l.TurnID != r.Origin.TurnID {
		return invalid("retrieval result %s: lease %s is not its holder's lease of its source", r.ID, r.LeaseID)
	}
	if !t.sourceWithin(r.Observed.Source, r.Access) {
		return invalid("retrieval result %s: source is not stored content containing its boundary", r.ID)
	}
	t.r.sem.ret.results.put(r.ID, r)
	t.t.sequencedWrite(r.Seq)
	t.t.deferCheck(func() error {
		e, ok := t.r.sem.ret.events.peek(r.RetrievalEventID)
		if !ok {
			return invalid("retrieval result %s: event %s is not stored", r.ID, r.RetrievalEventID)
		}
		if err := r.ValidateOriginEvent(e); err != nil {
			return err
		}
		p, ok := t.r.sem.ret.projs.peek(r.ProjectionID)
		if !ok || p.RetrievalResultID != r.ID {
			return invalid("retrieval result %s: projection %s is not stored for it", r.ID, r.ProjectionID)
		}
		return nil
	})
	return nil
}

// InsertRetrievalEvent audits one attempt. A successful event names stored
// source content and its result, which must name it back by commit; a
// denial carries neither.
func (t *semTx) InsertRetrievalEvent(e domain.RetrievalEvent) error {
	if err := t.t.companion("retrieval event", e.SemanticMeta, e.Validate); err != nil {
		return err
	}
	if t.r.sem.ret.events.has(e.ID) {
		return immutable("retrieval event", e.ID)
	}
	if e.Source != nil {
		if err := t.checkContent(*e.Source); err != nil {
			return invalid("retrieval event %s: %v", e.ID, err)
		}
	}
	t.r.sem.ret.events.put(e.ID, e)
	t.r.sem.ret.evByReq.add(e.RequestID, seqRef{e.Seq, e.ID})
	t.t.sequencedWrite(e.Seq)
	if e.ResultID != "" {
		t.t.deferCheck(func() error {
			r, ok := t.r.sem.ret.results.peek(e.ResultID)
			if !ok || r.RetrievalEventID != e.ID {
				return invalid("retrieval event %s: result %s is not stored for it", e.ID, e.ResultID)
			}
			return nil
		})
	}
	return nil
}

func (r semRead) RetrievalLease(id string) (domain.RetrievalLease, error) {
	if err := r.r.check(); err != nil {
		return domain.RetrievalLease{}, err
	}
	l, ok := r.r.sem.ret.leases.get(id)
	if !ok {
		return l, notFound("retrieval lease", id)
	}
	return l, nil
}

func (r semRead) RetrievalResult(id string) (domain.RetrievalResult, error) {
	if err := r.r.check(); err != nil {
		return domain.RetrievalResult{}, err
	}
	v, ok := r.r.sem.ret.results.get(id)
	if !ok {
		return v, notFound("retrieval result", id)
	}
	return v, nil
}

func (r semRead) RetrievalEvent(id string) (domain.RetrievalEvent, error) {
	if err := r.r.check(); err != nil {
		return domain.RetrievalEvent{}, err
	}
	e, ok := r.r.sem.ret.events.get(id)
	if !ok {
		return e, notFound("retrieval event", id)
	}
	return e, nil
}

func (r semRead) Projection(id string) (domain.ProjectionRecord, error) {
	if err := r.r.check(); err != nil {
		return domain.ProjectionRecord{}, err
	}
	p, ok := r.r.sem.ret.projs.get(id)
	if !ok {
		return p, notFound("projection", id)
	}
	return p, nil
}

func (r semRead) ProjectionByItem(itemID string) (domain.ProjectionRecord, error) {
	if err := r.r.check(); err != nil {
		return domain.ProjectionRecord{}, err
	}
	id, ok := r.r.sem.ret.projByItem.peek(itemID)
	if !ok {
		return domain.ProjectionRecord{}, notFound("projection of item", itemID)
	}
	p, _ := r.r.sem.ret.projs.get(id)
	return p, nil
}

func (r semRead) LeasesByHolder(holder domain.Principal, conversationID, turnID string, p store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.RetrievalLease]{}, err
	}
	return page(p, r.r.sem.ret.byHolder.after(holderKey{holder, conversationID, turnID}, cursorRef(p.After)), loadAll(&r.r.sem.ret.leases, ident))
}

// LeasesBySource pages every holder's leases of source (GC protection).
func (r semRead) LeasesBySource(source domain.ItemContentRef, p store.Page) (store.ResultPage[domain.RetrievalLease], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.RetrievalLease]{}, err
	}
	return page(p, r.r.sem.ret.bySource.after(source, cursorRef(p.After)), loadAll(&r.r.sem.ret.leases, ident))
}

// RetrievalEventsByRequest pages a request's audit events visible to
// viewer: those whose principal or triggering actor is exactly viewer,
// filtered before the limit, so another principal learns nothing.
func (r semRead) RetrievalEventsByRequest(viewer domain.Principal, requestID string, p store.Page) (store.ResultPage[domain.RetrievalEvent], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.RetrievalEvent]{}, err
	}
	if err := viewer.Validate(); err != nil {
		return store.ResultPage[domain.RetrievalEvent]{}, err
	}
	return page(p, r.r.sem.ret.evByReq.after(requestID, cursorRef(p.After)), func(id string) (domain.RetrievalEvent, bool) {
		e, ok := r.r.sem.ret.events.get(id)
		return e, ok && (e.Principal == viewer || e.TriggeringActor == viewer)
	})
}
