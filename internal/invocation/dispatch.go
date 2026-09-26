package invocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// MarkSent durably moves a PREPARED call to SENT and opens a new transport
// attempt. The dispatcher calls it immediately before transport and sends
// only after it returns (FR-CALL-002).
//
// The unsent request is revalidated first: a semantic change committed since
// the call's SemanticSeq fails with domain.ErrVersionConflict and leaves the
// call PREPARED, so the dispatcher cancels and replans (FR-CALL-001,
// FR-CALL-005). Any state other than PREPARED fails with
// domain.ErrInvalidTransition: SENT and UNKNOWN never resend.
func (l *Ledger) MarkSent(ctx context.Context, actor domain.Principal, callID, providerRequestID string) (domain.CallAttempt, error) {
	if err := checkServiceActor(actor); err != nil {
		return domain.CallAttempt{}, err
	}
	var out domain.CallAttempt
	err := l.store.Update(ctx, actor.SessionID, func(tx store.Tx) error {
		c, err := loadCall(tx, actor, callID)
		if err != nil {
			return err
		}
		if c.State != domain.CallPrepared {
			return fmt.Errorf("call %s: send from %s: %w", callID, c.State, domain.ErrInvalidTransition)
		}
		stale, err := semanticStale(tx, c.SemanticSeq)
		if err != nil {
			return err
		}
		if stale {
			return fmt.Errorf("call %s: semantic state changed after sequence %d: %w", callID, c.SemanticSeq, domain.ErrVersionConflict)
		}
		// The open attempt is stored before the call moves to SENT; the
		// store accepts PREPARED -> SENT only with that attempt present.
		seq := tx.NextSeq()
		c.Attempts++
		out = domain.CallAttempt{
			CallID:            callID,
			SessionID:         c.SessionID,
			Attempt:           c.Attempts,
			State:             domain.AttemptSent,
			ProviderRequestID: providerRequestID,
			SentSeq:           seq,
			SentAt:            l.now(),
		}
		if err := tx.PutCallAttempt(out); err != nil {
			return err
		}
		_, err = l.transition(tx, c, domain.CallSent, seq, domain.LifecycleEvent{Action: ActionSend, Actor: actor})
		return err
	})
	if err != nil {
		return domain.CallAttempt{}, err
	}
	return out, nil
}

// outcomeAudit is the audit record of one attempt's outcome, stored as the
// payload of its lifecycle event so per-attempt usage survives retries.
// Response bytes are stored separately under ResponseHash.
type outcomeAudit struct {
	CallID        string                  `json:"call_id"`
	Attempt       int                     `json:"attempt"`
	Late          bool                    `json:"late,omitempty"`
	State         domain.CallState        `json:"state"`
	ResponseHash  string                  `json:"response_hash,omitempty"`
	FailureReason string                  `json:"failure_reason,omitempty"`
	Retryable     bool                    `json:"retryable,omitempty"`
	Usage         []domain.UsageIteration `json:"usage,omitempty"`
}

// putOutcome stores an outcome's response and audit blobs and returns the
// audit hash.
func putOutcome(tx store.Tx, callID string, late bool, o domain.CallOutcome) (string, error) {
	if o.ResponseHash != "" {
		b := domain.Blob{SessionID: tx.SessionID(), Hash: o.ResponseHash, MediaType: responseMediaType, Data: o.Response}
		if err := tx.InsertBlob(b); err != nil {
			return "", err
		}
	}
	return putAudit(tx, outcomeAuditOf(callID, late, o))
}

func outcomeAuditOf(callID string, late bool, o domain.CallOutcome) outcomeAudit {
	return outcomeAudit{
		CallID: callID, Attempt: o.Attempt, Late: late, State: o.State,
		ResponseHash: o.ResponseHash, FailureReason: o.FailureReason, Retryable: o.Retryable, Usage: o.Usage,
	}
}

