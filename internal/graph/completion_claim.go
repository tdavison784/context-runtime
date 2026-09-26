package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func CompletionClaimText(goalItemID string) string {
	return "Completion claim for goal " + goalItemID + "; this claim does not change the goal or its obligations."
}

// LinkCompletionClaimReference is the narrowly scoped creation-time evidence
// exception. Ordinary LinkReference remains restricted to KindReference (P3-26).
func LinkCompletionClaimReference(tx store.Tx, actor domain.Principal, claimID, goalID, eventID string, seq uint64) (domain.Relationship, error) {
	if err := actor.Validate(); err != nil {
		return domain.Relationship{}, err
	}
	claim, err := loadAccessible(tx, actor, claimID)
	if err != nil {
		return domain.Relationship{}, err
	}
	goal, err := loadAccessible(tx, actor, goalID)
	if err != nil {
		return domain.Relationship{}, err
	}
	boundary := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: actor.SessionID, WorkflowID: actor.WorkflowID, TaskID: actor.TaskID, AgentID: actor.AgentID}
	if actor.Authority != domain.AuthorityAgent || actor.TaskID == "" || actor.AgentID == "" || claim.Authority != domain.AuthorityAgent || claim.Kind != domain.KindEvidence || goal.Kind != domain.KindGoal || claim.DirectiveID != "" || claim.Role != domain.RoleSemantic || claim.Access != boundary || !claim.Access.Within(goal.Access) {
		return domain.Relationship{}, domain.ErrInvalidAuthorityPromotion
	}
	if !tx.Allocated(claim.Seq) || !tx.Allocated(seq) || seq == 0 {
		return domain.Relationship{}, ErrDerivedLinkNotAtCreation
	}
	if len(claim.Parts) != 1 || claim.Parts[0].Type != domain.PartText || claim.Parts[0].Text != CompletionClaimText(goalID) {
		return domain.Relationship{}, domain.ErrInvalidRecord
	}
	rel := domain.Relationship{ID: relationshipID(actor.SessionID, domain.RelReferences, claimID, goalID, eventID), SessionID: actor.SessionID, Type: domain.RelReferences, FromID: claimID, ToID: goalID, Seq: seq, Authority: actor.Authority, EventID: eventID, RuleVersion: "completion-claim/1"}
	if err := tx.InsertRelationship(rel); err != nil {
		return domain.Relationship{}, err
	}
	return rel, nil
}
