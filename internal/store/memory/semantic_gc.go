package memory

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Durable GC requests, collect receipts and results (P3-38/39), the
// collector's candidate read, and completion's open-goal read (P3-9).

type gcState struct {
	requests    map[string]domain.GCRequest
	reqIDs      map[string]string // request identity -> GC request ID
	pending     map[string][]seqRef
	receipts    map[string]domain.CollectReceipt
	results     map[string]domain.GCResult // by GC request ID
	resultIDs   map[string]bool
	resident    map[string][]seqRef // "" -> all resident items; task -> its resident items
	openGoals   map[string][]seqRef // task -> TURN/TASK-owned OPEN goals
	residentAll map[string][]seqRef
	progress    map[string]domain.GCProgress    // by GC request ID (H3)
	byTrigger   map[domain.GCTrigger][]seqRef   // pending requests per trigger (DUR-3.2)
	queue       map[string]domain.GCQueueCursor // the session queue cursor under "" (DUR-3.2)
}

func newGCState() gcState {
	return gcState{requests: map[string]domain.GCRequest{}, reqIDs: map[string]string{}, pending: map[string][]seqRef{},
		receipts: map[string]domain.CollectReceipt{}, results: map[string]domain.GCResult{}, resultIDs: map[string]bool{},
		resident: map[string][]seqRef{}, openGoals: map[string][]seqRef{}, residentAll: map[string][]seqRef{}, progress: map[string]domain.GCProgress{},
		byTrigger: map[domain.GCTrigger][]seqRef{}, queue: map[string]domain.GCQueueCursor{}}
}

type gcView struct {
	requests    table[string, domain.GCRequest]
	reqIDs      table[string, string]
	pending     orderedIndex[string]
	receipts    table[string, domain.CollectReceipt]
	results     table[string, domain.GCResult]
	resultIDs   table[string, bool]
	resident    orderedIndex[string]
	openGoals   orderedIndex[string]
	residentAll orderedIndex[string]
	progress    table[string, domain.GCProgress]
	byTrigger   orderedIndex[domain.GCTrigger]
	queue       table[string, domain.GCQueueCursor]
}

func newGCView(st *gcState, w bool) gcView {
	return gcView{requests: newTable(st.requests, w, domain.GCRequest.Clone), reqIDs: newTable(st.reqIDs, w, same[string]),
		pending: newOrderedIndex(st.pending, w), receipts: newTable(st.receipts, w, domain.CollectReceipt.Clone),
		results: newTable(st.results, w, domain.GCResult.Clone), resultIDs: newTable(st.resultIDs, w, same[bool]),
		resident: newOrderedIndex(st.resident, w), openGoals: newOrderedIndex(st.openGoals, w), residentAll: newOrderedIndex(st.residentAll, w),
		progress: newTable(st.progress, w, domain.GCProgress.Clone), byTrigger: newOrderedIndex(st.byTrigger, w),
		queue: newTable(st.queue, w, domain.GCQueueCursor.Clone)}
}

func (v *gcView) dirty() bool {
	return v.requests.dirty() || v.receipts.dirty() || v.results.dirty() || v.progress.dirty() || v.queue.dirty()
}

func (v *gcView) commit() {
	v.requests.commit()
	v.reqIDs.commit()
	v.pending.commit()
	v.receipts.commit()
	v.results.commit()
	v.resultIDs.commit()
	v.resident.commit()
	v.openGoals.commit()
	v.residentAll.commit()
	v.progress.commit()
	v.byTrigger.commit()
	v.queue.commit()
}

// openTaskGoal reports whether it is an OPEN goal whose declared owning
// scope is TURN or TASK of a task.
func openTaskGoal(it domain.ContextItem) bool {
	return it.Kind == domain.KindGoal && it.GoalStatus != nil && *it.GoalStatus == domain.GoalOpen && it.TaskID != "" &&
		(it.Scope == domain.ScopeTask || it.Scope == domain.ScopeTurn)
}

