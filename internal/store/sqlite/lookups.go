package sqlite

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func (t *transaction) ItemsByBlob(blobHash string, limit int) ([]domain.ContextItem, error) {
	if limit <= 0 || !domain.ValidHash(blobHash) {
		return nil, fmt.Errorf("%w: items by blob: positive limit and valid hash required", domain.ErrInvalidRecord)
	}
	ids, err := t.boundedIDs(limit, "SELECT item_id FROM item_blobs WHERE session_id=? AND blob_hash=? LIMIT ?", t.session, blobHash, limit+1)
	if err != nil {
		return nil, err
	}
	return t.itemsByID(ids)
}

// boundedIDs runs an ID query whose last argument is limit+1 and fails with
// store.ErrLimitExceeded when it returns more than limit rows.
func (t *transaction) boundedIDs(limit int, q string, args ...any) ([]string, error) {
	rows, err := t.conn.QueryContext(t.ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		if len(ids) == limit {
			return nil, store.ErrLimitExceeded
		}
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// itemsByID loads items (verifying content) ordered by Seq then ID. An
// indexed ID without its item is corruption.
func (t *transaction) itemsByID(ids []string) ([]domain.ContextItem, error) {
	out := make([]domain.ContextItem, 0, len(ids))
	for _, id := range ids {
		it, err := t.Item(id)
		if err != nil {
			return nil, integrityIfMissing(err, "indexed item")
		}
		out = append(out, it)
	}
	slices.SortFunc(out, func(a, b domain.ContextItem) int {
		return cmp.Or(cmp.Compare(a.Seq, b.Seq), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}
