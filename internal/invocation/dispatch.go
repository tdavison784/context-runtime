package invocation

import (
	"context"
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
		seq := tx.NextSeq()
		c.Attempts++
		if _, err := l.transition(tx, c, domain.CallSent, seq, domain.LifecycleEvent{Action: ActionSend, Actor: actor}); err != nil {
			return err
		}
		out = domain.CallAttempt{
			CallID:            callID,
			SessionID:         c.SessionID,
			Attempt:           c.Attempts,
			State:             domain.AttemptSent,
			ProviderRequestID: providerRequestID,
			SentSeq:           seq,
			SentAt:            l.now(),
		}
		return tx.PutCallAttempt(out)
	})
	if err != nil {
		return domain.CallAttempt{}, err
	}
	return out, nil
}

// outcomeAudit is the audit record of an outcome for one attempt. It holds
// exactly the inputs of domain.CallOutcome.OutcomeHash plus the call,
// attempt, and whether it arrived late, so its content-addressed blob is also
// the idempotency receipt for that (call, attempt, outcome). Response bytes
// are stored separately under ResponseHash.
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

func auditOf(callID string, attempt int, late bool, o domain.CallOutcome) outcomeAudit {
	return outcomeAudit{
		CallID: callID, Attempt: attempt, Late: late, State: o.State,
		ResponseHash: o.ResponseHash, FailureReason: o.FailureReason, Retryable: o.Retryable, Usage: o.Usage,
	}
}

// RecordOutcome records the provider outcome of a call's transport attempt
// (FR-CALL-003). attempt is the CallAttempt.Attempt number MarkSent returned;
// binding the outcome to its attempt keeps a delayed duplicate of an earlier
// retried attempt from completing a later one.
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
// An outcome's ResponseHash is filled from its Response bytes before its
// OutcomeHash is taken, so reporting a response by bytes or by hash is the
// same outcome. Repeating an identical outcome for the same attempt returns the current
// record without writing; a different outcome for an attempt that already
// has one fails with domain.ErrCallOutcomeConflict. An outcome for a PREPARED
// call's unsent attempt fails with domain.ErrInvalidTransition. An outcome
// for an ABANDONED call is stored as audit data and returns the unchanged
// record with ErrLateOutcome.
func (l *Ledger) RecordOutcome(ctx context.Context, actor domain.Principal, callID string, attemptNo int, o domain.CallOutcome) (domain.CallRecord, error) {
	if err := checkServiceActor(actor); err != nil {
		return domain.CallRecord{}, err
	}
	o, err := normalizeOutcome(o)
	if err != nil {
		return domain.CallRecord{}, err
	}
	var out domain.CallRecord
	late := false
	err = l.store.Update(ctx, actor.SessionID, func(tx store.Tx) error {
		c, err := loadCall(tx, actor, callID)
		if err != nil {
			return err
		}
		out = c
		if attemptNo < 1 || attemptNo > c.Attempts {
			return fmt.Errorf("call %s: no sent attempt %d: %w", callID, attemptNo, domain.ErrInvalidTransition)
		}
		// Idempotent repeat: this exact outcome was already recorded.
		for _, isLate := range []bool{false, true} {
			seen, err := receiptExists(tx, auditOf(callID, attemptNo, isLate, o))
			if err != nil {
				return err
			}
			if seen {
				late = isLate
				return nil
			}
		}
		a, err := attempt(tx, callID, attemptNo)
		if err != nil {
			return err
		}

		if c.State == domain.CallAbandoned && a.State == domain.AttemptAbandoned {
			late = true
			return l.recordLate(tx, actor, c, attemptNo, o)
		}
		if attemptNo != c.Attempts || (c.State != domain.CallSent && c.State != domain.CallUnknown) {
			return fmt.Errorf("call %s attempt %d already closed as %s: %w", callID, attemptNo, a.State, domain.ErrCallOutcomeConflict)
		}

		if o.ResponseHash != "" && o.Response != nil {
			if err := tx.InsertBlob(domain.Blob{SessionID: c.SessionID, Hash: o.ResponseHash, MediaType: responseMediaType, Data: o.Response}); err != nil {
				return err
			}
		}
		auditHash, err := putAudit(tx, auditOf(callID, attemptNo, false, o))
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
		if to.Terminal() {
			c.Outcome = &o
			c.OutcomeHash = o.OutcomeHash()
		}
		next, err := l.transition(tx, c, to, seq, ev)
		if err != nil {
			return err
		}
		a.State, a.FinishedSeq, a.FinishedAt = attemptState, seq, l.now()
		if err := tx.PutCallAttempt(a); err != nil {
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

// recordLate audits an outcome that arrived after abandonment. It never
// touches the call or the conversation (FR-CALL-004, INV-15).
func (l *Ledger) recordLate(tx store.Tx, actor domain.Principal, c domain.CallRecord, attemptNo int, o domain.CallOutcome) error {
	if o.ResponseHash != "" && o.Response != nil {
		if err := tx.InsertBlob(domain.Blob{SessionID: c.SessionID, Hash: o.ResponseHash, MediaType: responseMediaType, Data: o.Response}); err != nil {
			return err
		}
	}
	auditHash, err := putAudit(tx, auditOf(c.CallID, attemptNo, true, o))
	if err != nil {
		return err
	}
	return l.appendEvent(tx, c, tx.NextSeq(), domain.LifecycleEvent{
		Action: ActionLateOutcome, From: string(c.State), To: string(c.State),
		Actor: actor, PayloadHash: auditHash, Reason: o.FailureReason,
	})
}

func receiptExists(tx store.ReadTx, a outcomeAudit) (bool, error) {
	data, err := jsonBytes(a)
	if err != nil {
		return false, err
	}
	return blobExists(tx, domain.HashBytes(data))
}

// normalizeOutcome validates an outcome and fills ResponseHash from Response
// bytes, so an outcome reported with or without its bytes has one identity.
func normalizeOutcome(o domain.CallOutcome) (domain.CallOutcome, error) {
	if o.State != domain.CallCompleted && o.State != domain.CallFailed {
		return o, fmt.Errorf("outcome state must be COMPLETED or FAILED, got %q: %w", o.State, domain.ErrInvalidRecord)
	}
	if o.Response != nil {
		h := domain.HashBytes(o.Response)
		if o.ResponseHash != "" && o.ResponseHash != h {
			return o, fmt.Errorf("outcome response hash does not match response bytes: %w", domain.ErrInvalidRecord)
		}
		o.ResponseHash = h
	}
	if o.ResponseHash != "" && !domain.ValidHash(o.ResponseHash) {
		return o, fmt.Errorf("outcome response hash is malformed: %w", domain.ErrInvalidRecord)
	}
	if o.State == domain.CallCompleted && (o.ResponseHash == "" || o.Retryable) {
		return o, fmt.Errorf("completed outcome needs a response and cannot be retryable: %w", domain.ErrInvalidRecord)
	}
	return o, nil
}

// Cancel fails a PREPARED call as a known unsent failure and releases its
// reservation (FR-CALL-004, SDD section 8). Once SENT is durable a call
// cannot be cancelled: it fails with domain.ErrInvalidTransition.
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
		c.CancelReason = reason
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