// noteItem keeps the resident and open-goal indexes in step with an item
// write; before is the zero item on insert.
func (t *tx) noteItem(before, after domain.ContextItem) {
	ref := seqRef{after.Seq, after.ID}
	wasResident, isResident := before.ID != "" && before.Residency == domain.ResidencyResident, after.Residency == domain.ResidencyResident
	switch {
	case wasResident && !isResident:
		t.sem.gc.residentAll.remove("", ref)
		t.sem.gc.resident.remove(after.TaskID, ref)
	case !wasResident && isResident:
		t.sem.gc.residentAll.add("", ref)
		t.sem.gc.resident.add(after.TaskID, ref)
	}
	wasOpen, isOpen := before.ID != "" && openTaskGoal(before), openTaskGoal(after)
	switch {
	case wasOpen && !isOpen:
		t.sem.gc.openGoals.remove(after.TaskID, ref)
	case !wasOpen && isOpen:
		t.sem.gc.openGoals.add(after.TaskID, ref)
	}
}

// retireOpenGoal drops a superseded or duplicate item from the open-goal
// index: only current goals are requirements.
func (t *tx) retireOpenGoal(itemID string) {
	if it, ok := t.items.peek(itemID); ok && openTaskGoal(it) {
		t.sem.gc.openGoals.remove(it.TaskID, seqRef{it.Seq, it.ID})
	}
}

// OpenGoalsByTaskOwner pages every current OPEN goal whose declared owning
// scope is TURN or TASK of taskID, with no access filter: completion must
// reject hidden requirements (it reports them only with fixed errors).
func (r semRead) OpenGoalsByTaskOwner(taskID string, p store.Page) (store.ResultPage[domain.ContextItem], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ContextItem]{}, err
	}
	return page(p, r.r.sem.gc.openGoals.after(taskID, cursorRef(p.After)), func(id string) (domain.ContextItem, bool) {
		it, ok := r.r.items.get(id)
		return it, ok && openTaskGoal(it)
	})
}

// GCCandidates pages the RESIDENT items of the filter's scope at or before
// its snapshot sequence that the viewer may access, in (Seq, ID) order,
// filtered before the limit. Eligibility, protection and Archive authority
// are the collector's decisions.
func (r semRead) GCCandidates(f store.GCCandidateFilter) (store.ResultPage[domain.ContextItem], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ContextItem]{}, err
	}
	if err := f.Validate(); err != nil {
		return store.ResultPage[domain.ContextItem]{}, err
	}
	refs := r.r.sem.gc.residentAll.after("", cursorRef(f.Page.After))
	if f.Scope == domain.CollectTask {
		refs = r.r.sem.gc.resident.after(f.TaskID, cursorRef(f.Page.After))
	}
	bounded := func(yield func(seqRef) bool) {
		for ref := range refs {
			if ref.seq > f.SnapshotSeq || !yield(ref) {
				return
			}
		}
	}
	return page(f.Page, bounded, func(id string) (domain.ContextItem, bool) {
		it, ok := r.r.items.get(id)
		return it, ok && it.Residency == domain.ResidencyResident && it.Access.Permits(f.Viewer)
	})
}

// InsertGCRequest stores a durable request, pending until its result. Its
// ID and request identity are unique, and a task scope names a stored task.
func (t *semTx) InsertGCRequest(g domain.GCRequest) error {
	if err := t.t.companion("GC request", g.SemanticMeta, g.Validate); err != nil {
		return err
	}
	if t.r.sem.gc.requests.has(g.ID) || t.r.sem.gc.reqIDs.has(g.RequestID) {
		return immutable("GC request", g.ID)
	}
	if g.Scope == domain.CollectTask && !t.r.tasks.has(g.TaskID) {
		return invalid("GC request %s: task %s is not stored", g.ID, g.TaskID)
	}
	t.r.sem.gc.requests.put(g.ID, g)
	t.r.sem.gc.reqIDs.put(g.RequestID, g.ID)
	t.r.sem.gc.pending.add("", seqRef{g.Seq, g.ID})
	t.r.sem.gc.byTrigger.add(g.Trigger, seqRef{g.Seq, g.ID})
	t.t.sequencedWrite(g.Seq)
	return nil
}

