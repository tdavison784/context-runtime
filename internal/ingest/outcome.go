package ingest

import (
	"context"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Provider outcomes (P3-34). The call ledger ingests what a provider
// returned with the immutable context the call was issued in: its
// principal, task turn, conversation, exchange, and call. The outcome is
// stamped with that turn even when the task has since opened a newer one,
// opens no turn, and never reactivates a completed task: a late outcome
// for one is audit history, which task-lifecycle eligibility keeps out of
// current context.

// outcomeIDPrefix marks derived outcome EventIDs; it is not a reserved
// internal ID prefix.
const outcomeIDPrefix = "outcome-"

// OutcomeEventID is the EventID of the outcome bound by b. Deriving it from
// every binding field makes the binding part of the request identity: the
// same outcome cannot be retried under another call, exchange, or turn.
func OutcomeEventID(b domain.OutcomeBinding) string {
	c := domain.NewCanonicalEncoder("context-runtime/ingest/outcome-event-id/v1")
	c.String(b.Principal.SessionID).String(b.Principal.WorkflowID).String(b.Principal.TaskID).String(b.Principal.AgentID).String(string(b.Principal.Authority))
	c.String(b.ConversationID).String(b.ExchangeID).String(b.CallID).String(b.TurnID).Uint(b.Turn)
	h := c.Hash()
	return outcomeIDPrefix + h[strings.IndexByte(h, ':')+1:]
}

// IngestOutcome ingests e, a provider outcome, for the originating context
// b in one transaction of b's session. e must be an AGENT or TOOL event
// with no typed operations, under EventID OutcomeEventID(b).
func (g Ingester) IngestOutcome(ctx context.Context, s store.Store, b domain.OutcomeBinding, e domain.Event) (domain.IngestReceipt, error) {
	if err := checkOutcome(b, e); err != nil {
		return domain.IngestReceipt{}, err
	}
	r, err := g.ingest(ctx, s, b.Principal, e, &b)
	return r, sanitize(err)
}

// ApplyOutcome is IngestOutcome inside the caller's transaction, such as
// the ledger's outcome transaction (M3).
func (g Ingester) ApplyOutcome(tx store.Tx, b domain.OutcomeBinding, e domain.Event) (domain.IngestReceipt, error) {
	r, err := g.apply(tx, b.Principal, e, "", &b)
	return r, sanitize(err)
}

// checkOutcome rejects, before anything is read or written, an outcome
// whose binding is incomplete or whose event could carry more than
// provider output.
func checkOutcome(b domain.OutcomeBinding, e domain.Event) error {
	if b.Validate() != nil {
		return domain.ErrInvalidRecord
	}
	if e.Kind != domain.EventAgent && e.Kind != domain.EventTool || semanticShaped(e) || e.EventID != OutcomeEventID(b) {
		return domain.ErrInvalidRecord
	}
	return nil
}

// outcomeTask checks the outcome's task without advancing it: the task
// must exist in the binding's workflow and have opened the bound turn. A
// completed task is not reactivated.
func (r *run) outcomeTask() error {
	t, err := r.tx.Task(r.p.TaskID)
	if err != nil {
		return err
	}
	if t.WorkflowID != r.p.WorkflowID {
		return domain.ErrInvalidAuthorityPromotion
	}
	if r.binding.Turn > t.Turn || r.binding.TurnID != domain.DerivedTurnID(r.p.SessionID, r.p.TaskID, r.binding.Turn) {
		return domain.ErrInvalidRecord
	}
	r.task, r.hasTask = t, true
	return nil
}
