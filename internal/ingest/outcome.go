package ingest

import (
	"context"
	"strings"

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
// b in one transaction of b's session: the call's AGENT output under
// EventID domain.OutcomeEventID(b), or one external tool's TOOL result
// under ToolResultEventID(b, toolCall), with no typed operations. The whole
// binding is part of the request identity: the same outcome cannot be
// retried under another call, exchange, turn, or tool call.
//
// With m, a completed output also joins its logical exchange in the same
// transaction (P3-7; ruling: tool results are registered under the trusted
// dispatcher of the producing inference): the event's one AGENT transcript
// becomes the round's OUTPUT member and each of m.ToolCallIDs a TOOL_CALL
// member naming it, registered by m.Dispatcher, which must be the completed
// call's own service actor. A tool result becomes its tool call's
// TOOL_RESULT member the same way.
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
	if b.Validate() != nil || semanticShaped(e) {
		return domain.ErrInvalidRecord
	}
	id, err := domain.OutcomeEventID(b)
	if err != nil {
		return domain.ErrInvalidRecord
	}
	switch e.Kind {
	case domain.EventAgent:
		if e.EventID == id {
			return nil
		}
	case domain.EventTool:
		// An external tool's result: one TOOL span naming the tool call,
		// under that call's derived EventID.
		if len(e.Spans) == 1 && e.Spans[0].Authority == domain.AuthorityTool && e.Spans[0].Source != nil &&
			e.Spans[0].Source.ToolCallID != "" && e.EventID == ToolResultEventID(b, e.Spans[0].Source.ToolCallID) {
			return nil
		}
	}
	return domain.ErrInvalidRecord
}

// isOutcomeEventID reports whether id is in the outcome- EventID namespace,
// which only the outcome path may use (SEC-1.4).
func isOutcomeEventID(id string) bool { return strings.HasPrefix(id, "outcome-") }

// reservedEventID reports whether a new plain event may not claim id: the
// outcome- namespace or any runtime prefix (R20.1, G3, H5). A Phase 2 plain
// event under such an ID still replays to its own principal only (DUR-2.8,
// SEC-4.9); every other probe gets the uniform ErrInvalidRecord (SEC-3.3).
func reservedEventID(id string) bool { return isOutcomeEventID(id) || domain.ReservedIDPrefix(id) }

// checkOutcomeReplay requires a stored receipt under an outcome's EventID
// to be one this outcome produced (SEC-1.4, DUR-1.7): every item carries
// the binding's turn, and the receipt registered exactly o's membership —
// none without it; otherwise the output's OUTPUT member followed by one
// TOOL_CALL member per tool call ID in order, or the tool result's one
// TOOL_RESULT member, all for the binding's call. Membership is part of the
// request identity, so a retry with other membership is a bare conflict
// rather than a success for a round that never registered it. The
// dispatcher itself is the call's immutable service actor, checked before
// the lookup.
func checkOutcomeReplay(tx store.ReadTx, rc domain.IngestReceipt, e domain.Event, o outcome) error {
	b, m := o.binding, o.membership
	items := map[string]bool{}
	for _, it := range rc.Items {
		if it.CreatedTurn != b.Turn {
			return domain.ErrEventIDConflict
		}
		items[it.ID] = true
	}
	type want struct {
		role       domain.ExchangeMemberRole
		toolCallID string
	}
	var expect []want
	switch {
	case m == nil:
	case e.Kind == domain.EventTool:
		expect = []want{{domain.MemberToolResult, e.Spans[0].Source.ToolCallID}}
	default:
		expect = []want{{domain.MemberOutput, ""}}
		for _, id := range m.ToolCallIDs {
			expect = append(expect, want{domain.MemberToolCall, id})
		}
	}
	if len(rc.MutationReceiptIDs) != len(expect) {
		return domain.ErrEventIDConflict
	}
	if len(expect) == 0 {
		return nil
	}
	if rc.Versions.Semantic == nil {
		return domain.ErrEventIDConflict
	}
	sem, err := store.ReadSemantic(tx)
	if err != nil {
		return err
	}
	var got []domain.ExchangeMember
	page := store.Page{Limit: rc.Versions.Semantic.MaxPageSize}
	for {
		res, err := sem.ExchangeMembers(b.ExchangeID, page)
		if err != nil {
			return err
		}
		for _, x := range res.Records {
			if items[x.Source.ItemID] {
				got = append(got, x)
			}
		}
		if !res.More {
			break
		}
		page.After = res.Next
	}
	if len(got) != len(expect) {
		return domain.ErrEventIDConflict
	}
	for i, x := range got {
		if x.Role != expect[i].role || x.ToolCallID != expect[i].toolCallID || x.CallID != b.CallID {
			return domain.ErrEventIDConflict
		}
	}
	return nil
}

