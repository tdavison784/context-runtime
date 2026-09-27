package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"slices"
	"strings"
)

type derivedCoveragePlan struct {
	coverage domain.CoverageRecord
	members  []domain.CoverageMember
	sources  []domain.ContextItem
}

// The producer supplies the complete qualifying set and a recorded finite bound.
// Publication requires Within every source; readability alone is insufficient.
func planDerivedCoverage(tx store.Tx, actor domain.Principal, derivedID string, sourceIDs []string, purpose domain.CoveragePurpose, eventID string, limit int) (derivedCoveragePlan, error) {
	var plan derivedCoveragePlan
	if limit <= 0 || len(sourceIDs) == 0 || len(sourceIDs) > limit {
		return plan, store.ErrLimitExceeded
	}
	switch purpose {
	case domain.CoverageProvenance, domain.CoverageEvidenceSupport, domain.CoverageRepresentation, domain.CoverageGenerationInput:
	default:
		return plan, domain.ErrInvalidRecord // exchange replacement and leases need their own authenticated producers
	}
	if err := actor.Validate(); err != nil {
		return plan, err
	}
	derived, err := loadAccessible(tx, actor, derivedID)
	if err != nil {
		return plan, err
	}
	if !(actor.Authority.CanHoldLifecycleAuthority() || actor.Authority == domain.AuthorityAgent) || !actor.Authority.AtLeast(derived.Authority) {
		return plan, domain.ErrInvalidAuthorityPromotion
	}
	if actor.Authority == domain.AuthorityAgent && derived.Namespace == domain.NamespaceAgentKey {
		if err := domain.AuthorizeAgentKeyWrite(actor, derived); err != nil {
			return plan, err
		}
	}
	if !tx.Allocated(derived.Seq) {
		return plan, ErrDerivedLinkNotAtCreation
	}
	ids := sortedUniqueIDs(sourceIDs)
	if len(ids) != len(sourceIDs) {
		return plan, ErrCoverageMismatch
	}
	for _, id := range ids {
		if id == derivedID {
			return plan, ErrCoverageMismatch
		}
		source, err := loadAccessible(tx, actor, id)
		if err != nil {
			return plan, err
		}
		if purpose == domain.CoverageEvidenceSupport {
			ok, err := qualifiesAsEvidenceSupport(tx, source)
			if err != nil {
				return plan, err
			}
			if !ok {
				return plan, domain.ErrInvalidRecord
			}
		}
		plan.sources = append(plan.sources, source)
	}
	if err := CheckDerivedBoundary(derived.Access, plan.sources); err != nil {
		return plan, err
	}
	seq := tx.NextSeq()
	plan.coverage = domain.CoverageRecord{SemanticMeta: domain.SemanticMeta{ID: deriveID("coverage", "context-runtime/graph/derived-coverage-id/v1", actor.SessionID, derivedID, eventID, string(purpose)), SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq}, Purpose: purpose, Access: derived.Access}
	for _, source := range plan.sources {
		ref := domain.ItemContentRef{ItemID: source.ID, ContentHash: source.ContentHash}
		members := []domain.CoverageMember{{Source: &ref}}
		if source.Role == domain.RoleProjection {
			deps, err := projectionDependencies(tx, actor, source, derived.Access)
			if err != nil {
				return plan, err
			}
			members = append(members, deps...)
		}
		if len(members) > limit-len(plan.members) {
			return plan, store.ErrLimitExceeded
		}
		for _, member := range members {
			member.SemanticMeta = domain.SemanticMeta{SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq}
			member.CoverageID = plan.coverage.ID
			key, err := member.Key()
			if err != nil {
				return plan, err
			}
			member.ID = key
			plan.members = append(plan.members, member)
		}
	}
	slices.SortFunc(plan.members, func(a, b domain.CoverageMember) int {
		ka, _ := a.Key()
		kb, _ := b.Key()
		return strings.Compare(ka, kb)
	})
	plan.members = slices.CompactFunc(plan.members, func(a, b domain.CoverageMember) bool { return a.ID == b.ID })
	plan.coverage.MemberCount = uint64(len(plan.members))
	plan.coverage.Signature, err = domain.CoverageSignature(plan.coverage, plan.members)
	if err != nil {
		return plan, err
	}
	return plan, plan.coverage.Validate()
}
