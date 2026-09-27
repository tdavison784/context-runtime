package tools

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

const MethodCheckpoint = "context_checkpoint"

// CreateCheckpoint records an AGENT checkpoint for the invocation's own
// conversation. Its source is exactly the generation manifest of the inference
// that issued the call; it covers the closed prefix before the issuing round.
func (s *Service) CreateCheckpoint(tx store.Tx, dispatcher domain.Principal, r Request[domain.CheckpointIntent], seq uint64) (domain.ToolResult, error) {
	i, intent := r.Invocation, r.Intent.Clone()
	r.Intent = intent
	return execute(s, tx, dispatcher, r, MethodCheckpoint, intent.RequestID, seq, func(tx store.Tx, sem store.SemanticTx, state invocationState) (domain.ToolResult, error) {
		invocationID, _ := i.ID()
		id, err := s.writeCheckpoint(tx, sem, checkpointInput{actor: i.Principal, recipient: i.Principal, issuing: state.exchange, callID: i.CallID, key: invocationID, intent: intent})
		return domain.ToolResult{CheckpointID: id}, err
	})
}

type checkpointInput struct {
	actor, recipient domain.Principal
	issuing          domain.LogicalExchange
	callID, key      string // callID "" accepts any completed generating inference
	intent           domain.CheckpointIntent
}

