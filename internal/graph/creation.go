package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"slices"
)

// CreationAcceptance contains only accepted creation inputs that are not item
// fields. Producers authenticate and qualify them before calling DeclareCreation;
// rejected attributes and synthetic transcript support must never appear here.
type CreationAcceptance struct {
	PolicyVersion             string
	AcceptedAttributes        []string
	ObligationDeclarationHash string
	SupportIDs                []string
}

// DeclareCreation is the single creation-declaration writer for ingest and tools.
// item.ID selects the authoritative stored occurrence; caller-supplied lifecycle
// fields cannot replace its creation defaults. The returned value is immutable.
func DeclareCreation(tx store.Tx, item domain.ContextItem, accepted CreationAcceptance) (result domain.CreationDeclaration, err error) {
	defer poisonGraphError(tx, &err)
	const maxValues, maxBytes = 16384, 1 << 20
	if len(accepted.AcceptedAttributes) > maxValues || len(accepted.SupportIDs) > maxValues {
		return result, store.ErrLimitExceeded
	}
	bytes := len(accepted.PolicyVersion) + len(accepted.ObligationDeclarationHash)
	if bytes > maxBytes {
		return result, store.ErrLimitExceeded
	}
	for _, values := range [][]string{accepted.AcceptedAttributes, accepted.SupportIDs} {
		for _, value := range values {
			if len(value) > maxBytes-bytes {
				return result, store.ErrLimitExceeded
			}
			bytes += len(value)
		}
	}
	stored, err := tx.Item(item.ID)
	if err != nil {
		return result, err
	}
	if err := stored.ValidateSemantic(); err != nil {
		return result, err
	}
	key, keyed := stored.CurrentKey()
	if !keyed || stored.SessionID != tx.SessionID() || stored.Role != domain.RoleSemantic || stored.Version != 1 || !tx.Allocated(stored.Seq) {
		return result, ErrDerivedLinkNotAtCreation
	}
	attributes, support := slices.Clone(accepted.AcceptedAttributes), slices.Clone(accepted.SupportIDs)
	for _, values := range [][]string{attributes, support} {
		slices.Sort(values)
		for n, value := range values {
			if value == "" || n > 0 && values[n-1] == value {
				return result, domain.ErrInvalidRecord
			}
		}
	}
	for _, id := range support {
		source, err := tx.Item(id)
		if err != nil {
			return result, err
		}
		if id == stored.ID || source.SessionID != stored.SessionID || !source.QualifiesAsEvidenceSupport() || !stored.Access.Within(source.Access) {
			return result, domain.ErrInvalidAuthorityPromotion
		}
	}
	semantics := domain.CreationSemantics{Key: key, Authority: stored.Authority, WorkflowID: stored.WorkflowID, AgentID: stored.AgentID, Section: stored.Section, Kind: stored.Kind, ContentHash: stored.ContentHash, ObligationDeclarationHash: accepted.ObligationDeclarationHash, AcceptedAttributes: attributes, SupportIDs: support, Generation: stored.Generation, Retention: stored.Retention, Residency: stored.Residency, GoalStatus: stored.GoalStatus, OriginTaskID: stored.TaskID, OriginTurnID: stored.TurnID, CreatedTurn: stored.CreatedTurn, TTLTurns: stored.TTLTurns}
	signature, err := semantics.Signature(accepted.PolicyVersion)
	if err != nil {
		return result, err
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return result, err
	}
	result = domain.CreationDeclaration{SemanticMeta: domain.SemanticMeta{ID: deriveID("declaration", "context-runtime/graph/creation-declaration-id/v1", stored.SessionID, stored.ID), SessionID: stored.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()}, ItemID: stored.ID, PolicyVersion: accepted.PolicyVersion, Signature: signature, LegacyKnown: true, AcceptedSemantics: semantics.Clone()}
	if err := result.Validate(); err != nil {
		return domain.CreationDeclaration{}, err
	}
	if err := sem.InsertCreationDeclaration(result); err != nil {
		return domain.CreationDeclaration{}, err
	}
	return result.Clone(), nil
}
