package ingest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/tools"
)

// T17 through the real pipeline (P3-24..26): ingest records the user's pin
// and goal; the call ledger completes one inference; ingest records the
// completed output and registers it and its tool calls as exchange members
// under the call's dispatcher; each tool call then runs W5's handler in one
// transaction as that dispatcher (ruling: tool results are registered under
// the trusted dispatcher of the producing inference).

func textParts(s string) []domain.ContentPart {
	return []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: s}}
}

// TestGateT17_AgentToolsThroughPipeline is T17's state half.
func TestGateT17_AgentToolsThroughPipeline(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		needsObligations(t, f)
		user := principal(domain.AuthorityUser)
		setup := f.mustIngest(user, t17Setup())
		p1, g1 := mustDirective(t, setup, "P1"), mustDirective(t, setup, "G1")
		o1 := f.currentObligation(p1.ID)

		// An item of another session, for the invalid citation.
		other := principal(domain.AuthorityUser)
		other.SessionID = "S2"
		oe := userEvent("s2", "Another session's note.", false)
		oe.Spans[0].Access.SessionID = "S2"
		foreign, err := f.in.Ingest(ctx, f.s, other, oe)
		if err != nil {
			t.Fatal(err)
		}

		agent := agentPrincipal()
		dispatcher := dispatcherFor(agent)
		b := f.inference(agent, "r1")
		calls := []string{"state-1", "state-2", "remember", "resolve"}
		out, err := f.in.IngestOutcome(ctx, f.s, b, outcomeEvent(b, "Updating state, noting the API, and claiming G1."), &OutcomeMembership{Dispatcher: dispatcher, ToolCallIDs: calls})
		if err != nil {
			t.Fatal(err)
		}
		_ = out

		svc, err := tools.NewService(testPolicy())
		if err != nil {
			t.Fatal(err)
		}
		invocation := func(call string) domain.ToolInvocation {
			return domain.ToolInvocation{SessionID: sess, Principal: agent, ConversationID: b.ConversationID, ExchangeID: b.ExchangeID, CallID: b.CallID, ToolCallID: call, TurnID: b.TurnID}
		}
		updateState := func(call, text string) (domain.ToolResult, error) {
			return tools.Execute(ctx, f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
				return svc.UpdateState(tx, dispatcher, tools.Request[domain.KeyedWriteIntent]{Invocation: invocation(call),
					Intent: domain.KeyedWriteIntent{RequestID: "req-" + call, Key: "status", Kind: domain.KindTaskState, Parts: textParts(text)}}, seq)
			})
		}

		// context_update_state("status", ...) twice: one current version,
		// the second superseding the first.
		first, err := updateState("state-1", "Working on the API.")
		if err != nil {
			t.Fatalf("update_state 1: %v", err)
		}
		second, err := updateState("state-2", "API done; running tests.")
		if err != nil {
			t.Fatalf("update_state 2: %v", err)
		}
		if first.Keyed == nil || second.Keyed == nil || second.Keyed.Duplicate || second.Keyed.SupersededItemID != first.Keyed.ItemID ||
			!f.isCurrent(second.Keyed.ItemID) || f.isCurrent(first.Keyed.ItemID) {
			t.Fatalf("agent.status versions: %+v then %+v", first.Keyed, second.Keyed)
		}

		// Agent B's own "status" key is independent: B's first write
		// supersedes nothing and A's version stays current (P3-25).
		agentB := agent
		agentB.AgentID = "B"
		bb := f.inference(agentB, "rB")
		if _, err := f.in.IngestOutcome(ctx, f.s, bb, outcomeEvent(bb, "B updates its state."), &OutcomeMembership{Dispatcher: dispatcherFor(agentB), ToolCallIDs: []string{"b-state"}}); err != nil {
			t.Fatal(err)
		}
		bState, err := tools.Execute(ctx, f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
			inv := domain.ToolInvocation{SessionID: sess, Principal: agentB, ConversationID: bb.ConversationID, ExchangeID: bb.ExchangeID, CallID: bb.CallID, ToolCallID: "b-state", TurnID: bb.TurnID}
			return svc.UpdateState(tx, dispatcherFor(agentB), tools.Request[domain.KeyedWriteIntent]{Invocation: inv,
				Intent: domain.KeyedWriteIntent{RequestID: "req-b-state", Key: "status", Kind: domain.KindTaskState, Parts: textParts("B is idle.")}}, seq)
		})
		if err != nil || bState.Keyed == nil || bState.Keyed.SupersededItemID != "" || !f.isCurrent(second.Keyed.ItemID) || !f.isCurrent(bState.Keyed.ItemID) {
			t.Fatalf("agent B's key interfered with A's: %+v (%v)", bState.Keyed, err)
		}
		if a := f.item(second.Keyed.ItemID); a.Access.Permits(agentB) {
			t.Fatalf("agent B can read A's keyed state: %+v", a.Access)
		}

		// context_remember citing another session's item fails and writes
		// nothing.
		f.requireAtomic(domain.ErrNotFound, func() error {
			_, err := tools.Execute(ctx, f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
				return svc.Remember(tx, dispatcher, tools.Request[domain.KeyedWriteIntent]{Invocation: invocation("remember"),
					Intent: domain.KeyedWriteIntent{RequestID: "req-remember", Key: "api-note", Kind: domain.KindFact, Parts: textParts("The API uses v3."),
						EvidenceIDs: []string{foreign.Items[0].ID}}}, seq)
			})
			return notFoundOrFixed(err)
		})

		// context_resolve(G1) citing evidence records a claim; G1 and O1
		// are untouched. The cited evidence is a semantic item the agent
		// can see: W1's graph refuses any transcript, including a TOOL
		// result like T17's X9, as evidence support (ruling requested; see
		// TestGateT17_ToolResultAsEvidence).
		x9 := second.Keyed.ItemID
		claim, err := tools.Execute(ctx, f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
			return svc.RecordCompletionClaim(tx, dispatcher, tools.Request[domain.CompletionClaimIntent]{Invocation: invocation("resolve"),
				Intent: domain.CompletionClaimIntent{RequestID: "req-resolve", GoalItemID: g1.ID, EvidenceIDs: []string{x9}}}, seq)
		})
		if err != nil {
			t.Fatalf("context_resolve: %v", err)
		}
		if claim.Claim == nil || claim.Claim.GoalStatus != domain.GoalOpen || claim.Claim.Currentness != domain.ItemCurrent || claim.Claim.TargetItemID != g1.ID {
			t.Fatalf("claim result = %+v", claim.Claim)
		}
		if g := f.item(g1.ID); *g.GoalStatus != domain.GoalOpen || g.Version != g1.Version {
			t.Fatalf("G1 changed: %+v", g)
		}
		if o := f.currentObligation(p1.ID); o.Status != domain.ObligationUnresolved || o.Revision != o1.Revision {
			t.Fatalf("O1 changed: %+v", o)
		}
		f.view(func(tx store.ReadTx) error {
			refs, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelReferences, FromID: claim.Claim.ClaimItemID, ToID: g1.ID})
			if err != nil || len(refs) != 1 {
				t.Errorf("claim REFERENCES G1: %+v (%v)", refs, err)
			}
			// Nothing a tool wrote is pinned, a goal, an obligation
			// source, or above AGENT authority.
			items, err := tx.Items(store.ItemFilter{})
			if err != nil {
				return err
			}
			for _, it := range items {
				if it.Authority != domain.AuthorityAgent || it.Role == domain.RoleTranscript {
					continue
				}
				obs, err := tx.ObligationsBySource(it.ID, 10)
				if it.IsPinned() || it.Kind == domain.KindGoal || err != nil || len(obs) != 0 {
					t.Errorf("tool-written item became a requirement: %+v (%d obligations, %v)", it, len(obs), err)
				}
			}
			if ok, err := graph.IsCurrent(tx, p1.ID); err != nil || !ok {
				t.Errorf("P1 no longer current: %v", err)
			}
			return nil
		})
	})
}

// notFoundOrFixed maps the tools package's fixed public error for a
// missing or inaccessible citation to domain.ErrNotFound for requireAtomic;
// any other error passes through unchanged.
func notFoundOrFixed(err error) error {
	if err == nil {
		return nil
	}
	var te *tools.Error
	if errors.As(err, &te) && te.Code() == domain.ToolErrorNotFound {
		return domain.ErrNotFound
	}
	return err
}

// TestGateT17_ToolResultAsEvidence: T16/T17 cite tool exchange results (X3,
// X9) as evidence, but W1's graph refuses every transcript, TOOL results
// included, as EVIDENCE_SUPPORT coverage, so a context_resolve or
// context_remember citing a raw tool result fails INVALID_ARGUMENT.
func TestGateT17_ToolResultAsEvidence(t *testing.T) {
	pending(t, "ruling: may TOOL-authority result transcripts qualify as evidence support (P3-25/C-19)? graph/coverage_plan.go rejects every transcript")
}
