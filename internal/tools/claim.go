package tools

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

const MethodResolve = "context_resolve"

// RecordCompletionClaim records that the agent claims a goal is complete. It
// changes no goal, obligation, currentness, or protection, and reports the
// goal occurrence's actual observed status and currentness (P3-26, C-18).
func (s *Service) RecordCompletionClaim(tx store.Tx, dispatcher domain.Principal, r Request[domain.CompletionClaimIntent], seq uint64) (domain.ToolResult, error) {
	i, intent := r.Invocation, r.Intent.Clone()
	r.Intent = intent
	return execute(s, tx, dispatcher, r, MethodResolve, intent.RequestID, seq, func(tx store.Tx, sem store.SemanticTx, state invocationState) (domain.ToolResult, error) {
		var none domain.ToolResult
		if intent.Validate() != nil {
			return none, domain.ErrInvalidRecord
		}
		p := i.Principal
		// Missing, private, cross-session, and non-goal targets are one error.
		goal, err := tx.Item(intent.GoalItemID)
		if err != nil {
			return none, privateReadError(err)
		}
		if goal.SessionID != p.SessionID || !goal.Access.Permits(p) || goal.Kind != domain.KindGoal || goal.GoalStatus == nil {
			return none, domain.ErrNotFound
		}
		evidence, err := s.accessibleSet(tx, p, intent.EvidenceIDs)
		if err != nil {
			return none, err
		}
		currentness, err := observedCurrentness(tx, goal)
		if err != nil {
			return none, err
		}
		invocationID, _ := i.ID()
		eventID := toolID("event", invocationID)
		parts := []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: graph.CompletionClaimText(goal.ID)}}
		claim := domain.ContextItem{
			ID: toolID("claim", invocationID), Seq: tx.NextSeq(), SessionID: p.SessionID, WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID,
			TurnID: state.exchange.TurnID, Kind: domain.KindEvidence, Generation: domain.GenerationWorking, Authority: domain.AuthorityAgent,
			Scope: domain.ScopeTask, Access: conversationBoundary(p), Residency: domain.ResidencyResident, Retention: domain.RetentionNormal,
			Parts: parts, ContentHash: domain.ContentHash(parts), SemanticBytes: domain.SemanticBytes(parts), CreatedTurn: state.exchange.Turn, Version: 1,
		}
		if err = claim.ValidateSemantic(); err != nil {
			return none, err
		}
		if err = tx.InsertItem(claim); err != nil {
			return none, err
		}
		if _, err = graph.LinkCompletionClaimReference(tx, p, claim.ID, goal.ID, eventID, tx.NextSeq()); err != nil {
			return none, err
		}
		if _, err = graph.LinkDerivedCoverage(tx, p, claim.ID, []string{state.output.ItemID}, domain.CoverageProvenance, eventID, s.policy.MaxEvidence); err != nil {
			return none, err
		}
		if len(evidence) > 0 {
			if _, err = graph.LinkDerivedCoverage(tx, p, claim.ID, evidence, domain.CoverageEvidenceSupport, eventID, s.policy.MaxEvidence); err != nil {
				return none, err
			}
		}
		return domain.ToolResult{Claim: &domain.CompletionClaimResult{
			ClaimItemID: claim.ID, TargetItemID: goal.ID, ObservedVersion: goal.Version,
			GoalStatus: *goal.GoalStatus, Currentness: currentness,
		}}, nil
	})
}

// observedCurrentness classifies the exact occurrence named, never the key's
// replacement: a duplicate or superseded goal is reported as such.
func observedCurrentness(tx store.ReadTx, it domain.ContextItem) (domain.ItemCurrentness, error) {
	current, err := graph.IsCurrent(tx, it.ID)
	if err != nil || current {
		return domain.ItemCurrent, err
	}
	dups, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDuplicateOf, FromID: it.ID})
	if err != nil {
		return "", err
	}
	if len(dups) > 0 {
		return domain.ItemDuplicate, nil
	}
	return domain.ItemHistorical, nil
}
