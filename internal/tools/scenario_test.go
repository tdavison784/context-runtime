package tools

import (
	"errors"
	"strconv"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func runTool(t *testing.T, st store.Store, fn func(tx store.Tx) (domain.ToolResult, error)) domain.ToolResult {
	t.Helper()
	var r domain.ToolResult
	update(t, st, func(tx store.Tx) error {
		var err error
		r, err = fn(tx)
		return err
	})
	return r
}

func insertEvidence(t *testing.T, st store.Store, ids ...string) {
	t.Helper()
	update(t, st, func(tx store.Tx) error {
		for _, id := range ids {
			e := storetest.NewItem("s", id, tx.NextSeq(), "tool evidence "+id)
			e.Kind, e.Authority = domain.KindEvidence, domain.AuthorityTool
			if err := tx.InsertItem(e); err != nil {
				return err
			}
		}
		return nil
	})
}

// T16 state: twelve closed exchanges, F1/F2 from X3/X9 evidence, then K1
// issued in X13 covers exactly X1–X12 with separate source provenance.
func TestT16CheckpointCoversTwelveClosedExchanges(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	insertEvidence(t, st, "E3", "E9")
	state := func(inv domain.ToolInvocation, n int) {
		runTool(t, st, func(tx store.Tx) (domain.ToolResult, error) {
			return s.UpdateState(tx, dispatcher(inv), Request[domain.KeyedWriteIntent]{inv, domain.KeyedWriteIntent{RequestID: "state-" + strconv.Itoa(n), Key: "progress", Kind: domain.KindTaskState, Parts: keyed("", "", "step "+strconv.Itoa(n)).Parts}}, tx.NextSeq())
		})
	}
	facts := map[string]domain.KeyedWriteResult{}
	exchanges := []string{i.ExchangeID}
	cur := i
	state(cur, 1)
	for n := 2; n <= 13; n++ {
		var extra []string
		switch n {
		case 3:
			extra = []string{"E3"}
		case 9:
			extra = []string{"E9"}
		}
		cur, _ = nextRound(t, st, cur, strconv.Itoa(n), true, extra...)
		if n == 13 {
			break
		}
		exchanges = append(exchanges, cur.ExchangeID)
		switch n {
		case 4, 10:
			fact, evidence := "F1", "E3"
			if n == 10 {
				fact, evidence = "F2", "E9"
			}
			r := runTool(t, st, func(tx store.Tx) (domain.ToolResult, error) {
				return s.Remember(tx, dispatcher(cur), Request[domain.KeyedWriteIntent]{cur, keyed("fact-"+fact, fact, fact+" holds", evidence)}, tx.NextSeq())
			})
			facts[fact] = *r.Keyed
		default:
			state(cur, n)
		}
	}
	var manifest string
	update(t, st, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		page, err := sem.AdmissionsByExchange(exchanges[11], store.Page{Limit: 4})
		if err != nil || len(page.Records) != 1 {
			t.Fatalf("X12 admission: %+v, %v", page, err)
		}
		manifest = page.Records[0].ID
		return nil
	})
	k1 := runTool(t, st, func(tx store.Tx) (domain.ToolResult, error) {
		return s.CreateCheckpoint(tx, dispatcher(cur), Request[domain.CheckpointIntent]{cur, summary("k1", manifest, "K1: F1 and F2 hold; steps 1-12 done.")}, tx.NextSeq())
	})
	update(t, st, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		c, err := sem.Checkpoint(k1.CheckpointID)
		if err != nil || c.CoveredFrontier != 12 {
			t.Fatalf("K1: %+v, %v", c, err)
		}
		item, _ := tx.Item(c.ItemID)
		if item.Authority != domain.AuthorityAgent || item.Kind != domain.KindSummary || item.Role != domain.RoleCheckpoint {
			t.Fatalf("K1 item: %+v", item)
		}
		covered, _ := sem.CoverageMembers(c.CoveredExchangesID, store.Page{Limit: 32})
		got := map[string]bool{}
		for _, m := range covered.Records {
			got[m.ExchangeID] = true
		}
		if len(covered.Records) != 12 || got[cur.ExchangeID] {
			t.Fatalf("K1 covers %d exchanges (issuing included: %v)", len(covered.Records), got[cur.ExchangeID])
		}
		for _, x := range exchanges {
			if !got[x] {
				t.Fatalf("K1 misses exchange %s", x)
			}
		}
		for fact, evidence := range map[string]string{"F1": "E3", "F2": "E9"} {
			id := facts[fact].ItemID
			current, err := graph.IsCurrent(tx, id)
			d, _ := sem.CreationDeclaration(id)
			if err != nil || !current || len(d.AcceptedSemantics.SupportIDs) != 1 || d.AcceptedSemantics.SupportIDs[0] != evidence {
				t.Fatalf("%s: current=%v support=%v, %v", fact, current, d.AcceptedSemantics.SupportIDs, err)
			}
		}
		return nil
	})
}

