// Package invocation is the durable provider-call ledger (FR-CALL-001 through
// FR-CALL-005, INV-15, ADR 17). It records every logical provider operation,
// inference or compaction, as a CallRecord that moves through the
// domain.ValidCallTransition state machine, and it holds the single
// per-conversation operation reservation.
//
// The ledger never talks to a provider. The trusted dispatcher drives it:
//
//	Prepare  -> freeze the request and reserve the conversation (PREPARED)
//	MarkSent -> persist SENT immediately before transport
//	transport (no store transaction is open)
//	RecordOutcome -> COMPLETED, FAILED, or retry (SENT -> PREPARED)
//
// Recover runs at startup and turns every SENT call into UNKNOWN; UNKNOWN is
// resolved only by RecordOutcome (reconciliation) or Abandon. Nothing is ever
// resent automatically (FR-CALL-002, INV-15).
//
// Every method is exactly one store Update. Every state transition allocates
// one session sequence number and appends exactly one LifecycleEvent with
// TargetKind call in the same transaction, so replay has a total order.
//
// # Semantic staleness
//
// A preview carries the session sequence number it was planned at
// (PrepareRequest.SemanticSeq). Phase 1 applies the strict rule of ADR 17: any
// committed semantic change after that sequence makes the preview stale. The
// ledger's own transitions also consume session sequence numbers but are not
// semantic changes, so the check is: every sequence number in
// (SemanticSeq, LastSeq] must belong to a call lifecycle event. Sequence
// numbers are dense (store.Tx.NextSeq), so this is a count comparison over
// the call lifecycle events after SemanticSeq, with no extra state to keep in
// sync. Any other use of a sequence number, including one the ledger does not
// recognize, counts as a semantic change; that errs toward rejecting a
// preview. The store contract reserves call lifecycle events for this
// package.
//
// # Service actors
//
// The ledger methods are not agent tools (SDD section 8). Every method takes
// the service actor driving it, which must be SYSTEM or HARNESS; any
// non-empty workflow, task, or agent on the actor must match the call's
// inference principal. The conversation-specific service grant
// (ActionDispatchCall) is an ADR 17 open item deferred to Phase 5.
package invocation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ErrLateOutcome reports an outcome for an ABANDONED call. The outcome is
// stored as audit data and never changes the call or its conversation
// (FR-CALL-004). It wraps domain.ErrInvalidTransition.
var ErrLateOutcome = fmt.Errorf("late outcome for abandoned call recorded as audit only: %w", domain.ErrInvalidTransition)

// Lifecycle event actions written by the ledger.
const (
	ActionPrepare     = "prepare"
	ActionSend        = "send"
	ActionOutcome     = "outcome"
	ActionRetry       = "retry"
	ActionCancel      = "cancel"
	ActionRecover     = "recover"
	ActionAbandon     = "abandon"
	ActionLateOutcome = "late_outcome"
)

const (
	auditMediaType    = "application/vnd.context-runtime.call-audit+json"
	responseMediaType = "application/octet-stream"
)

// Ledger is the call ledger over a store. It is safe for concurrent use;
// concurrency control comes from the store's per-session serialization and
// compare-and-swap revisions.
type Ledger struct {
	store store.Store
	now   func() time.Time
}

// Option configures a Ledger.
type Option func(*Ledger)

// WithClock sets the clock for attempt timestamps. Timestamps are
// observations only and never influence a decision (SDD section 10).
func WithClock(now func() time.Time) Option { return func(l *Ledger) { l.now = now } }

// New returns a ledger over s.
func New(s store.Store, opts ...Option) *Ledger {
	l := &Ledger{store: s, now: time.Now}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Call returns a call record. The actor must be a service actor of the
// call's session.
func (l *Ledger) Call(ctx context.Context, actor domain.Principal, callID string) (domain.CallRecord, error) {
	if err := checkServiceActor(actor); err != nil {
		return domain.CallRecord{}, err
	}
	var c domain.CallRecord
	err := l.store.View(ctx, actor.SessionID, func(tx store.ReadTx) error {
		var err error
		c, err = tx.Call(callID)
		return err
	})
	return c, err
}

// checkServiceActor enforces that ledger methods are driven by the trusted
// dispatcher, never by an agent (SDD section 8).
func checkServiceActor(p domain.Principal) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if p.Authority != domain.AuthoritySystem && p.Authority != domain.AuthorityHarness {
		return fmt.Errorf("service actor must be SYSTEM or HARNESS, got %s: %w", p.Authority, domain.ErrInvalidAuthorityPromotion)
	}
	return nil
}

