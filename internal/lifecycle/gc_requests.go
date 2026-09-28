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
// explicit enabled set; test it with errors.Is (DUR-4.9). The durable
// request, if any, stays pending: this is a configuration fault, never a
// charge on the request (J5).
var ErrGCConfiguration = errors.New("lifecycle: invalid collector configuration")

var ErrGCTriggerDisabled = fmt.Errorf("lifecycle: GC trigger disabled by policy: %w", domain.ErrInvalidTransition)

// ErrGCPolicyVersion reports a pending GC request recorded under another
// policy version (DUR-4.9, ruling M1); test it with errors.Is. CollectPending
// settles such a request FAILED/POLICY_MISMATCH — uncharged, nothing
// archived — so it stops stranding and the normal re-arm path applies under
// the current policy.
var ErrGCPolicyVersion = fmt.Errorf("lifecycle: GC request recorded under another policy version: %w", domain.ErrUnsupportedSchema)

// EnqueueGC persists a durable GC request in the producer's transaction
// (P3-39) under this executor's policy; it is a thin wrapper over
// gcqueue.Enqueue, which producers below lifecycle call directly with their
// event's recorded policy (SPEC-2.11). A session-scoped (task-less) automatic
// trigger produces nothing (H4); manual session Collect can resume (J7).
func (s *Service) EnqueueGC(tx store.Tx, origin domain.Principal, trigger domain.GCTrigger, scope domain.CollectScope, taskID, triggerID string) (string, error) {
	switch scope {
	case domain.CollectTask:
		return gcqueue.Enqueue(tx, s.policy, origin, trigger, taskID, triggerID)
	case domain.CollectSession:
		return "", nil // automatic task-less producers are disabled (H4)
	}
	tx.Poison(domain.ErrInvalidRecord)
	return "", domain.ErrInvalidRecord
}

// ErrGCRequestFailed reports a quarantined GC request: it recorded a FAILED
// outcome and is never retried automatically (H3); re-arming needs a new
// request identity.
var ErrGCRequestFailed = fmt.Errorf("lifecycle: GC request quarantined: %w", domain.ErrInvalidTransition)

// maxGCAttempts bounds transient reads of one candidate before an explicit
// SKIP_ATTEMPTS_EXHAUSTED decision (J4).
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
// belong to that task. Continuation binds to that authority class plus
// task, never to the principal that ran an earlier batch: any authorized
// collector may continue a pending request (SEC-4.5). A failure rolls back
// only this batch: CollectPending
// records the attempt or quarantine in its own transaction.
func (s *Service) ExecuteGCRequest(tx store.Tx, collector domain.Principal, gcRequestID string, seq uint64) (out MutationOutcome, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			out = MutationOutcome{}
		}
	}()
	// Collector faults are configuration errors, never the request's:
	// they are not charged and the request stays pending (J5, DUR-3.3).
	if err = collector.Validate(); err != nil {
		return out, errors.Join(ErrGCConfiguration, err)
	}
	if _, err = domain.MutationRequestHash(collector, domain.MutationCollection, methodCollect, []byte{0}); err != nil {
		return out, errors.Join(ErrGCConfiguration, err)
	}
	if collector.SessionID != tx.SessionID() {
		return out, fmt.Errorf("%w: collector belongs to another session", ErrGCConfiguration)
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
	if i.RequestID, err = req.BatchRequestID(progress.Batches + 1); err != nil {
		return out, err
	}
	return s.collect(tx, collector, i, &gcBatch{requestID: req.ID, progress: progress}, seq)
}

