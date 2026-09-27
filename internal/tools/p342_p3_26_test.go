package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestP3_26_RetryWritesOneClaim: a context_resolve request writes exactly one
// claim occurrence. The identical retry replays the frozen result without
// writing a second claim item or a second REFERENCES edge and without
// consuming a sequence; only a distinct request under its own invocation
// files another claim. Every claim still references the same goal occurrence
// and reports its unchanged status.
func TestP3_26_RetryWritesOneClaim(t *testing.T) {
	p24Stores(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		i := seedToolFixture(t, st)
		update(t, st, func(tx store.Tx) error {
			_, err := insertGoal(tx, "p26-goal", domain.GoalOpen)
			return err
		})

		claim := func(invocation domain.ToolInvocation, request string) (domain.ToolResult, error) {
			// Through the outer boundary the sequence is allocated only after
			// the replay check, so a retry consumes nothing (FR-ING-006).
			return Execute(testContext, st, "s", func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
				return s.RecordCompletionClaim(tx, dispatcher(invocation), Request[domain.CompletionClaimIntent]{invocation,
					domain.CompletionClaimIntent{RequestID: request, GoalItemID: "p26-goal"}}, seq)
			})
		}
		counts := func() (items, references int) {
			update(t, st, func(tx store.Tx) error {
				list, err := tx.Items(store.ItemFilter{TaskID: i.Principal.TaskID, AgentID: i.Principal.AgentID, Kinds: []domain.Kind{domain.KindEvidence}})
				if err != nil {
					t.Fatal(err)
				}
				for _, it := range list {
					if len(it.Parts) == 1 && it.Parts[0].Text == graph.CompletionClaimText("p26-goal") {
						items++
					}
				}
				refs, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelReferences, ToID: "p26-goal"})
				if err != nil {
					t.Fatal(err)
				}
				references = len(refs)
				return nil
			})
			return items, references
		}

		first, err := claim(i, "p26")
		if err != nil || first.Claim == nil || first.Claim.GoalStatus != domain.GoalOpen || first.Claim.Currentness != domain.ItemCurrent {
			t.Fatalf("first claim: %+v, %v", first.Claim, err)
		}
		before := lastSeq(t, st)
		again, err := claim(i, "p26")
		if err != nil || *again.Claim != *first.Claim {
			t.Fatalf("retry: %+v, %v (want %+v)", again.Claim, err, first.Claim)
		}
		if after := lastSeq(t, st); after != before {
			t.Fatalf("retry consumed a sequence: %d -> %d", before, after)
		}
		if items, references := counts(); items != 1 || references != 1 {
			t.Fatalf("retry persisted %d claim items and %d references, want 1 and 1", items, references)
		}

		// Only a distinct request files another claim — one more item, one
		// more reference, same reported truth.
		second, err := claim(addToolCall(t, st, i, "p26-t2"), "p26b")
		if err != nil || second.Claim.ClaimItemID == first.Claim.ClaimItemID || second.Claim.GoalStatus != domain.GoalOpen {
			t.Fatalf("distinct request: %+v, %v", second.Claim, err)
		}
		if items, references := counts(); items != 2 || references != 2 {
			t.Fatalf("distinct request persisted %d claim items and %d references, want 2 and 2", items, references)
		}
		// A third distinct request under its own invocation files a third
		// claim, and its identical retry adds nothing further.
		third := addToolCall(t, st, i, "p26-t3")
		if _, err := claim(third, "p26c"); err != nil {
			t.Fatal(err)
		}
		if items, references := counts(); items != 3 || references != 3 {
			t.Fatalf("third claim: %d items, %d references", items, references)
		}
		if _, err := claim(third, "p26c"); err != nil {
			t.Fatal(err)
		}
		if items, references := counts(); items != 3 || references != 3 {
			t.Fatalf("third claim's retry persisted %d items and %d references, want 3 and 3", items, references)
		}
	})
}