// storedOutcome rebuilds the outcome attempt a closed with from its audit
// blob (the payload of the lifecycle event at a.FinishedSeq) and response
// blob, and verifies it against a.OutcomeHash.
func storedOutcome(tx store.ReadTx, a domain.CallAttempt) (domain.CallOutcome, error) {
	evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetCall, TargetID: a.CallID, MinSeq: a.FinishedSeq})
	if err != nil {
		return domain.CallOutcome{}, err
	}
	if len(evs) == 0 || evs[0].Seq != a.FinishedSeq || evs[0].PayloadHash == "" {
		return domain.CallOutcome{}, fmt.Errorf("call %s attempt %d: outcome audit missing: %w", a.CallID, a.Attempt, domain.ErrIntegrity)
	}
	b, err := tx.Blob(evs[0].PayloadHash)
	if err != nil {
		return domain.CallOutcome{}, err
	}
	var au outcomeAudit
	if err := json.Unmarshal(b.Data, &au); err != nil {
		return domain.CallOutcome{}, fmt.Errorf("call %s attempt %d: %v: %w", a.CallID, a.Attempt, err, domain.ErrIntegrity)
	}
	o := domain.CallOutcome{
		Attempt: au.Attempt, State: au.State, ResponseHash: au.ResponseHash,
		FailureReason: au.FailureReason, Retryable: au.Retryable, Usage: au.Usage,
	}
	if o.ResponseHash != "" {
		rb, err := tx.Blob(o.ResponseHash)
		if err != nil {
			return domain.CallOutcome{}, err
		}
		o.Response = rb.Data
	}
	if o.OutcomeHash() != a.OutcomeHash {
		return domain.CallOutcome{}, fmt.Errorf("call %s attempt %d: stored outcome does not match its hash: %w", a.CallID, a.Attempt, domain.ErrIntegrity)
	}
	return o, nil
}

// RecordOutcome records the provider outcome of one transport attempt
// (FR-CALL-003). o.Attempt is the CallAttempt.Attempt number MarkSent
// returned; binding the outcome to its attempt keeps a delayed duplicate of
// an earlier retried attempt from closing a later one.
//
// From SENT or UNKNOWN:
//   - COMPLETED advances the conversation Version exactly once, advances
//     LogicalCalls only for inference, moves the conversation to the call's
//     Epoch, and releases the reservation.
//   - FAILED releases the reservation, except a Retryable failure from SENT,
//     which returns the call to PREPARED with the reservation kept; the next
//     MarkSent revalidates it. A retryable failure found by reconciling an
//     UNKNOWN call is terminal, because UNKNOWN -> PREPARED is not a valid
//     transition.
//
// Idempotency is per attempt through CallAttempt.OutcomeHash: repeating the
// outcome an attempt closed with returns the current record without
// writing, and a different outcome fails with domain.ErrCallOutcomeConflict.
// An outcome for an attempt that was never sent fails with
// domain.ErrInvalidTransition. An outcome for an ABANDONED attempt is stored
// once as audit data and returns the unchanged record with ErrLateOutcome.
//
// The attempt is closed before the call transitions: the store accepts a
// call leaving SENT or UNKNOWN only when its latest attempt is already stored
// in the matching closed state.
func (l *Ledger) RecordOutcome(ctx context.Context, actor domain.Principal, callID string, o domain.CallOutcome) (domain.CallRecord, error) {
	if err := checkServiceActor(actor); err != nil {
		return domain.CallRecord{}, err
	}
	if err := o.Validate(); err != nil {
		return domain.CallRecord{}, err
	}
	hash := o.OutcomeHash()
	var out domain.CallRecord
	late := false
	err := l.store.Update(ctx, actor.SessionID, func(tx store.Tx) error {
		c, err := loadCall(tx, actor, callID)
		if err != nil {
			return err
		}
		out = c
		if o.Attempt > c.Attempts {
			return fmt.Errorf("call %s: no sent attempt %d: %w", callID, o.Attempt, domain.ErrInvalidTransition)
		}
		a, err := attempt(tx, callID, o.Attempt)
		if err != nil {
			return err
		}
		switch {
		case a.State == domain.AttemptAbandoned:
			late = true
			return l.recordLate(tx, actor, c, o)
		case a.OutcomeHash == hash:
			return nil // idempotent repeat
		case a.OutcomeHash != "":
			return fmt.Errorf("call %s attempt %d closed with another outcome: %w", callID, o.Attempt, domain.ErrCallOutcomeConflict)
		case a.Attempt != c.Attempts || (c.State != domain.CallSent && c.State != domain.CallUnknown):
			// Unreachable while every closed attempt carries an outcome.
			return fmt.Errorf("call %s attempt %d is %s: %w", callID, o.Attempt, a.State, domain.ErrInvalidTransition)
		}

		auditHash, err := putOutcome(tx, callID, false, o)
		if err != nil {
			return err
		}
		seq := tx.NextSeq()
		ev := domain.LifecycleEvent{Action: ActionOutcome, Actor: actor, PayloadHash: auditHash, Reason: o.FailureReason}
		to, attemptState := o.State, domain.AttemptFailed
		switch {
		case o.State == domain.CallCompleted:
			attemptState = domain.AttemptCompleted
		case o.Retryable && c.State == domain.CallSent:
			to, ev.Action = domain.CallPrepared, ActionRetry
		}
		a.State, a.OutcomeHash, a.Retryable, a.FinishedSeq, a.FinishedAt = attemptState, hash, o.Retryable, seq, l.now()
		if err := tx.PutCallAttempt(a); err != nil {
			return err
		}
		if to.Terminal() {
			c.Outcome = &o
			c.OutcomeHash = hash
		}
		next, err := l.transition(tx, c, to, seq, ev)
		if err != nil {
			return err
		}
		switch to {
		case domain.CallCompleted:
			err = releaseReservation(tx, next, func(conv *domain.Conversation) {
				conv.Version++
				if next.Operation == domain.OperationInference {
					conv.LogicalCalls++
				}
				conv.Epoch = next.Epoch
				conv.RequireNewEpoch = false
			})
		case domain.CallFailed:
			err = releaseReservation(tx, next, nil)
		}
		out = next
		return err
	})
	if err != nil {
		return domain.CallRecord{}, err
	}
	if late {
		return out, ErrLateOutcome
	}
	return out, nil
}

