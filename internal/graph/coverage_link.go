package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// LinkDerivedCoverage stores one purpose-tagged complete coverage set and N
// small edges. Producers select qualifying support explicitly; automatic request
// or transcript provenance never implies evidence support. It makes no admission
// or lease-liveness claim; W3/W6's shared eligibility check owns those decisions.
func LinkDerivedCoverage(tx store.Tx, actor domain.Principal, derivedID string, sourceIDs []string, purpose domain.CoveragePurpose, eventID string, maxMembers int) (result []domain.Relationship, err error) {
	defer poisonGraphError(tx, &err)
	plan, err := planDerivedCoverage(tx, actor, derivedID, sourceIDs, purpose, eventID, maxMembers)
	if err != nil {
		return nil, err
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return nil, err
	}
	if err := sem.InsertCoverage(plan.coverage, plan.members); err != nil {
		return nil, err
	}
	for _, source := range plan.sources {
		rel := domain.Relationship{ID: deriveID("rel", "context-runtime/graph/derived-relationship-id/v2", actor.SessionID, derivedID, source.ID, eventID, string(purpose)), SessionID: actor.SessionID, Type: domain.RelDerivedFrom, FromID: derivedID, ToID: source.ID, Seq: tx.NextSeq(), Authority: actor.Authority, EventID: eventID, RuleVersion: "derived-coverage/v1", CoverageID: plan.coverage.ID}
		if err := tx.InsertRelationship(rel); err != nil {
			return nil, err
		}
		result = append(result, rel)
	}
	return result, nil
}
