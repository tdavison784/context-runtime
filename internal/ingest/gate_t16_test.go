package ingest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/invocation"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/tools"
)

// T16's state half through the full pipeline (P3-7/27, C-12/13): each round
// the dispatcher registers the exchange, the call ledger completes the
// inference, the dispatcher records the previous exchange as that
// inference's generation input and acknowledges it (closing it), ingest
// records the output with its tool calls, and W5's handler executes the
// tool. A checkpoint issued in the last round covers exactly the closed
// prefix before it.

type t16 struct {
	f          *fixture
	agent      domain.Principal
	dispatcher domain.Principal
	membership *graph.MembershipService
	svc        *tools.Service
	ledger     *invocation.Ledger
	conv       string
	prev       *domain.OutcomeBinding
	exchanges  []string
}

func newT16(t *testing.T, f *fixture) *t16 {
	agent := agentPrincipal()
	membership, err := graph.NewMembershipService(testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	svc, err := tools.NewService(testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	f.mustIngest(principal(domain.AuthorityUser), userEvent("t16-q", "Build the feature.", false))
	return &t16{f: f, agent: agent, dispatcher: dispatcherFor(agent), membership: membership, svc: svc, ledger: invocation.New(f.s),
		conv: domain.ConversationIDFor(agent.TaskID, agent.AgentID)}
}

func (c *t16) update(fn func(tx store.Tx, sem store.SemanticTx) error) {
	c.f.t.Helper()
	if err := c.f.s.Update(ctx, sess, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		return fn(tx, sem)
	}); err != nil {
		c.f.t.Fatal(err)
	}
}

// round runs round n with one tool call, returning its binding and the
// generation manifest recording the previous exchange as its input.
func (c *t16) round(n int) (domain.OutcomeBinding, string) {
	c.f.t.Helper()
	task := c.f.task()
	var exchangeID string
	c.update(func(tx store.Tx, sem store.SemanticTx) error {
		var rev uint64
		if st, err := sem.ConversationMembership(c.conv); err == nil {
			rev = st.Revision
		}
		x, err := c.membership.RegisterExchange(tx, c.dispatcher, domain.RegisterExchangeIntent{RequestID: fmt.Sprintf("x%d", n), Principal: c.agent, TurnID: task.TurnID, Turn: task.Turn, ExpectedMembershipRevision: rev}, tx.NextSeq())
		if err == nil {
			exchangeID = x.IDs[0]
		}
		return err
	})
	c.exchanges = append(c.exchanges, exchangeID)
	base := uint64(1)
	c.f.view(func(tx store.ReadTx) error {
		if conv, err := tx.Conversation(c.conv); err == nil {
			base = conv.Version
		}
		return nil
	})
	req := fmt.Sprintf("round-%d", n)
	call, err := c.ledger.Prepare(ctx, invocation.PrepareRequest{Principal: c.agent, ServiceActor: c.dispatcher, Operation: domain.OperationInference,
		BaseConversationVersion: base, SemanticSeq: c.f.lastSeq(), Epoch: 1, PolicyVersion: "policy-1", DescriptorVersion: "desc-1",
		Request: []byte(req), ManifestHash: domain.HashBytes([]byte("manifest " + req))})
	if err != nil {
		c.f.t.Fatalf("round %d prepare: %v", n, err)
	}
	if _, err := c.ledger.MarkSent(ctx, c.dispatcher, call.CallID, "provider-"+req); err != nil {
		c.f.t.Fatalf("round %d send: %v", n, err)
	}
	resp := "response " + req
	if _, err := c.ledger.RecordOutcome(ctx, c.dispatcher, call.CallID, domain.CallOutcome{Attempt: 1, State: domain.CallCompleted, Response: []byte(resp), ResponseHash: domain.HashBytes([]byte(resp))}); err != nil {
		c.f.t.Fatalf("round %d outcome: %v", n, err)
	}
	var manifest string
	if c.prev != nil {
		prev := *c.prev
		c.update(func(tx store.Tx, sem store.SemanticTx) error {
			members, err := sem.ExchangeMembers(prev.ExchangeID, store.Page{Limit: 64})
			if err != nil {
				return err
			}
			var sources []domain.ItemContentRef
			for _, m := range members.Records {
				if !contains(sources, m.Source) {
					sources = append(sources, m.Source)
				}
			}
			coverage, err := c.membership.RecordAdmissionCoverage(tx, c.dispatcher, c.agent, fmt.Sprintf("gen-%d", n), sources)
			if err != nil {
				return err
			}
			st, err := sem.ConversationMembership(c.conv)
			if err != nil {
				return err
			}
			admitted, err := c.membership.AdmitExchange(tx, c.dispatcher, domain.AdmitExchangeIntent{RequestID: fmt.Sprintf("admit-%d", n), ExchangeID: prev.ExchangeID,
				CoverageID: coverage.ID, CallID: call.CallID, Purpose: domain.AdmissionGenerationInput, ExpectedMembershipRevision: st.Revision}, tx.NextSeq())
			if err != nil {
				return err
			}
			manifest = admitted.IDs[0]
			px, err := sem.LogicalExchange(prev.ExchangeID)
			if err != nil {
				return err
			}
			_, err = c.membership.AcknowledgeExchange(tx, c.dispatcher, domain.AcknowledgeExchangeIntent{RequestID: fmt.Sprintf("ack-%d", n), ExchangeID: px.ID,
				ManifestID: manifest, ConsumingCallID: call.CallID, ExpectedRevision: px.Revision}, tx.NextSeq())
			return err
		})
	}
	b := domain.OutcomeBinding{Principal: c.agent, TurnID: task.TurnID, Turn: task.Turn, ExchangeID: exchangeID, ConversationID: c.conv, CallID: call.CallID}
	if _, err := c.f.in.IngestOutcome(ctx, c.f.s, b, outcomeEvent(b, fmt.Sprintf("Round %d output.", n)), &OutcomeMembership{Dispatcher: c.dispatcher, ToolCallIDs: []string{"tool"}}); err != nil {
		c.f.t.Fatalf("round %d outcome ingest: %v", n, err)
	}
	c.prev = &b
	return b, manifest
}

func contains(refs []domain.ItemContentRef, r domain.ItemContentRef) bool {
	for _, x := range refs {
		if x == r {
			return true
		}
	}
	return false
}

func (c *t16) invocation(b domain.OutcomeBinding) domain.ToolInvocation {
	return domain.ToolInvocation{SessionID: sess, Principal: c.agent, ConversationID: b.ConversationID, ExchangeID: b.ExchangeID, CallID: b.CallID, ToolCallID: "tool", TurnID: b.TurnID}
}

// toolResult is the TOOL result transcript registered for b's tool call.
func (c *t16) toolResult(b domain.OutcomeBinding) string {
	c.f.t.Helper()
	_, ms := c.f.members(b.ExchangeID)
	for _, m := range ms {
		if m.Role == domain.MemberToolResult {
			return m.Source.ItemID
		}
	}
	c.f.t.Fatalf("no tool result in %s", b.ExchangeID)
	return ""
}

func (c *t16) keyed(b domain.OutcomeBinding, remember bool, key, text string, evidence ...string) domain.ToolResult {
	c.f.t.Helper()
	res, err := tools.Execute(ctx, c.f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
		r := tools.Request[domain.KeyedWriteIntent]{Invocation: c.invocation(b), Intent: domain.KeyedWriteIntent{RequestID: "req-" + b.CallID,
			Key: key, Kind: domain.KindTaskState, Parts: textParts(text), EvidenceIDs: evidence}}
		if remember {
			r.Intent.Kind = domain.KindFact
			return c.svc.Remember(tx, c.dispatcher, r, seq)
		}
		return c.svc.UpdateState(tx, c.dispatcher, r, seq)
	})
	if err != nil {
		c.f.t.Fatalf("keyed write %s: %v", key, err)
	}
	return res
}

