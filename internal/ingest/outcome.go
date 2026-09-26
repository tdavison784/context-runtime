package ingest

import (
	"context"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Provider outcomes (P3-34). The call ledger ingests what a provider
// returned with the immutable context the call was issued in: its
// principal, task turn, conversation, exchange, and call. The outcome is
// stamped with that turn even when the task has since opened a newer one,
// opens no turn, and never reactivates a completed task: a late outcome
// for one is audit history, which task-lifecycle eligibility keeps out of
// current context.

// IngestOutcome ingests e, a provider outcome, for the originating context
// b in one transaction of b's session. e must be an AGENT or TOOL event
// with no typed operations, under EventID domain.OutcomeEventID(b), so the
// whole binding is part of the request identity: the same outcome cannot
// be retried under another call, exchange, or turn.
//
// With m, the completed output also joins its logical exchange in the same
// transaction (P3-7; ruling: tool results are registered under the trusted
// dispatcher of the producing inference): the event's one AGENT transcript
// becomes the round's OUTPUT member and each of m.ToolCallIDs a TOOL_CALL
// member naming it, registered by m.Dispatcher, which must be the completed
// call's own service actor.
func (g Ingester) IngestOutcome(ctx context.Context, s store.Store, b domain.OutcomeBinding, e domain.Event, m *OutcomeMembership) (domain.IngestReceipt, error) {
	if err := checkOutcome(b, e); err != nil {
		return domain.IngestReceipt{}, err
	}
	r, err := g.ingest(ctx, s, b.Principal, e, &outcome{b, m})
	return r, sanitize(err)
}

// OutcomeMembership is how a completed provider output joins its logical
// exchange: the trusted dispatcher that executed the call, and the tool
// calls the output issued, in order.
type OutcomeMembership struct {
	Dispatcher  domain.Principal
	ToolCallIDs []string
}

// outcome is an outcome event's originating context.
type outcome struct {
	binding    domain.OutcomeBinding
	membership *OutcomeMembership
}

// ApplyOutcome is IngestOutcome inside the caller's transaction, such as
// the ledger's outcome transaction (M3).
func (g Ingester) ApplyOutcome(tx store.Tx, b domain.OutcomeBinding, e domain.Event, m *OutcomeMembership) (domain.IngestReceipt, error) {
	r, err := g.apply(tx, b.Principal, e, "", &outcome{b, m})
	return r, sanitize(err)
}

// checkOutcome rejects, before anything is read or written, an outcome
// whose binding is incomplete or whose event could carry more than
// provider output.
func checkOutcome(b domain.OutcomeBinding, e domain.Event) error {
	if b.Validate() != nil {
		return domain.ErrInvalidRecord
	}
	id, err := domain.OutcomeEventID(b)
	if err != nil || e.Kind != domain.EventAgent && e.Kind != domain.EventTool || semanticShaped(e) || e.EventID != id {
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

// registerOutput makes the outcome's AGENT transcript the exchange's OUTPUT
// member and registers each tool call it issued, as the call's dispatcher
// (P3-7). The call must be the binding's, COMPLETED, and dispatched by
// exactly m.Dispatcher; the membership service checks that the dispatcher
// controls the exchange and the round's association rules.
func (r *run) registerOutput(m OutcomeMembership) error {
	b := r.binding
	call, err := r.tx.Call(b.CallID)
	if err != nil {
		return err
	}
	if call.State != domain.CallCompleted || call.Principal != b.Principal || call.ServiceActor != m.Dispatcher ||
		call.ConversationID != b.ConversationID || !m.Dispatcher.Authority.CanHoldLifecycleAuthority() || m.Dispatcher.Authority == domain.AuthorityUser {
		return domain.ErrInvalidAuthorityPromotion
	}
	var output *domain.ContextItem
	for si := range r.e.Spans {
		if it, ok := r.transcripts[si]; ok && it.Authority == domain.AuthorityAgent {
			if output != nil {
				return domain.ErrInvalidRecord // one completed output per round
			}
			output = &it
		}
	}
	if output == nil {
		return domain.ErrInvalidRecord
	}
	svc, err := graph.NewMembershipService(*r.pol)
	if err != nil {
		return err
	}
	sem, err := store.Semantic(r.tx)
	if err != nil {
		return err
	}
	source := domain.ItemContentRef{ItemID: output.ID, ContentHash: output.ContentHash}
	for i := range len(m.ToolCallIDs) + 1 {
		x, err := sem.LogicalExchange(b.ExchangeID)
		if err != nil {
			return err
		}
		members, err := sem.ExchangeMembers(x.ID, store.Page{Limit: r.pol.MaxPageSize})
		if err != nil {
			return err
		}
		if members.More {
			return store.ErrLimitExceeded
		}
		// Membership receipts are keyed past the event's operation and
		// command request IDs.
		req, err := domain.OperationRequestID(r.p.SessionID, r.occurrence, uint64(len(r.e.Spans)+len(r.e.Operations)+i), 1<<32)
		if err != nil {
			return err
		}
		in := domain.RegisterExchangeMemberIntent{RequestID: req, ExchangeID: x.ID, ExpectedRevision: x.Revision, Position: uint64(len(members.Records)) + 1,
			Role: domain.MemberOutput, Source: source, CallID: b.CallID}
		if i > 0 {
			in.Role, in.ToolCallID = domain.MemberToolCall, m.ToolCallIDs[i-1]
		}
		if _, err := svc.RegisterExchangeMember(r.tx, m.Dispatcher, in, r.tx.NextSeq()); err != nil {
			return err
		}
		id, err := domain.MutationReceiptID(r.p.SessionID, domain.MutationMembership, req)
		if err != nil {
			return err
		}
		r.mutationReceipts = append(r.mutationReceipts, id)
	}
	return nil
}
