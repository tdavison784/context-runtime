package graph

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// FileObservationState files only an already-created, validated TOOL task_state.
// W4 separately proves terminal completeness, resource applicability, run order,
// and watermark CAS in this same transaction. It cannot be called by model text.
func FileObservationState(tx store.Tx, actor domain.Principal, newItemID, subjectKey, expectedPriorID string) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	item, err := loadAccessible(tx, actor, newItemID)
	if err != nil {
		return err
	}
	if actor.Authority != domain.AuthoritySystem && actor.Authority != domain.AuthorityHarness || item.Namespace != domain.NamespaceObservation || item.DirectiveID != subjectKey || item.Authority != domain.AuthorityTool || item.Kind != domain.KindTaskState {
		return domain.ErrInvalidAuthorityPromotion
	}
	if err := item.ValidateSemantic(); err != nil {
		return err
	}
	if !tx.Allocated(item.Seq) {
		return ErrDerivedLinkNotAtCreation
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return err
	}
	key, _ := item.CurrentKey()
	prior, err := tx.CurrentVersion(key)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if prior != expectedPriorID {
		return domain.ErrVersionConflict
	}
	if expectedPriorID != "" {
		old, err := loadAccessible(tx, actor, expectedPriorID)
		if err != nil {
			return err
		}
		if ok, err := IsCurrent(tx, old.ID); err != nil {
			return err
		} else if !ok {
			return domain.ErrInvalidTransition
		}
		seq := tx.NextSeq()
		if err := domain.AuthorizeSupersession(actor, item, old); err != nil {
			return err
		}
		rel := domain.Relationship{ID: relationshipID(actor.SessionID, domain.RelSupersedes, newItemID, old.ID, item.EventID), SessionID: actor.SessionID, Type: domain.RelSupersedes, FromID: newItemID, ToID: old.ID, Seq: seq, Authority: actor.Authority, EventID: item.EventID, RuleVersion: "obs-state/1"}
		if err := tx.InsertRelationship(rel); err != nil {
			return err
		}
	}
	if err := sem.SetCurrentVersion(newItemID, expectedPriorID); err != nil {
		tx.Poison(err)
		return err
	}
	return nil
}
