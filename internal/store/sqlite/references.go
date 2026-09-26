package sqlite

import (
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
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

func (t *transaction) UnresolvedReferences(f store.ReferenceFilter) ([]domain.UnresolvedReference, error) {
	if f.Limit <= 0 {
		return nil, fmt.Errorf("%w: unresolved references: limit must be positive", domain.ErrInvalidRecord)
	}
	s := schemas["reference"]
	q := s.selectSQL + " WHERE session_id=?"
	args := []any{t.session}
	if f.LocatorKey != "" {
		q += " AND f_locator_key=?"
		args = append(args, f.LocatorKey)
	}
	if f.RuleVersion != "" {
		q += " AND f_rule_version=?"
		args = append(args, f.RuleVersion)
	}
	rows, err := t.conn.QueryContext(t.ctx, q+referenceOrderSQL, append(args, f.Limit+1)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.UnresolvedReference{}
	for rows.Next() {
		if len(out) == f.Limit {
			return nil, store.ErrLimitExceeded
		}
		v, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("unresolved reference: %w", err)
		}
		out = append(out, v.Interface().(domain.UnresolvedReference))
	}
	return out, rows.Err()
}

// referenceOrderSQL ends every reference query; with a locator key the
// reference_locator index (migration 0008) serves both filter and order.
const referenceOrderSQL = " ORDER BY f_seq, id LIMIT ?"