// ToolResultEventID is the EventID of the external tool result for
// toolCallID of the output bound by b: the output's OutcomeEventID, a
// slash, and the tool call ID. The fixed-length prefix makes it
// unambiguous, and it binds the result to exactly one call of one output.
// It is empty for an invalid binding.
func ToolResultEventID(b domain.OutcomeBinding, toolCallID string) string {
	id, err := domain.OutcomeEventID(b)
	if err != nil {
		return ""
	}
	return id + "/" + toolCallID
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
	if err := r.checkDispatcher(m); err != nil {
		return err
	}
	if r.e.Kind == domain.EventTool {
		return r.registerToolResult(m)
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
		req, err := domain.OperationRequestID(r.p, m.Dispatcher, r.occurrence, r.seq, uint64(len(r.e.Spans)+len(r.e.Operations)+i), 1<<32)
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
		id, err := domain.MutationReceiptID(r.tx, m.Dispatcher, domain.MutationMembership, req)
		if err != nil {
			return err
		}
		r.mutationReceipts = append(r.mutationReceipts, id)
	}
	return nil
}

// checkDispatcher requires the binding's call to be COMPLETED and
// dispatched by exactly m.Dispatcher, a trusted SYSTEM or HARNESS actor.
func (r *run) checkDispatcher(m OutcomeMembership) error {
	return checkDispatcher(r.tx, *r.binding, m)
}

func checkDispatcher(tx store.ReadTx, b domain.OutcomeBinding, m OutcomeMembership) error {
	call, err := tx.Call(b.CallID)
	if err != nil {
		return err
	}
	if call.State != domain.CallCompleted || call.Principal != b.Principal || call.ServiceActor != m.Dispatcher ||
		call.ConversationID != b.ConversationID || !m.Dispatcher.Authority.CanHoldLifecycleAuthority() || m.Dispatcher.Authority == domain.AuthorityUser {
		return domain.ErrInvalidAuthorityPromotion
	}
	return nil
}

// registerToolResult makes an external tool result the TOOL_RESULT member
// of the tool call it names in the binding's exchange (P3-7), as the
// call's dispatcher; the membership service requires that call to have
// been registered by the round's output.
func (r *run) registerToolResult(m OutcomeMembership) error {
	b := r.binding
	if err := r.checkDispatcher(m); err != nil {
		return err
	}
	if len(m.ToolCallIDs) != 0 || len(r.e.Spans) != 1 {
		return domain.ErrInvalidRecord
	}
	result, ok := r.transcripts[0]
	if !ok || result.Kind != domain.KindToolResult {
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
	req, err := domain.OperationRequestID(r.p, m.Dispatcher, r.occurrence, r.seq, 1, 1<<32)
	if err != nil {
		return err
	}
	in := domain.RegisterExchangeMemberIntent{RequestID: req, ExchangeID: x.ID, ExpectedRevision: x.Revision, Position: uint64(len(members.Records)) + 1,
		Role: domain.MemberToolResult, Source: domain.ItemContentRef{ItemID: result.ID, ContentHash: result.ContentHash}, CallID: b.CallID,
		ToolCallID: r.e.Spans[0].Source.ToolCallID}
	if _, err := svc.RegisterExchangeMember(r.tx, m.Dispatcher, in, r.tx.NextSeq()); err != nil {
		return err
	}
	id, err := domain.MutationReceiptID(r.tx, m.Dispatcher, domain.MutationMembership, req)
	if err != nil {
		return err
	}
	r.mutationReceipts = append(r.mutationReceipts, id)
	return nil
}
