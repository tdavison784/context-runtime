package tools

import (
	"errors"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

const (
	MethodRemember    = "context_remember"
	MethodUpdateState = "context_update_state"
)

// keyedMethod fixes everything the model cannot choose: allowed kinds and
// creation defaults. Owner, authority and boundary come from the invocation.
type keyedMethod struct {
	name       string
	kinds      []domain.Kind
	generation domain.Generation
	retention  domain.RetentionClass
}

var (
	rememberMethod    = keyedMethod{MethodRemember, []domain.Kind{domain.KindFact, domain.KindDecision}, domain.GenerationDurable, domain.RetentionHigh}
	updateStateMethod = keyedMethod{MethodUpdateState, []domain.Kind{domain.KindTaskState}, domain.GenerationWorking, domain.RetentionNormal}
)

// Remember files a fact or decision as a new AGENT_KEY occurrence (P3-25).
func (s *Service) Remember(tx store.Tx, i domain.ToolInvocation, intent domain.KeyedWriteIntent) (domain.ToolResult, error) {
	return s.keyedWrite(tx, i, intent, rememberMethod)
}

// UpdateState files task_state as a new AGENT_KEY occurrence (P3-25).
func (s *Service) UpdateState(tx store.Tx, i domain.ToolInvocation, intent domain.KeyedWriteIntent) (domain.ToolResult, error) {
	return s.keyedWrite(tx, i, intent, updateStateMethod)
}

func (s *Service) keyedWrite(tx store.Tx, i domain.ToolInvocation, intent domain.KeyedWriteIntent, method keyedMethod) (domain.ToolResult, error) {
	intent = intent.Clone()
	return execute(s, tx, i, method.name, intent.RequestID, intent, func(tx store.Tx, _ store.SemanticTx, state invocationState) (domain.ToolResult, error) {
		var none domain.ToolResult
		if intent.Validate() != nil || !slices.Contains(method.kinds, intent.Kind) {
			return none, domain.ErrInvalidRecord
		}
		for _, part := range intent.Parts {
			if part.Type != domain.PartText {
				return none, domain.ErrInvalidRecord
			}
		}
		p := i.Principal
		evidence, err := s.accessibleSet(tx, p, intent.EvidenceIDs)
		if err != nil {
			return none, err
		}
		invocationID, _ := i.ID()
		eventID := toolID("event", invocationID)
		boundary := conversationBoundary(p)
		item := domain.ContextItem{
			ID: toolID("keyed", invocationID), DirectiveID: domain.AgentKeyID(intent.Key), Namespace: domain.NamespaceAgentKey,
			Seq: tx.NextSeq(), SessionID: p.SessionID, WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID,
			TurnID: state.exchange.TurnID, Kind: intent.Kind, Generation: method.generation, Authority: domain.AuthorityAgent,
			Scope: domain.ScopeTask, Access: boundary, Residency: domain.ResidencyResident, Retention: method.retention,
			Parts: slices.Clone(intent.Parts), ContentHash: domain.ContentHash(intent.Parts), SemanticBytes: domain.SemanticBytes(intent.Parts),
			CreatedTurn: state.exchange.Turn, Version: 1,
		}
		if err = item.ValidateSemantic(); err != nil {
			return none, domain.ErrInvalidRecord
		}
		if err = tx.InsertItem(item); err != nil {
			return none, err
		}
		// Request provenance is honest history, never qualifying support.
		if _, err = graph.LinkDerivedCoverage(tx, p, item.ID, []string{state.output.ItemID}, domain.CoverageProvenance, eventID, s.policy.MaxEvidence); err != nil {
			return none, err
		}
		if len(evidence) > 0 {
			if _, err = graph.LinkDerivedCoverage(tx, p, item.ID, evidence, domain.CoverageEvidenceSupport, eventID, s.policy.MaxEvidence); err != nil {
				return none, err
			}
		}
		if _, err = graph.DeclareCreation(tx, item, graph.CreationAcceptance{PolicyVersion: s.policy.Dedup, SupportIDs: evidence}); err != nil {
			return none, err
		}
		current, err := graph.CurrentVersionFor(tx, p, item)
		switch {
		case errors.Is(err, domain.ErrNotFound):
		case err != nil:
			return none, err
		default:
			same, err := graph.SameDirective(tx, item, "", current)
			if err != nil {
				return none, err
			}
			if same {
				if _, err = graph.LinkDuplicate(tx, p, item.ID, current.ID, eventID, s.policy.Dedup, ""); err != nil {
					return none, err
				}
				return domain.ToolResult{Keyed: &domain.KeyedWriteResult{ItemID: item.ID, CanonicalItemID: current.ID, Duplicate: true}}, nil
			}
		}
		previous, err := graph.ReplaceDirective(tx, p, p.TaskID, item.DirectiveID, item.ID, eventID)
		if err != nil {
			return none, err
		}
		return domain.ToolResult{Keyed: &domain.KeyedWriteResult{ItemID: item.ID, CanonicalItemID: item.ID, SupersededItemID: previous}}, nil
	})
}

// accessibleSet validates a whole citation set before any qualification check:
// a missing, private, or cross-session ID is the same NOT_FOUND, with no index.
func (s *Service) accessibleSet(tx store.ReadTx, p domain.Principal, ids []string) ([]string, error) {
	if len(ids) > s.policy.MaxEvidence {
		return nil, domain.ErrResourceLimit
	}
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	if len(slices.Compact(slices.Clone(sorted))) != len(sorted) {
		return nil, domain.ErrInvalidRecord
	}
	for _, id := range sorted {
		it, err := tx.Item(id)
		if err != nil {
			return nil, privateReadError(err)
		}
		if it.SessionID != p.SessionID || !it.Access.Permits(p) {
			return nil, domain.ErrNotFound
		}
	}
	return sorted, nil
}

// conversationBoundary is the fixed boundary of an agent's conversation:
// session plus the principal's exact workflow/task/agent conjunction.
func conversationBoundary(p domain.Principal) domain.AccessBoundary {
	return domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: p.SessionID, WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID}
}
