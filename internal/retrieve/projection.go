package retrieve

import (
	"errors"
	"fmt"
	"math"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
)

var ErrResultTooLarge = errors.New("retrieval result exceeds delivery limit")

const (
	fullDeliveryVersion = "retrieval-full/v1"
	stubDeliveryVersion = "retrieval-stub/v1"
)

// ProjectionInput is trusted runtime data, never a model-authored delivery
// policy. AllowStub selects the registered retrieval-stub/v1 policy.
type ProjectionInput struct {
	Source    domain.ContextItem
	Observed  domain.ObservedItemState
	Origin    domain.RetrievalOrigin
	Task      domain.TaskState
	ItemID    string
	RequestID string
	Seq       uint64
	MaxBytes  int
	AllowStub bool
}

// buildProjection creates only TOOL historical data. Raw source parts are
// preserved in the full result; an oversized source is never silently cut.
func buildProjection(in ProjectionInput) (domain.ContextItem, string, uint64, error) {
	if in.Source.Validate() != nil || in.Observed.Validate() != nil || in.Origin.Validate() != nil || in.Task.Validate() != nil ||
		in.Observed.Source != (domain.ItemContentRef{ItemID: in.Source.ID, ContentHash: in.Source.ContentHash}) ||
		in.Origin.Holder.SessionID != in.Source.SessionID || !in.Source.Access.Permits(in.Origin.Holder) ||
		in.Task.Status != domain.TaskActive || in.Task.TaskID != in.Origin.Holder.TaskID || in.Task.WorkflowID != in.Origin.Holder.WorkflowID ||
		in.Task.TurnID != in.Origin.TurnID || in.Task.Turn == 0 || in.ItemID == "" || in.RequestID == "" || in.Seq == 0 || in.MaxBytes <= 0 {
		return domain.ContextItem{}, "", 0, domain.ErrInvalidRecord
	}
	convAccess := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: in.Origin.Holder.SessionID, WorkflowID: in.Origin.Holder.WorkflowID, TaskID: in.Origin.Holder.TaskID, AgentID: in.Origin.Holder.AgentID}
	access, ok := domain.Intersect(domain.ScopeTask, in.Source.Access, convAccess)
	if !ok {
		return domain.ContextItem{}, "", 0, domain.ErrNotFound
	}
	status := "NONE"
	if in.Observed.GoalStatus != nil {
		status = string(*in.Observed.GoalStatus)
	}
	label := fmt.Sprintf("Historical evidence %s (%s), currentness %s, goal status %s, source hash %s. Content follows as TOOL data:\n", in.Source.ID, in.Source.Kind, in.Observed.Currentness, status, in.Source.ContentHash)
	parts := append([]domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: label}}, slices.Clone(in.Source.Parts)...)
	version := fullDeliveryVersion
	omitted := uint64(0)
	if domain.SemanticBytes(parts) > uint64(in.MaxBytes) {
		if !in.AllowStub {
			return domain.ContextItem{}, "", 0, ErrResultTooLarge
		}
		var err error
		omitted, err = rawContentBytes(in.Source.Parts)
		if err != nil {
			return domain.ContextItem{}, "", 0, err
		}
		version = stubDeliveryVersion
		parts = []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: fmt.Sprintf("Historical evidence %s (%s), currentness %s, goal status %s, source hash %s. %d content bytes omitted under %s. Retrieve with context_get(%s).", in.Source.ID, in.Source.Kind, in.Observed.Currentness, status, in.Source.ContentHash, omitted, version, in.Source.ID)}}
		if domain.SemanticBytes(parts) > uint64(in.MaxBytes) {
			return domain.ContextItem{}, "", 0, ErrResultTooLarge
		}
	}
	item := domain.ContextItem{
		ID: in.ItemID, EventID: in.RequestID, Seq: in.Seq,
		SessionID: in.Origin.Holder.SessionID, WorkflowID: in.Origin.Holder.WorkflowID, TaskID: in.Origin.Holder.TaskID, AgentID: in.Origin.Holder.AgentID, TurnID: in.Origin.TurnID,
		Kind: domain.KindToolResult, Role: domain.RoleProjection, Generation: domain.GenerationEphemeral, Authority: domain.AuthorityTool,
		Scope: domain.ScopeTask, Access: access, Residency: domain.ResidencyResident, Retention: domain.RetentionLow,
		Parts: parts, ContentHash: domain.ContentHash(parts), SemanticBytes: domain.SemanticBytes(parts), CreatedTurn: in.Task.Turn,
		Source: &domain.SourceRef{Kind: domain.SourceItem, Locator: in.Source.ID, ContentHash: in.Source.ContentHash}, Version: 1,
	}
	if err := item.ValidateSemantic(); err != nil {
		return domain.ContextItem{}, "", 0, err
	}
	return item, version, omitted, nil
}

func rawContentBytes(parts []domain.ContentPart) (uint64, error) {
	var total uint64
	for _, part := range parts {
		n := part.BlobSize
		if part.Type == domain.PartText {
			n = uint64(len(part.Text))
		}
		if n > math.MaxUint64-total {
			return 0, domain.ErrResourceLimit
		}
		total += n
	}
	return total, nil
}
