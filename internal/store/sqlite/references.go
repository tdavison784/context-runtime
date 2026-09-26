package sqlite

import (
	"github.com/tdavison784/context-runtime/internal/domain"
)

func (t *transaction) InsertUnresolvedReference(r domain.UnresolvedReference) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if err := t.checkSession(r.SessionID); err != nil {
		return err
	}
	if err := t.checkSeq(r.Seq); err != nil {
		return err
	}
	if err := t.requireItem(r.ItemID, 0); err != nil {
		return err
	}
	return t.put("reference", r.ID, 0, r, false)
}

func (t *transaction) UnresolvedReference(id string) (domain.UnresolvedReference, error) {
	var v domain.UnresolvedReference
	err := t.get("reference", id, 0, &v)
	return v, err
}
