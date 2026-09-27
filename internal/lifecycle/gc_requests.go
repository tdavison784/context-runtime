package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ErrGCTriggerDisabled rejects collection for a trigger outside the policy's
// explicit enabled set. The durable request, if any, stays pending.
var ErrGCTriggerDisabled = fmt.Errorf("lifecycle: GC trigger disabled by policy: %w", domain.ErrInvalidTransition)

// EnqueueGC persists a durable GC request in the producer's transaction
// (P3-39). The request ID derives from the trigger identity, so a duplicate
// trigger is one request; the same identity with different content conflicts.
// Manual collection calls Collect directly and is never enqueued. The origin is
// recorded as context only: it never becomes the collecting principal. A
// supersession/TTL/policy trigger outside the enabled set persists nothing and
// returns an empty ID; task completion always persists its request (P3-39),
// which then waits, pending, until its trigger is enabled.
func (s *Service) EnqueueGC(tx store.Tx, origin domain.Principal, trigger domain.GCTrigger, scope domain.CollectScope, taskID, triggerID string) (id string, err error) {
	// Like every lifecycle entry point, a failure poisons the producer's
	// transaction, so its write never commits without the durable trigger
	// (DUR-1.11, P3-1/39). A disabled trigger's "", nil is not a failure.
	defer func() {
		if err != nil {
			tx.Poison(err)
			id = ""
		}
	}()
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
	if !trigger.Valid() {
		return "", domain.ErrInvalidRecord
	}
	if err := origin.Validate(); err != nil {
		return "", err
	}
	if origin.SessionID != tx.SessionID() {
		return "", domain.ErrNotFound
	}
	if trigger != domain.GCTaskCompletion && !s.policy.GCTriggerEnabled(trigger) {
		return "", nil
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
// committed. seq 0 allocates only after the replay check. The collector is
// an authenticated SYSTEM/HARNESS principal
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
	if !s.policy.GCTriggerEnabled(req.Trigger) {
		return out, ErrGCTriggerDisabled
	}
	return s.collect(tx, collector, req.CollectIntent, req.ID, seq)
}

// CollectPending executes up to max pending requests, each in its own
// transaction, paging through the whole queue (G2). collectorFor supplies
// the authenticated collector for a request, or false to leave it pending;
// disabled triggers and requests already collected by another worker stay
// uncounted. A failing request never blocks later ones: its error is joined
// into the result and the request stays pending for a later attempt.
func (s *Service) CollectPending(ctx context.Context, session string, collectorFor func(domain.GCRequest) (domain.Principal, bool), max int) (int, error) {
	if collectorFor == nil || max <= 0 {
		return 0, domain.ErrInvalidRecord
	}
	done := 0
	var failures []error
	var after store.Cursor
	for done < max {
		var page store.ResultPage[domain.GCRequest]
		if err := s.store.View(ctx, session, func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			page, err = sem.PendingGCRequests(store.Page{After: after, Limit: s.policy.MaxPageSize})
			return err
		}); err != nil {
			return done, errors.Join(append(failures, err)...)
		}
		for _, r := range page.Records {
			after = store.Cursor{Seq: r.Seq, ID: r.ID}
			if done == max {
				break
			}
			p, ok := collectorFor(r)
			if !ok || !s.policy.GCTriggerEnabled(r.Trigger) {
				continue
			}
			executed, err := s.collectPendingOne(ctx, session, p, r.ID)
			if err != nil {
				failures = append(failures, fmt.Errorf("GC request %s: %w", r.ID, err))
				continue
			}
			if executed {
				done++
			}
		}
		if !page.More {
			break
		}
		if len(page.Records) == 0 {
			return done, errors.Join(append(failures, domain.ErrIntegrity)...)
		}
	}
	return done, errors.Join(failures...)
}

// collectPendingOne runs one request in its own transaction. Under the
// session writer, a request another worker already collected is skipped: no
// sequence, no count (DUR-1.3).
func (s *Service) collectPendingOne(ctx context.Context, session string, p domain.Principal, id string) (bool, error) {
	executed := false
	err := s.store.Update(ctx, session, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		if _, err := sem.GCResult(id); err == nil {
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if _, err := s.ExecuteGCRequest(tx, p, id, 0); err != nil {
			return err
		}
		executed = true
		return nil
	})
	return executed && err == nil, err
}
