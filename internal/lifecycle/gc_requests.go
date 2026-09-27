package lifecycle

import (
	"context"
	"errors"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/gcqueue"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ErrGCTriggerDisabled rejects collection for a trigger outside the policy's
// explicit enabled set. The durable request, if any, stays pending.
var ErrGCConfiguration = errors.New("lifecycle: invalid collector configuration")

var ErrGCTriggerDisabled = fmt.Errorf("lifecycle: GC trigger disabled by policy: %w", domain.ErrInvalidTransition)

// EnqueueGC persists a durable GC request in the producer's transaction
// (P3-39) under this executor's policy; it is a thin wrapper over
// gcqueue.Enqueue, which producers below lifecycle call directly with their
// event's recorded policy (SPEC-2.11). A session-scoped (task-less) trigger
// produces nothing: Phase 3 has no session-scoped GC (H4).
func (s *Service) EnqueueGC(tx store.Tx, origin domain.Principal, trigger domain.GCTrigger, scope domain.CollectScope, taskID, triggerID string) (string, error) {
	switch scope {
	case domain.CollectTask:
		return gcqueue.Enqueue(tx, s.policy, origin, trigger, taskID, triggerID)
	case domain.CollectSession:
		return "", nil // no session-scoped GC in Phase 3 (H4)
	}
	tx.Poison(domain.ErrInvalidRecord)
	return "", domain.ErrInvalidRecord
}

// ErrGCRequestFailed reports a quarantined GC request: it recorded a FAILED
// outcome and is never retried automatically (H3); re-arming needs a new
// request identity.
var ErrGCRequestFailed = fmt.Errorf("lifecycle: GC request quarantined: %w", domain.ErrInvalidTransition)

// maxGCAttempts bounds transient failures of one GC request before it is
// quarantined as ATTEMPTS_EXHAUSTED (H3).
const maxGCAttempts = 3

// maxGCPagesPerCall bounds the pending-queue pages one CollectPending call
// scans, whatever it skips (DUR-2.7).
const maxGCPagesPerCall = 64

// ExecuteGCRequest runs the next bounded batch of one durable request after
// its producer committed (H3). The batch starts at the request's durable
// cursor, commits its own CollectReceipt (request ID GCBatchRequestID(n))
// and either advances the cursor (CAS) or, when no candidates remain,
// records the COLLECTED result. A finished request replays its final batch;
// a quarantined one reports ErrGCRequestFailed. seq 0 allocates only after
// the replay check. The collector is an authenticated SYSTEM/HARNESS
// principal supplied by the embedding; for task-scoped requests it must
// belong to that task. A failure rolls back only this batch: CollectPending
// records the attempt or quarantine in its own transaction.
func (s *Service) ExecuteGCRequest(tx store.Tx, collector domain.Principal, gcRequestID string, seq uint64) (out MutationOutcome, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = MutationOutcome{}
		}
	}()
	if err = collector.Validate(); err != nil {
		return out, errors.Join(ErrGCConfiguration, err)
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
	// A finished request replays under its recorded principal and policy
	// before any of today's checks (P3-2).
	if res, err := sem.GCResult(req.ID); err == nil {
		if res.Outcome == domain.GCFailed {
			return out, ErrGCRequestFailed
		}
		final, err := sem.CollectReceipt(res.CollectReceiptID)
		if err != nil {
			return out, err
		}
		i := req.CollectIntent
		i.RequestID = final.RequestID
		return s.collect(tx, collector, i, &gcBatch{requestID: req.ID}, seq)
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
	progress, err := gcProgress(sem, req.ID)
	if err != nil {
		return out, err
	}
	i := req.CollectIntent
	if i.RequestID, err = domain.GCBatchRequestID(req.RequestID, progress.Batches+1); err != nil {
		return out, err
	}
	return s.collect(tx, collector, i, &gcBatch{requestID: req.ID, progress: progress}, seq)
}

// gcProgress is the request's stored progress, or the zero progress
// (Revision 0) before its first batch.
func gcProgress(sem store.SemanticReader, id string) (domain.GCProgress, error) {
	p, err := sem.GCProgress(id)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.GCProgress{}, nil
	}
	if err != nil {
		return p, err
	}
	// Pre-J2 progress recovers the ceiling from its first committed receipt.
	if p.Batches > 0 && p.SnapshotSeq == 0 {
		req, err := sem.GCRequest(id)
		if err != nil {
			return p, err
		}
		firstID, err := domain.GCBatchRequestID(req.RequestID, 1)
		if err != nil {
			return p, err
		}
		first, err := sem.CollectReceipt(collectReceiptID(req.SessionID, firstID))
		if err != nil {
			return p, err
		}
		p.SnapshotSeq = first.SnapshotSeq
	}
	return p, nil
}

