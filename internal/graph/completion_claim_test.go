package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestCompletionClaimReferenceDoesNotChangeGoal runs on both stores (P3-26,
// ADR 8 :1369): a REFERENCES edge never supplies semantic target state, so
// the stored goal's status and version must survive the link on memory and
// SQLite alike.
func TestCompletionClaimReferenceDoesNotChangeGoal(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		p := domain.Principal{SessionID: "s", TaskID: "task", AgentID: "a", Authority: domain.AuthorityAgent}
		err := s.Update(ctx, "s", func(tx store.Tx) error {
			goal := taskItem("s", "goal", tx.NextSeq(), domain.AuthorityUser)
			goal.Kind = domain.KindGoal
			status := domain.GoalResolved
			goal.GoalStatus = &status
			if err := tx.InsertItem(goal); err != nil {
				return err
			}
			claim := taskItem("s", "claim", tx.NextSeq(), domain.AuthorityAgent)
			claim.Kind = domain.KindEvidence
			claim.AgentID = "a"
			claim.Access.AgentID = "a"
			claim.Parts = []domain.ContentPart{{Type: domain.PartText, Text: CompletionClaimText("goal")}}
			claim.ContentHash = domain.ContentHash(claim.Parts)
			claim.SemanticBytes = domain.SemanticBytes(claim.Parts)
			if err := tx.InsertItem(claim); err != nil {
				return err
			}
			if _, err := LinkCompletionClaimReference(tx, p, "claim", "goal", "event", tx.NextSeq()); err != nil {
				return err
			}
			got, err := tx.Item("goal")
			if err != nil {
				return err
			}
			if *got.GoalStatus != domain.GoalResolved || got.Version != goal.Version {
				t.Fatal("claim mutated goal")
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}
