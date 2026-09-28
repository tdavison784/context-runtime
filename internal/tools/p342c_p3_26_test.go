package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_26_ClaimCitationFailuresAreUniformAndAtomic (P3-26, ADR 8 :1368):
// RecordCompletionClaim validates its whole EvidenceIDs set before writing:
// a missing, a private (another agent's), and a mixed public+private citation
// set are one uniform NOT_FOUND with no index, and the refused call writes
// nothing — no claim item, no REFERENCES edge, no consumed sequence — on both
// stores. A clean citation set still files exactly one claim, so the refusals
// come from the citations, not the fixture.
// (TestKeyedWriteCitationFailuresAreUniformAndAtomic exercises the Remember
// path on the default store only; claim_test.go varies the goal target, never
// the evidence citations.)
func TestP3_26_ClaimCitationFailuresAreUniformAndAtomic(t *testing.T) {
	p342bEachStore(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		i := seedToolFixture(t, st)
		b := seedAgentInvocation(t, st, "b")
		update(t, st, func(tx store.Tx) error {
			if _, err := insertGoal(tx, "p26c-goal", domain.GoalOpen); err != nil {
				return err
			}
			pub := storetest.NewItem("s", "p26c-pub", tx.NextSeq(), "public evidence")
			pub.Kind = domain.KindEvidence
			if err := tx.InsertItem(pub); err != nil {
				return err
			}
			private := storetest.NewItem("s", "p26c-priv", tx.NextSeq(), "b only")
			private.AgentID, private.Scope, private.Access = "b", domain.ScopeTask, conversationBoundary(b.Principal)
			return tx.InsertItem(private)
		})
		counts := func() (items, references int) {
			update(t, st, func(tx store.Tx) error {
				list, err := tx.Items(store.ItemFilter{TaskID: i.Principal.TaskID, AgentID: i.Principal.AgentID, Kinds: []domain.Kind{domain.KindEvidence}})
				if err != nil {
					t.Fatal(err)
				}
				for _, it := range list {
					if len(it.Parts) == 1 && it.Parts[0].Text == graph.CompletionClaimText("p26c-goal") {
						items++
					}
				}
				refs, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelReferences, ToID: "p26c-goal"})
				if err != nil {
					t.Fatal(err)
				}
				references = len(refs)
				return nil
			})
			return items, references
		}

		var texts []string
		for _, evidence := range [][]string{{"p26c-missing"}, {"p26c-priv"}, {"p26c-pub", "p26c-priv"}} {
			var before uint64
			err := FixedError(st.Update(testContext, "s", func(tx store.Tx) error {
				before = tx.LastSeq()
				_, err := s.RecordCompletionClaim(tx, dispatcher(i), Request[domain.CompletionClaimIntent]{i,
					domain.CompletionClaimIntent{RequestID: "p26c-" + evidence[len(evidence)-1], GoalItemID: "p26c-goal", EvidenceIDs: evidence}}, tx.NextSeq())
				return err
			}))
			if err == nil {
				t.Fatalf("citations %v were accepted", evidence)
			}
			texts = append(texts, err.Error())
			update(t, st, func(tx store.Tx) error {
				if tx.LastSeq() != before {
					t.Fatalf("citation failure %v wrote state: seq %d -> %d", evidence, before, tx.LastSeq())
				}
				return nil
			})
		}
		for _, text := range texts {
			if text != domain.ToolErrorNotFound.Message() {
				t.Fatalf("citation errors differ: %q", texts)
			}
		}
		if items, references := counts(); items != 0 || references != 0 {
			t.Fatalf("refused claims persisted %d claim items and %d references", items, references)
		}

		// Control: a clean citation set files exactly one claim with one
		// reference, so the refusals above came from the citations alone.
		clean := addToolCall(t, st, i, "p26c-clean")
		var res domain.ToolResult
		err := st.Update(testContext, "s", func(tx store.Tx) error {
			var err error
			res, err = s.RecordCompletionClaim(tx, dispatcher(clean), Request[domain.CompletionClaimIntent]{clean,
				domain.CompletionClaimIntent{RequestID: "p26c-clean", GoalItemID: "p26c-goal", EvidenceIDs: []string{"p26c-pub"}}}, tx.NextSeq())
			return err
		})
		if err != nil || res.Claim == nil || res.Claim.ClaimItemID == "" {
			t.Fatalf("clean citations refused: %+v %v", res.Claim, err)
		}
		if items, references := counts(); items != 1 || references != 1 {
			t.Fatalf("clean claim persisted %d claim items and %d references, want 1 and 1", items, references)
		}
	})
}
