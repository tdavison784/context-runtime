package tools

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func insertGoal(tx store.Tx, id string, status domain.GoalStatus) (domain.ContextItem, error) {
	g := storetest.NewItem("s", id, tx.NextSeq(), "ship feature "+id)
	g.Kind, g.GoalStatus = domain.KindGoal, &status
	return g, tx.InsertItem(g)
}

func TestCompletionClaimReportsActualStatusAndMutatesNothing(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	b := seedAgentInvocation(t, st, "b")
	update(t, st, func(tx store.Tx) error {
		for id, status := range map[string]domain.GoalStatus{"open": domain.GoalOpen, "resolved": domain.GoalResolved, "old": domain.GoalOpen, "new": domain.GoalOpen} {
			if _, err := insertGoal(tx, id, status); err != nil {
				return err
			}
		}
		if err := tx.InsertRelationship(domain.Relationship{ID: "sup", SessionID: "s", Type: domain.RelSupersedes, FromID: "new", ToID: "old", Seq: tx.NextSeq(), Authority: domain.AuthorityUser, EventID: "e"}); err != nil {
			return err
		}
		private := storetest.NewItem("s", "private-goal", tx.NextSeq(), "b goal")
		open := domain.GoalOpen
		private.Kind, private.GoalStatus, private.AgentID, private.Scope, private.Access = domain.KindGoal, &open, "b", domain.ScopeTask, conversationBoundary(b.Principal)
		return tx.InsertItem(private)
	})
	calls := map[string]domain.ToolInvocation{"open": i, "resolved": addToolCall(t, st, i, "t-resolved"), "old": addToolCall(t, st, i, "t-old")}
	want := map[string][2]string{"open": {"OPEN", "CURRENT"}, "resolved": {"RESOLVED", "CURRENT"}, "old": {"OPEN", "HISTORICAL"}}
	for goal, inv := range calls {
		var r domain.ToolResult
		update(t, st, func(tx store.Tx) error {
			before, _ := tx.Item(goal)
			var err error
			if r, err = s.RecordCompletionClaim(tx, dispatcher(inv), Request[domain.CompletionClaimIntent]{inv, domain.CompletionClaimIntent{RequestID: "claim-" + goal, GoalItemID: goal}}, tx.NextSeq()); err != nil {
				return err
			}
			after, _ := tx.Item(goal)
			if after.Version != before.Version || *after.GoalStatus != *before.GoalStatus || after.Generation != before.Generation {
				t.Fatalf("%s mutated: %+v", goal, after)
			}
			claim, err := tx.Item(r.Claim.ClaimItemID)
			if err != nil || claim.Kind != domain.KindEvidence || claim.Authority != domain.AuthorityAgent || claim.DirectiveID != "" || claim.Parts[0].Text != graph.CompletionClaimText(goal) {
				t.Fatalf("claim item: %+v, %v", claim, err)
			}
			refs, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelReferences, FromID: claim.ID})
			if err != nil || len(refs) != 1 || refs[0].ToID != goal {
				t.Fatalf("reference: %+v, %v", refs, err)
			}
			return nil
		})
		c := r.Claim
		if string(c.GoalStatus) != want[goal][0] || string(c.Currentness) != want[goal][1] || c.ObservedVersion != 1 {
			t.Fatalf("%s observed: %+v", goal, c)
		}
		if strings.Contains(ResultText(r), "still required") != (goal == "open") {
			t.Fatalf("%s text: %s", goal, ResultText(r))
		}
		// Retrying records exactly one claim.
		update(t, st, func(tx store.Tx) error {
			again, err := s.RecordCompletionClaim(tx, dispatcher(inv), Request[domain.CompletionClaimIntent]{inv, domain.CompletionClaimIntent{RequestID: "claim-" + goal, GoalItemID: goal}}, tx.NextSeq())
			if err != nil || *again.Claim != *c {
				t.Fatalf("replay: %+v, %v", again, err)
			}
			return nil
		})
	}
	for _, target := range []string{"missing", "private-goal", "output"} {
		inv := addToolCall(t, st, i, "t-"+target)
		err := FixedError(st.Update(testContext, "s", func(tx store.Tx) error {
			_, err := s.RecordCompletionClaim(tx, dispatcher(inv), Request[domain.CompletionClaimIntent]{inv, domain.CompletionClaimIntent{RequestID: "x-" + target, GoalItemID: target}}, tx.NextSeq())
			return err
		}))
		if err.Error() != domain.ToolErrorNotFound.Message() {
			t.Fatalf("%s: %v", target, err)
		}
	}
}
