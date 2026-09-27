// Package gcqueue persists durable GC requests (P3-39) from any producer's
// transaction. It imports only domain and store, so ingest, tools and
// obligation, which sit below lifecycle, can produce triggers directly.
package gcqueue

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Enqueue persists a GC request for trigger in tx and returns its ID.
//
//   - pol is the caller's RECORDED policy (SPEC-2.11): it decides whether
//     the trigger is enabled and is recorded as the request's policy.
//   - A trigger the policy disables persists nothing and returns "", nil.
//     Task completion always persists (P3-39).
//   - A task-less trigger persists nothing (H4): Phase 3 has no session
//     automatic GC, so producer requests are TASK scoped. Manual session
//     collection is batched by lifecycle.
//   - The request identity derives from the authenticated origin and the
//     trigger identity (H5), so a repeated trigger returns the existing
//     request and no caller can name it; the same identity with different
//     content is ErrEventIDConflict. MANUAL collection is never enqueued.
//
// origin is recorded as context only: it never becomes the collector. Any
// failure poisons tx.
func Enqueue(tx store.Tx, pol domain.Phase3Policy, origin domain.Principal, trigger domain.GCTrigger, taskID, triggerID string) (id string, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			id = ""
		}
	}()
	if err := pol.Validate(); err != nil {
		return "", err
	}
	if trigger == domain.GCManual || !trigger.Valid() || triggerID == "" {
		return "", domain.ErrInvalidRecord
	}
	if err := origin.Validate(); err != nil {
		return "", err
	}
	if origin.SessionID != tx.SessionID() {
		return "", domain.ErrNotFound
	}
	if trigger != domain.GCTaskCompletion && !pol.GCTriggerEnabled(trigger) || taskID == "" {
		return "", nil
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return "", err
	}
	// Runtime GC IDs bind the authenticated origin and live in reserved
	// namespaces no caller can name (H5, SEC-2.6).
	requestID, err := domain.GCTriggerRequestID(origin, trigger, triggerID)
	if err != nil {
		return "", err
	}
	recordID, err := domain.GCRequestRecordID(origin.SessionID, requestID)
	if err != nil {
		return "", err
	}
	r := domain.GCRequest{SemanticMeta: domain.SemanticMeta{ID: recordID, SessionID: origin.SessionID, SchemaVersion: domain.SemanticSchemaV1},
		CollectIntent: domain.CollectIntent{RequestID: requestID, Scope: domain.CollectTask, TaskID: taskID, Trigger: trigger}, Origin: origin, PolicyVersion: pol.Version}
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

// RequestID is the durable GC request's record ID for a request identity
// (domain.GCRequestRecordID).
func RequestID(session, requestID string) (string, error) {
	return domain.GCRequestRecordID(session, requestID)
}
