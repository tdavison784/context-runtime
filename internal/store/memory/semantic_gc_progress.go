package memory

import (
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// GC progress (H3): one CAS-written cursor per pending GC request. It is
// operational metadata, not a sequenced semantic record, so a transaction
// that only records an attempt commits.

func (r semRead) GCProgress(gcRequestID string) (domain.GCProgress, error) {
	if err := r.r.check(); err != nil {
		return domain.GCProgress{}, err
	}
	p, ok := r.r.sem.gc.progress.get(gcRequestID)
	if !ok {
		return p, notFound("GC progress", gcRequestID)
	}
	return p, nil
}

func (t *semTx) PutGCProgress(p domain.GCProgress, expectedRevision uint64) (domain.GCProgress, error) {
	if err := t.t.own(p.SessionID); err != nil {
		return domain.GCProgress{}, err
	}
	p.Revision = expectedRevision + 1
	if err := p.Validate(); err != nil {
		return domain.GCProgress{}, err
	}
	if !t.r.sem.gc.requests.has(p.GCRequestID) {
		return domain.GCProgress{}, invalid("GC progress: request %s is not stored", p.GCRequestID)
	}
	if t.r.sem.gc.results.has(p.GCRequestID) {
		return domain.GCProgress{}, fmt.Errorf("GC progress: request %s is finished: %w", p.GCRequestID, domain.ErrInvalidTransition)
	}
	cur, _ := t.r.sem.gc.progress.peek(p.GCRequestID)
	if cur.Revision != expectedRevision {
		return domain.GCProgress{}, conflict("GC progress %s: revision %d, expected %d", p.GCRequestID, cur.Revision, expectedRevision)
	}
	t.r.sem.gc.progress.put(p.GCRequestID, p)
	return p, nil
}
