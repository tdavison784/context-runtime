package lifecycle

import (
	"context"
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// EnqueueGC persists a durable GC request in the producer's transaction
// (P3-39). The request ID derives from the trigger identity, so a duplicate
// trigger is one request; the same identity with different content conflicts.
// Manual collection calls Collect directly and is never enqueued. The origin is
// recorded as context only: it never becomes the collecting principal.
func (s *Service) EnqueueGC(tx store.Tx, origin domain.Principal, trigger domain.GCTrigger, scope domain.CollectScope, taskID, triggerID string) (string, error) {
	sem, err := store.Semantic(tx)
	if err != nil {
		return "", err
	}
	return s.enqueueGC(tx, sem, origin, trigger, scope, taskID, triggerID)
}

func (s *Service) enqueueGC(tx store.Tx, sem store.SemanticTx, origin domain.Principal, trigger domain.GCTrigger, scope domain.CollectScope, taskID, triggerID string) (string, error) {
	if trigger == domain.GCManual || triggerID == "" {
		return "", domain.ErrInvalidRecord
	}
	if err := origin.Validate(); err != nil {
		return "", err
	}
	if origin.SessionID != tx.SessionID() {
		return "", domain.ErrNotFound
	}
	requestID := "gc_" + domain.NewCanonicalEncoder("context-runtime/gc-trigger/v1").String(origin.SessionID).String(string(trigger)).String(triggerID).Hash()
	r := domain.GCRequest{SemanticMeta: domain.SemanticMeta{ID: gcRequestID(origin.SessionID, requestID), SessionID: origin.SessionID, SchemaVersion: domain.SemanticSchemaV1},
		CollectIntent: domain.CollectIntent{RequestID: requestID, Scope: scope, TaskID: taskID, Trigger: trigger}, Origin: origin, PolicyVersion: s.policy.Version}
	prior, err := sem.GCRequest(r.ID)
	if err == nil {
		r.Seq = prior.Seq
		if prior != r {
			return "", domain.ErrEventIDConflict
		}
		return r.ID, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return "", err
	}
	r.Seq = tx.NextSeq()
	if err := sem.InsertGCRequest(r); err != nil {
		return "", err
	}
	return r.ID, nil
}

// ExecuteGCRequest runs one durable request idempotently after its producer
// committed. The collector is an authenticated SYSTEM/HARNESS principal
// supplied by the embedding; for task-scoped requests it must belong to that
// task. A failure rolls back only this attempt: the request stays pending and
// producer state is untouched. The request→result link commits with the
// collection, so a committed request never executes twice.
func (s *Service) ExecuteGCRequest(tx store.Tx, collector domain.Principal, gcRequestID string, seq uint64) (out MutationOutcome, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = MutationOutcome{}
		}
	}()
	if err = collector.Validate(); err != nil {
		return out, err
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return out, err
	}
	req, err := sem.GCRequest(gcRequestID)
	if errors.Is(err, domain.ErrNotFound) || err == nil && req.SessionID != collector.SessionID {
		return out, domain.ErrNotFound
	}
	if err != nil {
		return out, err
	}
	// A committed collection replays under its recorded principal and policy
	// before any of today's checks (P3-2).
	if _, err = sem.GCResult(req.ID); err == nil {
		return s.collect(tx, collector, req.CollectIntent, req.ID, seq)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return out, err
	}
	if collector.Authority != domain.AuthoritySystem && collector.Authority != domain.AuthorityHarness ||
		req.Scope == domain.CollectTask && collector.TaskID != req.TaskID {
		return out, domain.ErrInvalidAuthorityPromotion
	}
	if req.PolicyVersion != s.policy.Version {
		return out, domain.ErrUnsupportedSchema
	}
	return s.collect(tx, collector, req.CollectIntent, req.ID, seq)
}

// CollectPending executes up to max pending requests, each in its own
// transaction. collectorFor supplies the authenticated collector for a
// request, or false to leave it pending. The first failure stops the batch
// and leaves that request pending.
func (s *Service) CollectPending(ctx context.Context, session string, collectorFor func(domain.GCRequest) (domain.Principal, bool), max int) (int, error) {
	if collectorFor == nil || max <= 0 {
		return 0, domain.ErrInvalidRecord
	}
	var pending []domain.GCRequest
	if err := s.store.View(ctx, session, func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		page, err := sem.PendingGCRequests(store.Page{Limit: min(max, s.policy.MaxPageSize)})
		pending = page.Records
		return err
	}); err != nil {
		return 0, err
	}
	done := 0
	for _, r := range pending {
		p, ok := collectorFor(r)
		if !ok {
			continue
		}
		if err := s.store.Update(ctx, session, func(tx store.Tx) error {
			_, err := s.ExecuteGCRequest(tx, p, r.ID, tx.NextSeq())
			return err
		}); err != nil {
			return done, err
		}
		done++
	}
	return done, nil
}
