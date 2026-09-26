package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// LinkReference records that the reference item fromID REFERENCES the item
// toID (FR-DIR-003 References, M5, R2) and returns the edge. Both items must
// be accessible to actor (a hidden or missing endpoint fails with the bare
// domain.ErrNotFound, the source first). fromID must be a reference item
// (domain.ErrInvalidRecord otherwise). The reference's boundary must be
// Within the target's (domain.ErrInvalidAuthorityPromotion otherwise):
// anyone who can see the reference, and so the edge, can already see the
// target, so a broad reference can never disclose narrower evidence,
// including evidence ingested long after the reference was declared.
// References are immutable; the edge changes no currentness, requirement,
// or lifecycle state. ruleVersion names the locator identity rule that
// matched them (FR-REL-007).
func LinkReference(tx store.Tx, actor domain.Principal, fromID, toID, eventID, ruleVersion string) (domain.Relationship, error) {
	if err := actor.Validate(); err != nil {
		return domain.Relationship{}, err
	}
	from, err := loadAccessible(tx, actor, fromID)
	if err != nil {
		return domain.Relationship{}, err
	}
	to, err := loadAccessible(tx, actor, toID)
	if err != nil {
		return domain.Relationship{}, err
	}
	if from.Kind != domain.KindReference || from.ID == to.ID {
		return domain.Relationship{}, domain.ErrInvalidRecord
	}
	if !from.Access.Within(to.Access) {
		return domain.Relationship{}, domain.ErrInvalidAuthorityPromotion
	}
	rel := domain.Relationship{
		ID:          relationshipID(actor.SessionID, domain.RelReferences, from.ID, to.ID, eventID),
		SessionID:   actor.SessionID,
		Type:        domain.RelReferences,
		FromID:      from.ID,
		ToID:        to.ID,
		Seq:         tx.NextSeq(),
		Authority:   actor.Authority,
		EventID:     eventID,
		RuleVersion: ruleVersion,
	}
	if err := tx.InsertRelationship(rel); err != nil {
		return domain.Relationship{}, err
	}
	return rel, nil
}
