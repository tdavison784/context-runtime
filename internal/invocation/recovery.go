package invocation

import (
	"context"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Recover is startup recovery for one session (FR-CALL-002, FR-CALL-004,
// INV-15). A SENT call has no durable outcome, so the crash may have fallen in
// the send/acknowledgment gap: every SENT call and its open attempt become
// UNKNOWN. Nothing is resent and no conversation advances. It returns every
// UNKNOWN call in the session, including ones left by earlier recoveries,
// ordered by PreparedSeq; each needs RecordOutcome (reconciliation) or
// Abandon.
func (l *Ledger) Recover(ctx context.Context, actor domain.Principal) ([]domain.CallRecord, error) {
	if err := checkServiceActor(actor); err != nil {
		return nil, err
	}
	var out []domain.CallRecord
	err := l.store.Update(ctx, actor.SessionID, func(tx store.Tx) error {
		sent, err := tx.Calls(store.CallFilter{States: []domain.CallState{domain.CallSent}})
		if err != nil {
			return err
		}
		for _, c := range sent {
			if checkActorScope(actor, c) != nil {
				continue
			}
			seq := tx.NextSeq()
			if _, err := l.transition(tx, c, domain.CallUnknown, seq, domain.LifecycleEvent{
				Action: ActionRecover, Actor: actor, Reason: "restart before outcome",
			}); err != nil {
				return err
			}
			a, err := attempt(tx, c.CallID, c.Attempts)
			if err != nil {
				return err
			}
			a.State = domain.AttemptUnknown
			if err := tx.PutCallAttempt(a); err != nil {
				return err
			}
		}
		unknown, err := tx.Calls(store.CallFilter{States: []domain.CallState{domain.CallUnknown}})
		if err != nil {
			return err
		}
		for _, c := range unknown {
			if checkActorScope(actor, c) == nil {
				out = append(out, c)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// abandonAudit is the audit record of an abandonment and its uncertain
// usage.
type abandonAudit struct {
	CallID         string                  `json:"call_id"`
	Attempt        int                     `json:"attempt"`
	Reason         string                  `json:"reason"`
	UncertainUsage []domain.UsageIteration `json:"uncertain_usage"`
}

// Abandon explicitly gives up on an UNKNOWN call (FR-CALL-004): the call and
// its attempt become ABANDONED, the uncertain usage is recorded as audit
// data, the reservation is released, and the conversation must start a new
// epoch before its next operation. Only SYSTEM and HARNESS actors may
// abandon. Any state other than UNKNOWN fails with
// domain.ErrInvalidTransition. A response that arrives later is audit-only
// (see RecordOutcome).
func (l *Ledger) Abandon(ctx context.Context, actor domain.Principal, callID, reason string, uncertainUsage []domain.UsageIteration) (domain.CallRecord, error) {
	if err := checkServiceActor(actor); err != nil {
		return domain.CallRecord{}, err
	}
	var out domain.CallRecord
	err := l.store.Update(ctx, actor.SessionID, func(tx store.Tx) error {
		c, err := loadCall(tx, actor, callID)
		if err != nil {
			return err
		}
		if c.State != domain.CallUnknown {
			return fmt.Errorf("call %s: abandon from %s: %w", callID, c.State, domain.ErrInvalidTransition)
		}
		auditHash, err := putAudit(tx, abandonAudit{CallID: callID, Attempt: c.Attempts, Reason: reason, UncertainUsage: uncertainUsage})
		if err != nil {
			return err
		}
		seq := tx.NextSeq()
		next, err := l.transition(tx, c, domain.CallAbandoned, seq, domain.LifecycleEvent{
			Action: ActionAbandon, Actor: actor, Reason: reason, PayloadHash: auditHash,
		})
		if err != nil {
			return err
		}
		a, err := attempt(tx, callID, c.Attempts)
		if err != nil {
			return err
		}
		a.State, a.FinishedSeq, a.FinishedAt = domain.AttemptAbandoned, seq, l.now()
		if err := tx.PutCallAttempt(a); err != nil {
			return err
		}
		out = next
		return releaseReservation(tx, next, func(conv *domain.Conversation) { conv.RequireNewEpoch = true })
	})
	if err != nil {
		return domain.CallRecord{}, err
	}
	return out, nil
}
