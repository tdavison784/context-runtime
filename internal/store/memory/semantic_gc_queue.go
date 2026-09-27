package memory

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// The DUR-3.2 session GC queue cursor and per-trigger pending read. The
// cursor is unsequenced operational state, like GCProgress, so a
// cursor-only transaction commits.

func (r semRead) PendingGCRequestsByTrigger(triggers []domain.GCTrigger, p store.Page) (store.ResultPage[domain.GCRequest], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.GCRequest]{}, err
	}
	if !domain.ValidGCTriggerSet(triggers) {
		return store.ResultPage[domain.GCRequest]{}, invalid("GC trigger set is not canonical")
	}
	return page(p, mergeAfter(&r.r.sem.gc.byTrigger, triggers, cursorRef(p.After)), loadAll(&r.r.sem.gc.requests, ident))
}

func (r semRead) GCQueueCursor() (domain.GCQueueCursor, error) {
	if err := r.r.check(); err != nil {
		return domain.GCQueueCursor{}, err
	}
	c, ok := r.r.sem.gc.queue.get("")
	if !ok {
		return c, notFound("GC queue cursor", r.r.sessionID)
	}
	return c, nil
}

func (t *semTx) PutGCQueueCursor(c domain.GCQueueCursor, expectedRevision uint64) (domain.GCQueueCursor, error) {
	if err := t.t.own(c.SessionID); err != nil {
		return domain.GCQueueCursor{}, err
	}
	c.Revision = expectedRevision + 1
	if err := c.Validate(); err != nil {
		return domain.GCQueueCursor{}, err
	}
	cur, _ := t.r.sem.gc.queue.peek("")
	if cur.Revision != expectedRevision {
		return domain.GCQueueCursor{}, conflict("GC queue cursor: revision %d, expected %d", cur.Revision, expectedRevision)
	}
	t.r.sem.gc.queue.put("", c)
	return c, nil
}