// TestGateT16_CheckpointThroughPipeline is T16's state half.
func TestGateT16_CheckpointThroughPipeline(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		needsObligations(t, f)
		c := newT16(t, f)
		x1, _ := c.round(1)
		c.keyed(x1, false, "progress", "Step 1 done.")
		x2, _ := c.round(2)
		f1 := c.keyed(x2, true, "db", "The service uses postgres.", c.toolResult(x1))
		x3, _ := c.round(3)
		c.keyed(x3, false, "progress", "Step 3 done.")
		x4, _ := c.round(4)
		f2 := c.keyed(x4, true, "api", "The API is versioned.", c.toolResult(x3))
		x5, manifest := c.round(5)

		// F1 and F2 are current AGENT facts supported by the TOOL results
		// they cite (TOOL-result evidence support, ruling).
		for fact, src := range map[string]string{f1.Keyed.ItemID: c.toolResult(x1), f2.Keyed.ItemID: c.toolResult(x3)} {
			if !f.isCurrent(fact) {
				t.Fatalf("fact %s not current", fact)
			}
			f.view(func(tx store.ReadTx) error {
				rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: fact, ToID: src})
				if err != nil || len(rels) != 1 {
					t.Errorf("fact %s DERIVED_FROM its tool evidence: %+v (%v)", fact, rels, err)
				}
				return nil
			})
		}

		// An oversized checkpoint is rejected atomically (TOO_LARGE).
		big := strings.Repeat("x", testPolicy().MaxCheckpointSemanticBytes+1)
		before := f.lastSeq()
		_, err := tools.Execute(ctx, f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
			return c.svc.CreateCheckpoint(tx, c.dispatcher, tools.Request[domain.CheckpointIntent]{Invocation: c.invocation(x5),
				Intent: domain.CheckpointIntent{RequestID: "k-big", GenerationManifestID: manifest, Parts: textParts(big)}}, seq)
		})
		var te *tools.Error
		if err == nil || !asToolError(err, &te) || te.Code() != domain.ToolErrorTooLarge || f.lastSeq() != before {
			t.Fatalf("oversized checkpoint: %v, seq %d -> %d", err, before, f.lastSeq())
		}

		// K1 covers the closed prefix X1-X4 and excludes its issuing round.
		k1, err := tools.Execute(ctx, f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
			return c.svc.CreateCheckpoint(tx, c.dispatcher, tools.Request[domain.CheckpointIntent]{Invocation: c.invocation(x5),
				Intent: domain.CheckpointIntent{RequestID: "k1", GenerationManifestID: manifest, Parts: textParts("Postgres chosen; API versioned; steps 1 and 3 done.")}}, seq)
		})
		if err != nil {
			t.Fatalf("checkpoint: %v", err)
		}
		f.view(func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			k, err := sem.Checkpoint(k1.CheckpointID)
			if err != nil {
				return err
			}
			if k.CoveredFrontier != 4 || k.IssuingExchangeID != x5.ExchangeID || k.GenerationManifestID != manifest || k.PriorCheckpointID != "" {
				t.Fatalf("checkpoint = %+v", k)
			}
			covered, err := sem.CoverageMembers(k.CoveredExchangesID, store.Page{Limit: 16})
			if err != nil {
				return err
			}
			got := map[string]bool{}
			for _, m := range covered.Records {
				got[m.ExchangeID] = true
			}
			for i, id := range c.exchanges[:4] {
				if !got[id] {
					t.Errorf("X%d not covered", i+1)
				}
			}
			if got[x5.ExchangeID] || len(got) != 4 {
				t.Errorf("covered exchanges = %v (issuing %s)", got, x5.ExchangeID)
			}
			item, err := tx.Item(k.ItemID)
			if err != nil || item.Role != domain.RoleCheckpoint || item.Authority != domain.AuthorityAgent || item.IsPinned() {
				t.Errorf("checkpoint item = %+v (%v)", item, err)
			}
			// F1 and F2 remain independent current knowledge.
			for _, fact := range []string{f1.Keyed.ItemID, f2.Keyed.ItemID} {
				if ok, err := graph.IsCurrent(tx, fact); err != nil || !ok {
					t.Errorf("fact %s retired by the checkpoint (%v)", fact, err)
				}
			}
			return nil
		})

		// A later checkpoint chains to K1 and covers one more closed round
		// (X5's tool call was answered by K1 itself).
		x6, manifest6 := c.round(6)
		k2, err := tools.Execute(ctx, f.s, sess, func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
			return c.svc.CreateCheckpoint(tx, c.dispatcher, tools.Request[domain.CheckpointIntent]{Invocation: c.invocation(x6),
				Intent: domain.CheckpointIntent{RequestID: "k2", GenerationManifestID: manifest6, Parts: textParts("As K1; round 5 checkpointed.")}}, seq)
		})
		if err != nil {
			t.Fatalf("second checkpoint: %v", err)
		}
		f.view(func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			k, err := sem.Checkpoint(k2.CheckpointID)
			if err != nil || k.PriorCheckpointID != k1.CheckpointID || k.CoveredFrontier != 5 || k.IssuingExchangeID != x6.ExchangeID {
				t.Fatalf("chained checkpoint = %+v (%v)", k, err)
			}
			return nil
		})
	})
}

func asToolError(err error, te **tools.Error) bool {
	e, ok := err.(*tools.Error)
	if ok {
		*te = e
	}
	return ok
}