// writeCheckpoint stores the dedicated CHECKPOINT item and companion. Source
// coverage is the complete admitted generation input; covered exchanges are
// the separately recorded closed prefix. Neither is caller-selected (P3-27).
func (s *Service) writeCheckpoint(tx store.Tx, sem store.SemanticTx, in checkpointInput) (string, error) {
	p := in.recipient
	if in.intent.Validate() != nil {
		return "", domain.ErrInvalidRecord
	}
	for _, part := range in.intent.Parts {
		if part.Type != domain.PartText {
			return "", domain.ErrInvalidRecord
		}
	}
	if domain.SemanticBytes(in.intent.Parts) > uint64(s.policy.MaxCheckpointSemanticBytes) {
		return "", domain.ErrResourceLimit
	}
	manifest, err := sem.AdmissionManifest(in.intent.GenerationManifestID)
	if err != nil {
		return "", privateReadError(err)
	}
	if manifest.SessionID != p.SessionID || manifest.Principal != p || manifest.ConversationID != in.issuing.ConversationID {
		return "", domain.ErrNotFound
	}
	if manifest.Validate() != nil || manifest.Purpose != domain.AdmissionGenerationInput || in.callID != "" && manifest.CallID != in.callID {
		return "", domain.ErrIncompleteCoverage
	}
	call, err := tx.Call(manifest.CallID)
	if err != nil || call.Validate() != nil || call.ConversationID != manifest.ConversationID || call.Principal != p || call.State != domain.CallCompleted {
		return "", domain.ErrIncompleteCoverage
	}
	covered, prefix, err := s.membership.CoverClosedPrefix(tx, p, in.issuing.ID, in.key)
	if err != nil {
		return "", err
	}
	last := prefix.Exchanges[len(prefix.Exchanges)-1]
	if manifest.ExchangeID != in.issuing.ID && manifest.ExchangeID != last.ID || manifest.MembershipRevision > prefix.MembershipRevision {
		return "", domain.ErrIncompleteCoverage
	}
	prior, err := s.priorCheckpoint(sem, p, prefix.ConversationID, prefix.ClosedFrontier, prefix.MembershipRevision)
	if err != nil {
		return "", err
	}
	sources, err := s.generationSources(tx, sem, p, manifest.CoverageID, prior)
	if err != nil {
		return "", err
	}
	x := in.issuing
	parts := in.intent.Parts
	item := domain.ContextItem{
		ID: toolID("checkpoint-item", in.key), Role: domain.RoleCheckpoint, Seq: tx.NextSeq(),
		SessionID: p.SessionID, WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID, TurnID: x.TurnID,
		Kind: domain.KindSummary, Generation: s.policy.CheckpointGeneration, Authority: in.actor.Authority,
		Scope: domain.ScopeTask, Access: conversationBoundary(p), Residency: domain.ResidencyResident, Retention: s.policy.CheckpointRetention,
		Parts: parts, ContentHash: domain.ContentHash(parts), SemanticBytes: domain.SemanticBytes(parts), CreatedTurn: x.Turn, Version: 1,
	}
	if err = tx.InsertItem(item); err != nil {
		return "", err
	}
	rels, err := graph.LinkDerivedCoverage(tx, in.actor, item.ID, sources, domain.CoverageGenerationInput, toolID("event", in.key), s.policy.MaxCoverageMembers)
	if err != nil {
		return "", err
	}
	c := domain.Checkpoint{
		SemanticMeta: domain.SemanticMeta{ID: toolID("checkpoint", in.key), SessionID: p.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
		ItemID:       item.ID, ConversationID: prefix.ConversationID, IssuingExchangeID: x.ID, GenerationManifestID: manifest.ID,
		SnapshotSeq: covered.Seq, MembershipRevision: prefix.MembershipRevision, CoveredFrontier: prefix.ClosedFrontier,
		SourceCoverageID: rels[0].CoverageID, CoveredExchangesID: covered.ID, PriorCheckpointID: prior.ID, PolicyVersion: s.policy.Version,
	}
	if err = c.Validate(); err != nil {
		return "", err
	}
	if err = sem.InsertCheckpoint(c); err != nil {
		return "", err
	}
	return c.ID, nil
}

// priorCheckpoint is the newest checkpoint of the conversation. A chain never
// regresses: the new checkpoint covers at least its frontier and revision.
func (s *Service) priorCheckpoint(sem store.SemanticReader, p domain.Principal, conversation string, frontier, revision uint64) (domain.Checkpoint, error) {
	page, err := sem.CheckpointsByConversation(p, conversation, store.Page{Limit: 1})
	if err != nil || len(page.Records) == 0 {
		return domain.Checkpoint{}, err
	}
	prior := page.Records[0]
	if prior.Validate() != nil || prior.SessionID != p.SessionID || prior.ConversationID != conversation {
		return domain.Checkpoint{}, domain.ErrIntegrity
	}
	if prior.CoveredFrontier > frontier || prior.MembershipRevision > revision {
		return domain.Checkpoint{}, domain.ErrInvalidTransition
	}
	return prior, nil
}

// generationSources returns the complete admitted source set. A checkpoint
// among the inputs may carry coverage forward only as the validated prior.
func (s *Service) generationSources(tx store.ReadTx, sem store.SemanticReader, p domain.Principal, coverageID string, prior domain.Checkpoint) ([]string, error) {
	c, err := sem.Coverage(coverageID)
	if err != nil || c.Validate() != nil || c.SessionID != p.SessionID || c.Purpose != domain.CoverageGenerationInput || !c.Access.Permits(p) {
		return nil, domain.ErrIncompleteCoverage
	}
	members, err := completePages(s.policy.MaxPageSize, s.policy.MaxCoverageMembers, func(page store.Page) (store.ResultPage[domain.CoverageMember], error) {
		return sem.CoverageMembers(coverageID, page)
	})
	if err != nil {
		return nil, err
	}
	if uint64(len(members)) != c.MemberCount {
		return nil, domain.ErrIncompleteCoverage
	}
	var ids []string
	for _, m := range members {
		if m.Source == nil || m.LeaseID != "" {
			continue // lease dependencies are rederived from their projection
		}
		source, err := tx.Item(m.Source.ItemID)
		if errors.Is(err, domain.ErrNotFound) || err == nil && (!source.Access.Permits(p) || source.ContentHash != m.Source.ContentHash) {
			return nil, domain.ErrIncompleteCoverage
		}
		if err != nil {
			return nil, err
		}
		if source.Role == domain.RoleCheckpoint && source.ID != prior.ItemID {
			return nil, domain.ErrIncompleteCoverage
		}
		ids = append(ids, source.ID)
	}
	return ids, nil
}