// InsertCollectReceipt stores a collection's frozen result: its candidates
// are stored items, and a named GC request is stored with the same
// request identity.
func (t *semTx) InsertCollectReceipt(c domain.CollectReceipt) error {
	if err := t.t.companion("collect receipt", c.SemanticMeta, c.Validate); err != nil {
		return err
	}
	if t.r.sem.gc.receipts.has(c.ID) {
		return immutable("collect receipt", c.ID)
	}
	if c.GCRequestID != "" {
		g, ok := t.r.sem.gc.requests.peek(c.GCRequestID)
		if !ok || !store.CollectReceiptOf(g, c.RequestID) {
			return invalid("collect receipt %s: GC request %s is not stored with its request identity", c.ID, c.GCRequestID)
		}
	}
	for _, ref := range c.CandidateRefs {
		if !t.r.items.has(ref.ItemID) {
			return invalid("collect receipt %s: candidate %s is not stored", c.ID, ref.ItemID)
		}
	}
	t.r.sem.gc.receipts.put(c.ID, c)
	t.t.sequencedWrite(c.Seq)
	return nil
}

// InsertGCResult completes a request: at most one result per request, and
// its collect receipt names the request.
func (t *semTx) InsertGCResult(g domain.GCResult) error {
	if err := t.t.companion("GC result", g.SemanticMeta, g.Validate); err != nil {
		return err
	}
	if t.r.sem.gc.results.has(g.GCRequestID) || t.r.sem.gc.resultIDs.has(g.ID) {
		return immutable("GC result of request", g.GCRequestID)
	}
	req, ok := t.r.sem.gc.requests.peek(g.GCRequestID)
	if !ok {
		return invalid("GC result %s: request %s is not stored", g.ID, g.GCRequestID)
	}
	if g.Outcome == domain.GCCollected {
		if c, ok := t.r.sem.gc.receipts.peek(g.CollectReceiptID); !ok || c.GCRequestID != g.GCRequestID {
			return invalid("GC result %s: receipt %s is not its request's collect receipt", g.ID, g.CollectReceiptID)
		}
	}
	t.r.sem.gc.results.put(g.GCRequestID, g)
	t.r.sem.gc.resultIDs.put(g.ID, true)
	t.r.sem.gc.pending.remove("", seqRef{req.Seq, req.ID})
	t.r.sem.gc.byTrigger.remove(req.Trigger, seqRef{req.Seq, req.ID})
	t.t.sequencedWrite(g.Seq)
	return nil
}

func (r semRead) GCRequest(id string) (domain.GCRequest, error) {
	if err := r.r.check(); err != nil {
		return domain.GCRequest{}, err
	}
	g, ok := r.r.sem.gc.requests.get(id)
	if !ok {
		return g, notFound("GC request", id)
	}
	return g, nil
}

func (r semRead) GCResult(requestID string) (domain.GCResult, error) {
	if err := r.r.check(); err != nil {
		return domain.GCResult{}, err
	}
	g, ok := r.r.sem.gc.results.get(requestID)
	if !ok {
		return g, notFound("GC result", requestID)
	}
	return g, nil
}

func (r semRead) CollectReceipt(id string) (domain.CollectReceipt, error) {
	if err := r.r.check(); err != nil {
		return domain.CollectReceipt{}, err
	}
	c, ok := r.r.sem.gc.receipts.get(id)
	if !ok {
		return c, notFound("collect receipt", id)
	}
	return c, nil
}

// PendingGCRequests pages the requests without a result, oldest first.
func (r semRead) PendingGCRequests(p store.Page) (store.ResultPage[domain.GCRequest], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.GCRequest]{}, err
	}
	return page(p, r.r.sem.gc.pending.after("", cursorRef(p.After)), loadAll(&r.r.sem.gc.requests, ident))
}