// RearmGCRequest re-enqueues a FAILED request under a new, deterministic
// request identity (DUR-3.3): same scope, task and trigger, keyed on the
// failed request alone, so repeating the re-arm — by any authorized actor —
// returns the same request. The failed record stays immutable. Only
// SYSTEM/HARNESS may re-arm, and a task-scoped request re-arms only from
// its own task — SYSTEM included, exactly as ExecuteGCRequest binds its
// collector (SPEC-5.6); that binding is checked before any outcome
// check and is indistinguishable from an absent request, so a foreign
// principal learns nothing about whether a predictable gcq_ ID exists
// (SEC-4.4). MANUAL and session-scope requests re-arm (DUR-4.8); a trigger
// this executor's policy disables reports ErrGCTriggerDisabled. The actor
// is recorded as the new request's origin, never its collector.
func (s *Service) RearmGCRequest(tx store.Tx, actor domain.Principal, failedID string) (id string, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			id = ""
		}
	}()
	if err = actor.Validate(); err != nil {
		return "", err
	}
	if actor.SessionID != tx.SessionID() {
		return "", domain.ErrNotFound
	}
	if actor.Authority != domain.AuthoritySystem && actor.Authority != domain.AuthorityHarness {
		return "", domain.ErrInvalidAuthorityPromotion
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return "", err
	}
	req, err := sem.GCRequest(failedID)
	if errors.Is(err, domain.ErrNotFound) || err == nil && req.SessionID != actor.SessionID {
		return "", domain.ErrNotFound
	}
	if err != nil {
		return "", err
	}
	// Task scope binds the re-arm to the request's task before any outcome
	// check, for SYSTEM and HARNESS alike (SPEC-5.6): a foreign principal
	// sees ErrNotFound for pending, failed and absent requests alike
	// (SEC-4.4: no existence oracle).
	if req.Scope == domain.CollectTask && actor.TaskID != req.TaskID {
		return "", domain.ErrNotFound
	}
	res, err := sem.GCResult(req.ID)
	if errors.Is(err, domain.ErrNotFound) || err == nil && res.Outcome != domain.GCFailed {
		return "", domain.ErrInvalidTransition // only a FAILED request re-arms
	}
	if err != nil {
		return "", err
	}
	id, err = gcqueue.EnqueueRearm(tx, s.policy, actor, req)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", ErrGCTriggerDisabled // the trigger is disabled under this policy
	}
	return id, nil
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
func (s *Service) CollectPending(ctx context.Context, session string, collectorFor func(domain.GCRequest) (domain.Principal, bool), max int) (n int, err error) {
	if collectorFor == nil || max <= 0 {
		return 0, domain.ErrInvalidRecord
	}
	done, attempts := 0, 0
	var failures []error
	// Only enabled triggers are read, through the per-trigger pending index,
	// so disabled ones never fill a page; the scan resumes from the session's
	// durable cursor, so no request behind a long skipped prefix starves
	// across calls or restarts (J6, DUR-3.2).
	triggers := s.queueTriggers()
	// A request waiting on a disabled trigger is a configuration fault: it is
	// reported (J5) but never read into the scan, charged or collected.
	defer func() {
		if derr := s.disabledTriggerPending(ctx, session); derr != nil {
			err = errors.Join(err, derr)
		}
	}()
	if len(triggers) == 0 {
		return 0, nil
	}
	start, lerr := s.loadQueueCursor(ctx, session)
	if lerr != nil {
		return 0, lerr
	}
	after := store.Cursor{Seq: start.Cursor.Seq, ID: start.Cursor.ID}
	defer func() {
		if serr := s.saveQueueCursor(ctx, session, start, after); serr != nil {
			err = errors.Join(err, serr)
		}
	}()
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
			page, err = sem.PendingGCRequestsByTrigger(triggers, store.Page{After: after, Limit: s.policy.MaxPageSize})
			return err
		}); err != nil {
			return done, errors.Join(append(failures, err)...)
		}
		for _, r := range page.Records {
			if attempts == max {
				return done, errors.Join(failures...)
			}
			after = store.Cursor{Seq: r.Seq, ID: r.ID}
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
			// A request recorded under another policy version cannot run
			// under this one (DUR-4.9, ruling M1): settle it
			// FAILED/POLICY_MISMATCH — uncharged, nothing archived — so it
			// stops stranding and the normal re-arm path applies under the
			// current policy.
			if r.PolicyVersion != s.policy.Version {
				failures = append(failures, fmt.Errorf("GC request %s: %w", r.ID, ErrGCPolicyVersion))
				if qerr := s.settleGCFailure(ctx, session, r.ID, domain.GCFailurePolicyMismatch, false); qerr != nil {
					failures = append(failures, fmt.Errorf("GC request %s: %w", r.ID, qerr))
				}
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
			after = store.Cursor{} // wrap; previously skipped requests get another opportunity
			break
		}
		if len(page.Records) == 0 {
			return done, errors.Join(append(failures, domain.ErrIntegrity)...)
		}
	}
	return done, errors.Join(failures...)
}