// CollectPending executes up to max request batches, each in its own
// transaction, paging through the queue (G2, H3). collectorFor supplies the
// authenticated collector for a request, or false to leave it pending;
// disabled triggers and requests already collected by another worker stay
// uncounted. Only request-level permanent failures quarantine. Configuration
// errors are reported without charging; infrastructure failures record an
// attempt and remain pending. Item retries and skips belong to the batch.
// Each call makes at most max attempts, scans at most maxGCPagesPerCall
// pages, and stops when ctx is done.
func (s *Service) CollectPending(ctx context.Context, session string, collectorFor func(domain.GCRequest) (domain.Principal, bool), max int) (int, error) {
	if collectorFor == nil || max <= 0 {
		return 0, domain.ErrInvalidRecord
	}
	done, attempts := 0, 0
	var failures []error
	var after store.Cursor
	for pages := 0; attempts < max && pages < maxGCPagesPerCall; pages++ {
		if err := ctx.Err(); err != nil {
			return done, errors.Join(append(failures, err)...)
		}
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
			if attempts == max {
				break
			}
			if err := ctx.Err(); err != nil {
				return done, errors.Join(append(failures, err)...)
			}
			p, ok := collectorFor(r)
			if !ok {
				continue
			}
			if !s.policy.GCTriggerEnabled(r.Trigger) {
				failures = append(failures, fmt.Errorf("GC request %s: %w", r.ID, ErrGCTriggerDisabled))
				continue
			}
			attempts++
			executed, err := s.collectPendingOne(ctx, session, p, r.ID)
			if err == nil {
				if executed {
					done++
				}
				continue
			}
			switch kind, code := classifyGCFailure(err); kind {
			case gcPermanent:
				if qerr := s.settleGCFailure(ctx, session, r.ID, code, false); qerr != nil {
					failures = append(failures, fmt.Errorf("GC request %s: %w", r.ID, errors.Join(err, qerr)))
				}
			case gcTransient:
				failures = append(failures, fmt.Errorf("GC request %s: %w", r.ID, err))
				if qerr := s.settleGCFailure(ctx, session, r.ID, domain.GCFailureAttemptsExhausted, true); qerr != nil {
					failures = append(failures, fmt.Errorf("GC request %s attempt: %w", r.ID, qerr))
				}
			default:
				failures = append(failures, fmt.Errorf("GC request %s: %w", r.ID, err))
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

// collectPendingOne runs one batch in its own transaction. Under the
// session writer, a request another worker already finished is skipped: no
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

// settleGCFailure records a failed attempt in its own transaction (H3). A
// permanent failure quarantines at once with code; a transient one (attempt)
// counts toward maxGCAttempts and quarantines as code on the last. A request
// finished meanwhile is left alone.
func (s *Service) settleGCFailure(ctx context.Context, session, id string, code domain.GCFailureCode, attempt bool) error {
	return s.store.Update(ctx, session, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		if _, err := sem.GCResult(id); err == nil {
			return nil
		} else if !errors.Is(err, domain.ErrNotFound) {
			return err
		}
		if attempt {
			p, err := gcProgress(sem, id)
			if err != nil {
				return err
			}
			next := p
			next.SessionID, next.GCRequestID = session, id
			next.Attempts++
			next.Revision++
			_, err = sem.PutGCProgress(next, p.Revision)
			return err
		}
		result := domain.GCResult{SemanticMeta: domain.SemanticMeta{ID: gcResultID(session, id), SessionID: session, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
			GCRequestID: id, Outcome: domain.GCFailed, Reason: code}
		return sem.InsertGCResult(result)
	})
}