// recordLate audits an outcome that arrived after abandonment. The
// abandoned attempt is immutable, so the content-addressed audit blob is the
// duplicate check: an identical late outcome is audited once. It never
// changes the call or the conversation (FR-CALL-004, INV-15).
func (l *Ledger) recordLate(tx store.Tx, actor domain.Principal, c domain.CallRecord, o domain.CallOutcome) error {
	audit := outcomeAuditOf(c.CallID, true, o)
	data, err := json.Marshal(audit)
	if err != nil {
		return err
	}
	if _, err := tx.Blob(domain.HashBytes(data)); err == nil {
		return nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	auditHash, err := putOutcome(tx, c.CallID, true, o)
	if err != nil {
		return err
	}
	return l.appendEvent(tx, c, tx.NextSeq(), domain.LifecycleEvent{
		Action: ActionLateOutcome, From: string(c.State), To: string(c.State),
		Actor: actor, PayloadHash: auditHash, Reason: o.FailureReason,
	})
}

// Cancel fails a PREPARED call as a known unsent failure and releases its
// reservation (FR-CALL-004, SDD section 8). The record carries the Reason;
// a call never sent has no Outcome, and a call waiting to retry keeps its
// last attempt's retryable failure as its Outcome. Once SENT is durable a
// call cannot be cancelled: it fails with domain.ErrInvalidTransition.
func (l *Ledger) Cancel(ctx context.Context, actor domain.Principal, callID, reason string) (domain.CallRecord, error) {
	if err := checkServiceActor(actor); err != nil {
		return domain.CallRecord{}, err
	}
	var out domain.CallRecord
	err := l.store.Update(ctx, actor.SessionID, func(tx store.Tx) error {
		c, err := loadCall(tx, actor, callID)
		if err != nil {
			return err
		}
		if c.State != domain.CallPrepared {
			return fmt.Errorf("call %s: cancel from %s: %w", callID, c.State, domain.ErrInvalidTransition)
		}
		c.Reason = reason
		if c.Attempts > 0 {
			// A retried call's final outcome is its last attempt's retryable
			// failure; the cancellation reason is recorded alongside it.
			a, err := attempt(tx, callID, c.Attempts)
			if err != nil {
				return err
			}
			o, err := storedOutcome(tx, a)
			if err != nil {
				return err
			}
			c.Outcome, c.OutcomeHash = &o, a.OutcomeHash
		}
		next, err := l.transition(tx, c, domain.CallFailed, tx.NextSeq(), domain.LifecycleEvent{
			Action: ActionCancel, Actor: actor, Reason: reason,
		})
		if err != nil {
			return err
		}
		out = next
		return releaseReservation(tx, next, nil)
	})
	if err != nil {
		return domain.CallRecord{}, err
	}
	return out, nil
}