// checkActorScope rejects a service actor whose non-empty owner fields do not
// match the call's inference principal, so a dispatcher scoped to one task or
// agent cannot drive another's conversation.
func checkActorScope(actor domain.Principal, c domain.CallRecord) error {
	p := c.Principal
	if actor.SessionID != c.SessionID ||
		(actor.WorkflowID != "" && actor.WorkflowID != p.WorkflowID) ||
		(actor.TaskID != "" && actor.TaskID != p.TaskID) ||
		(actor.AgentID != "" && actor.AgentID != p.AgentID) {
		return fmt.Errorf("service actor is not scoped to call %s: %w", c.CallID, domain.ErrInvalidAuthorityPromotion)
	}
	return nil
}

// loadCall reads a call and checks the actor may drive it.
func loadCall(tx store.ReadTx, actor domain.Principal, callID string) (domain.CallRecord, error) {
	c, err := tx.Call(callID)
	if err != nil {
		return c, err
	}
	return c, checkActorScope(actor, c)
}

// semanticStale reports whether a semantic change was committed after seq:
// some sequence number in (seq, LastSeq] is not a call lifecycle event.
func semanticStale(tx store.ReadTx, seq uint64) (bool, error) {
	last := tx.LastSeq()
	if seq > last {
		return true, nil
	}
	if seq == last {
		return false, nil
	}
	evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetCall, MinSeq: seq + 1})
	if err != nil {
		return false, err
	}
	ledgerSeqs := map[uint64]bool{}
	for _, e := range evs {
		if e.Seq <= last {
			ledgerSeqs[e.Seq] = true
		}
	}
	return uint64(len(ledgerSeqs)) != last-seq, nil
}

// transition applies a state change to c under compare-and-swap and appends
// its lifecycle event at seq. It returns the updated record.
func (l *Ledger) transition(tx store.Tx, c domain.CallRecord, to domain.CallState, seq uint64, ev domain.LifecycleEvent) (domain.CallRecord, error) {
	from := c.State
	if !domain.ValidCallTransition(from, to) {
		return c, fmt.Errorf("call %s: %s -> %s: %w", c.CallID, from, to, domain.ErrInvalidTransition)
	}
	next := c.Clone()
	next.State = to
	if to.Terminal() {
		next.FinishedSeq = seq
	}
	next, err := tx.UpdateCall(next, c.Revision)
	if err != nil {
		return c, err
	}
	ev.From, ev.To = string(from), string(to)
	if err := l.appendEvent(tx, next, seq, ev); err != nil {
		return c, err
	}
	return next, nil
}

// appendEvent writes a call lifecycle event at seq.
func (l *Ledger) appendEvent(tx store.Tx, c domain.CallRecord, seq uint64, ev domain.LifecycleEvent) error {
	ev.ID = lifecycleEventID(c.SessionID, seq)
	ev.SessionID = c.SessionID
	ev.Seq = seq
	ev.TargetKind = domain.TargetCall
	ev.TargetID = c.CallID
	return tx.AppendLifecycleEvent(ev)
}

// lifecycleEventID derives a call lifecycle event's ID from its session and
// sequence number, which identify it uniquely, so replay and a restarted
// process reproduce the same IDs (ADR 4, INV-09).
func lifecycleEventID(sessionID string, seq uint64) string {
	h := domain.NewCanonicalEncoder("context-runtime/call-lifecycle-id/v1").String(sessionID).Uint(seq).Hash()
	return "lce_" + strings.TrimPrefix(h, "sha256:")[:32]
}

// releaseReservation clears the conversation's in-flight call if it is c,
// applying adjust to the conversation first.
func releaseReservation(tx store.Tx, c domain.CallRecord, adjust func(*domain.Conversation)) error {
	conv, err := tx.Conversation(c.ConversationID)
	if err != nil {
		return err
	}
	if conv.InFlightCallID != c.CallID {
		return fmt.Errorf("conversation %s: reservation held by %q, not %s: %w",
			conv.ConversationID, conv.InFlightCallID, c.CallID, domain.ErrVersionConflict)
	}
	next := conv
	next.InFlightCallID = ""
	if adjust != nil {
		adjust(&next)
	}
	_, err = tx.PutConversation(next, conv.Revision)
	return err
}

// attempt returns attempt n of a call.
func attempt(tx store.ReadTx, callID string, n int) (domain.CallAttempt, error) {
	as, err := tx.CallAttempts(callID)
	if err != nil {
		return domain.CallAttempt{}, err
	}
	for _, a := range as {
		if a.Attempt == n {
			return a, nil
		}
	}
	return domain.CallAttempt{}, fmt.Errorf("call %s attempt %d: %w", callID, n, domain.ErrNotFound)
}

// putAudit stores v as a JSON audit blob and returns its hash.
func putAudit(tx store.Tx, v any) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	b := domain.Blob{SessionID: tx.SessionID(), Hash: domain.HashBytes(data), MediaType: auditMediaType, Data: data}
	return b.Hash, tx.InsertBlob(b)
}