// queueTriggers are the policy's enabled triggers, in its canonical sorted
// order; MANUAL is included, since a resumable manual collection persists a
// durable request (J7).
func (s *Service) queueTriggers() []domain.GCTrigger {
	return append([]domain.GCTrigger(nil), s.policy.GCTriggers...)
}

// disabledTriggerPending reports ErrGCTriggerDisabled when any request waits
// on a trigger this executor's policy disables, with one single-record page
// of the per-trigger index: O(1), whatever the backlog (J5, DUR-3.2).
func (s *Service) disabledTriggerPending(ctx context.Context, session string) error {
	var disabled []domain.GCTrigger
	for _, t := range domain.DefaultGCTriggers() {
		if !s.policy.GCTriggerEnabled(t) {
			disabled = append(disabled, t)
		}
	}
	if len(disabled) == 0 {
		return nil
	}
	var waiting bool
	err := s.store.View(ctx, session, func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		page, err := sem.PendingGCRequestsByTrigger(disabled, store.Page{Limit: 1})
		waiting = len(page.Records) != 0
		return err
	})
	if err != nil {
		return err
	}
	if waiting {
		return fmt.Errorf("%w: GC requests wait on triggers %v", ErrGCTriggerDisabled, disabled)
	}
	return nil
}

// loadQueueCursor reads the session's durable queue cursor; the zero cursor
// (Revision 0) before the first scan.
func (s *Service) loadQueueCursor(ctx context.Context, session string) (domain.GCQueueCursor, error) {
	var c domain.GCQueueCursor
	err := s.store.View(ctx, session, func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		c, err = sem.GCQueueCursor()
		if errors.Is(err, domain.ErrNotFound) {
			c, err = domain.GCQueueCursor{}, nil
		}
		return err
	})
	return c, err
}

// saveQueueCursor CAS-advances the session's cursor to after, in its own
// transaction. A worker that moved it first wins; this call's position is
// dropped rather than regressing theirs.
func (s *Service) saveQueueCursor(ctx context.Context, session string, start domain.GCQueueCursor, after store.Cursor) error {
	next := domain.GCQueueCursor{SessionID: session, Cursor: domain.GCCursor{Seq: after.Seq, ID: after.ID}, Revision: start.Revision + 1}
	if next.Cursor == start.Cursor && start.Revision != 0 {
		return nil
	}
	err := s.store.Update(context.WithoutCancel(ctx), session, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		_, err = sem.PutGCQueueCursor(next, start.Revision)
		return err
	})
	if errors.Is(err, domain.ErrVersionConflict) {
		return nil
	}
	return err
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
// permanent failure quarantines at once with code; an infrastructure
// failure (attempt) records operational statistics and remains pending
// until maxGCAttempts consecutive failed batches — the counter resets on
// progress (DUR-3.4) — quarantine it FAILED/ATTEMPTS_EXHAUSTED (DUR-4.5).
// A request finished meanwhile is left alone.
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
			if next.Attempts >= maxGCAttempts {
				result := domain.GCResult{SemanticMeta: domain.SemanticMeta{ID: gcResultID(session, id), SessionID: session, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
					GCRequestID: id, Outcome: domain.GCFailed, Reason: domain.GCFailureAttemptsExhausted}
				return sem.InsertGCResult(result)
			}
			_, err = sem.PutGCProgress(next, p.Revision)
			return err
		}
		result := domain.GCResult{SemanticMeta: domain.SemanticMeta{ID: gcResultID(session, id), SessionID: session, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
			GCRequestID: id, Outcome: domain.GCFailed, Reason: code}
		return sem.InsertGCResult(result)
	})
}