// T17 state: tools write at AGENT authority only, a foreign-session citation
// writes nothing, and a completion claim changes neither G1 nor O1.
func TestT17AgentToolsWriteAtAgentAuthorityOnly(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	insertEvidence(t, st, "X9")
	update(t, st, func(tx store.Tx) error {
		pin := storetest.NewItem("s", "P1", tx.NextSeq(), "pinned note")
		pin.Generation = domain.GenerationPinned
		if err := tx.InsertItem(pin); err != nil {
			return err
		}
		if _, err := insertGoal(tx, "G1", domain.GoalOpen); err != nil {
			return err
		}
		return tx.InsertObligationVersion(storetest.NewObligation("s", "O1", 1, tx.NextSeq(), "G1"))
	})
	if err := st.Update(testContext, "s2", func(tx store.Tx) error {
		return tx.InsertItem(storetest.NewItem("s2", "foreign", tx.NextSeq(), "other session"))
	}); err != nil {
		t.Fatal(err)
	}
	status := func(inv domain.ToolInvocation, request, text string) domain.KeyedWriteResult {
		return *runTool(t, st, func(tx store.Tx) (domain.ToolResult, error) {
			return s.UpdateState(tx, dispatcher(inv), Request[domain.KeyedWriteIntent]{inv, domain.KeyedWriteIntent{RequestID: request, Key: "status", Kind: domain.KindTaskState, Parts: keyed("", "", text).Parts}}, tx.NextSeq())
		}).Keyed
	}
	first := status(i, "s1", "tests running")
	second := status(addToolCall(t, st, i, "t2"), "s2", "tests green")
	note := addToolCall(t, st, i, "t3")
	err := FixedError(st.Update(testContext, "s", func(tx store.Tx) error {
		_, err := s.Remember(tx, dispatcher(note), Request[domain.KeyedWriteIntent]{note, keyed("api", "api-note", "v2 endpoint", "foreign")}, tx.NextSeq())
		return err
	}))
	if err.Error() != domain.ToolErrorNotFound.Message() {
		t.Fatalf("foreign citation: %v", err)
	}
	resolve := addToolCall(t, st, i, "t4")
	claim := runTool(t, st, func(tx store.Tx) (domain.ToolResult, error) {
		return s.RecordCompletionClaim(tx, dispatcher(resolve), Request[domain.CompletionClaimIntent]{resolve, domain.CompletionClaimIntent{RequestID: "resolve", GoalItemID: "G1", EvidenceIDs: []string{"X9"}}}, tx.NextSeq())
	}).Claim
	update(t, st, func(tx store.Tx) error {
		if second.SupersededItemID != first.ItemID {
			t.Fatalf("status chain: %+v %+v", first, second)
		}
		for id, want := range map[string]bool{first.ItemID: false, second.ItemID: true} {
			if cur, err := graph.IsCurrent(tx, id); err != nil || cur != want {
				t.Fatalf("status %s current=%v, %v", id, cur, err)
			}
		}
		g1, _ := tx.Item("G1")
		o1, _ := tx.Obligation("O1")
		if *g1.GoalStatus != domain.GoalOpen || o1.Status != domain.ObligationUnresolved || claim.GoalStatus != domain.GoalOpen || claim.Currentness != domain.ItemCurrent {
			t.Fatalf("G1/O1 changed: %+v %+v %+v", g1, o1, claim)
		}
		refs, _ := tx.Relationships(store.RelationshipFilter{Type: domain.RelReferences, FromID: claim.ClaimItemID})
		if len(refs) != 1 || refs[0].ToID != "G1" {
			t.Fatalf("claim reference: %+v", refs)
		}
		obligations, _ := tx.Obligations("")
		if len(obligations) != 1 {
			t.Fatalf("tools created obligations: %+v", obligations)
		}
		for _, id := range []string{first.ItemID, second.ItemID, claim.ClaimItemID} {
			it, _ := tx.Item(id)
			if it.Authority != domain.AuthorityAgent || it.Generation == domain.GenerationPinned || it.Kind == domain.KindGoal {
				t.Fatalf("tool item %s exceeds agent authority: %+v", id, it)
			}
		}
		if _, err := tx.Item(toolID("keyed", mustID(note))); !errors.Is(err, domain.ErrNotFound) {
			t.Fatal("failed api-note left an item", err)
		}
		return nil
	})
}

func mustID(i domain.ToolInvocation) string { id, _ := i.ID(); return id }
