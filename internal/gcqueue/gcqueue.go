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

// EnqueueRearm persists the re-arm of a FAILED GC request under the
// executor's policy pol (DUR-3.3, SEC-4.4, DUR-4.8). The new durable
// request keeps the failed one's scope, task and trigger — MANUAL and J7
// session scope included — and its identity is
// domain.GCRearmRequestID(failed.ID): derived from the failed request
// alone, so any authorized actor re-arming yields the same request
// (idempotent), in an encoder domain no runtime trigger, manual collection
// or caller can alias. actor is recorded as the new request's origin
// context only, never its collector.
//
// As in Enqueue, task completion always persists (P3-39); any other
// trigger this policy disables persists nothing and returns ("", nil) —
// the lifecycle re-arm path reports that as ErrGCTriggerDisabled. Any
// failure poisons tx.
func EnqueueRearm(tx store.Tx, pol domain.Phase3Policy, actor domain.Principal, failed domain.GCRequest) (id string, err error) {
	defer func() {
		if err != nil {
			tx.Poison(err)
			id = ""
		}
	}()
	if err := pol.Validate(); err != nil {
		return "", err
	}
	if err := actor.Validate(); err != nil {
		return "", err
	}
	if actor.SessionID != tx.SessionID() || failed.SessionID != tx.SessionID() {
		return "", domain.ErrNotFound
	}
	if err := failed.Validate(); err != nil {
		return "", err
	}
	if failed.Trigger != domain.GCTaskCompletion && !pol.GCTriggerEnabled(failed.Trigger) {
		return "", nil
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return "", err
	}
	requestID, err := domain.GCRearmRequestID(failed.ID)
	if err != nil {
		return "", err
	}
	recordID, err := domain.GCRequestRecordID(actor.SessionID, requestID)
	if err != nil {
		return "", err
	}
	r := domain.GCRequest{SemanticMeta: domain.SemanticMeta{ID: recordID, SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1},
		CollectIntent: domain.CollectIntent{RequestID: requestID, Scope: failed.Scope, TaskID: failed.TaskID, Trigger: failed.Trigger},
		Origin:        actor, PolicyVersion: pol.Version}
	prior, err := sem.GCRequest(r.ID)
	if err == nil {
		// The identity ignores the actor, so a re-arm by any other
		// authorized actor replays the first one's request; only different
		// intent content is a conflict.
		if prior.CollectIntent != r.CollectIntent {
			return "", domain.ErrEventIDConflict
		}
		r.Seq = prior.Seq
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
