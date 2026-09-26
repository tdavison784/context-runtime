package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// nextRound registers the round after prev, whose tool calls must all have
// results. Its producing inference "producing-n" consumed prev under a
// GENERATION_INPUT manifest of the given sources plus prev's own members;
// with ack, that manifest closes prev. It returns the new round's first
// tool-call invocation and the manifest ID.
func nextRound(t *testing.T, st store.Store, prev domain.ToolInvocation, n string, ack bool, extra ...string) (domain.ToolInvocation, string) {
	t.Helper()
	i := prev
	var manifestID string
	update(t, st, func(tx store.Tx) error {
		membership, _ := graph.NewMembershipService(testPolicy())
		sem, _ := store.Semantic(tx)
		p := prev.Principal
		actor := p
		actor.Authority = domain.AuthorityHarness
		task, _ := tx.Task(p.TaskID)
		state, _ := sem.ConversationMembership(prev.ConversationID)
		x, err := membership.RegisterExchange(tx, actor, domain.RegisterExchangeIntent{RequestID: "exchange-" + n, Principal: p, TurnID: task.TurnID, Turn: task.Turn, ExpectedMembershipRevision: state.Revision}, tx.NextSeq())
		if err != nil {
			return err
		}
		i.ExchangeID, i.CallID, i.ToolCallID, i.TurnID = x.IDs[0], "producing-"+n, "tool-"+n, task.TurnID
		call := storetest.NewCall("s", i.CallID, i.ConversationID, tx.NextSeq())
		call.Principal, call.ServiceActor = p, actor
		call = storetest.Reseal(call)
		if err = tx.InsertCall(call); err != nil {
			return err
		}
		attempt := storetest.NewAttempt("s", call.CallID, 1, tx.NextSeq())
		if err = tx.PutCallAttempt(attempt); err != nil {
			return err
		}
		call.State, call.Attempts = domain.CallSent, 1
		if call, err = tx.UpdateCall(call, call.Revision); err != nil {
			return err
		}
		call = storetest.Finish(call, domain.CallCompleted, tx.NextSeq())
		if err = tx.PutCallAttempt(storetest.CloseAttempt(attempt, domain.AttemptCompleted, call.OutcomeHash, call.FinishedSeq)); err != nil {
			return err
		}
		if _, err = tx.UpdateCall(call, call.Revision); err != nil {
			return err
		}
		members, err := sem.ExchangeMembers(prev.ExchangeID, store.Page{Limit: 64})
		if err != nil {
			return err
		}
		var sources []domain.ItemContentRef
		for _, m := range members.Records {
			sources = append(sources, m.Source)
		}
		for _, id := range extra {
			it, err := tx.Item(id)
			if err != nil {
				return err
			}
			sources = append(sources, storetest.ContentRef(it))
		}
		coverage, err := membership.RecordAdmissionCoverage(tx, actor, p, "generation-"+n, dedupe(sources))
		if err != nil {
			return err
		}
		state, _ = sem.ConversationMembership(prev.ConversationID)
		admitted, err := membership.AdmitExchange(tx, actor, domain.AdmitExchangeIntent{RequestID: "admit-" + n, ExchangeID: prev.ExchangeID, CoverageID: coverage.ID, CallID: i.CallID, Purpose: domain.AdmissionGenerationInput, ExpectedMembershipRevision: state.Revision}, tx.NextSeq())
		if err != nil {
			return err
		}
		manifestID = admitted.IDs[0]
		if ack {
			px, _ := sem.LogicalExchange(prev.ExchangeID)
			if _, err = membership.AcknowledgeExchange(tx, actor, domain.AcknowledgeExchangeIntent{RequestID: "ack-" + n, ExchangeID: px.ID, ManifestID: manifestID, ConsumingCallID: i.CallID, ExpectedRevision: px.Revision}, tx.NextSeq()); err != nil {
				return err
			}
		}
		output := storetest.NewItem("s", "output-"+n, tx.NextSeq(), "assistant output "+n)
		output.Authority, output.CreatedTurn, output.Role, output.AgentID = domain.AuthorityAgent, task.Turn, domain.RoleTranscript, p.AgentID
		output.Scope, output.Access = domain.ScopeTask, conversationBoundary(p)
		if err = tx.InsertItem(output); err != nil {
			return err
		}
		member := domain.RegisterExchangeMemberIntent{RequestID: "output-" + n, ExchangeID: i.ExchangeID, ExpectedRevision: 1, Position: 1, Role: domain.MemberOutput, Source: storetest.ContentRef(output), CallID: i.CallID}
		if _, err = membership.RegisterExchangeMember(tx, actor, member, tx.NextSeq()); err != nil {
			return err
		}
		member.RequestID, member.ExpectedRevision, member.Position, member.Role, member.ToolCallID = "call-"+n, 2, 2, domain.MemberToolCall, i.ToolCallID
		_, err = membership.RegisterExchangeMember(tx, actor, member, tx.NextSeq())
		return err
	})
	return i, manifestID
}

func dedupe(refs []domain.ItemContentRef) []domain.ItemContentRef {
	seen := map[domain.ItemContentRef]bool{}
	var out []domain.ItemContentRef
	for _, r := range refs {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	return out
}
