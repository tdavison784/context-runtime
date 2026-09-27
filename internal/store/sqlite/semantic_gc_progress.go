package sqlite

import (
	"errors"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// GC progress (H3, migration 0040): one CAS-written cursor per pending GC
// request. It is operational metadata, not a sequenced semantic record, so
// a transaction that only records an attempt commits.

func (s semRead) GCProgress(gcRequestID string) (domain.GCProgress, error) {
	var p domain.GCProgress
	return p, s.t.get("gc_progress", gcRequestID, 0, &p)
}

func (s semTx) PutGCProgress(p domain.GCProgress, expectedRevision uint64) (domain.GCProgress, error) {
	t := s.t
	if err := t.checkSession(p.SessionID); err != nil {
		return domain.GCProgress{}, err
	}
	p.Revision = expectedRevision + 1
	if err := p.Validate(); err != nil {
		return domain.GCProgress{}, err
	}
	if ok, err := t.exists("gc_request", p.GCRequestID); err != nil || !ok {
		return domain.GCProgress{}, errors.Join(err, invalidIf(!ok, "GC progress: request %s is not stored", p.GCRequestID))
	}
	if ok, err := t.exists("gc_result", p.GCRequestID); err != nil {
		return domain.GCProgress{}, err
	} else if ok {
		return domain.GCProgress{}, transition("GC progress: request %s is finished", p.GCRequestID)
	}
	var cur domain.GCProgress
	err := t.get("gc_progress", p.GCRequestID, 0, &cur)
	found := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.GCProgress{}, err
	}
	if cur.Revision != expectedRevision {
		return domain.GCProgress{}, fmt.Errorf("GC progress %s: revision %d, expected %d: %w", p.GCRequestID, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	return p, t.put("gc_progress", p.GCRequestID, 0, p, found)
}
