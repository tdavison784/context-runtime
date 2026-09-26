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

func (t *transaction) DuplicateCandidates(f store.DuplicateFilter) ([]domain.ContextItem, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	a := f.Access
	ids, err := t.boundedIDs(f.Limit, duplicateSQL, t.session, f.ContentHash, f.TaskID, f.Section, f.Role, f.Authority,
		a.Scope, a.SessionID, a.WorkflowID, a.TaskID, a.AgentID, f.Limit+1)
	if err != nil {
		return nil, err
	}
	return t.itemsByID(ids)
}

// duplicateSQL matches the item_duplicate index (migration 0010) column for
// column.
const duplicateSQL = "SELECT id FROM rec_item WHERE session_id=? AND f_content_hash=? AND f_task_id=? AND f_section=? AND f_role=? AND f_authority=?" +
	" AND f_access_scope=? AND f_access_session_id=? AND f_access_workflow_id=? AND f_access_task_id=? AND f_access_agent_id=? AND subkey=0 LIMIT ?"

func (t *transaction) ItemsBySourceKey(locatorKey string, limit int) ([]domain.ContextItem, error) {
	if limit <= 0 || locatorKey == "" || len(locatorKey) > domain.MaxLocatorKeyBytes {
		return nil, fmt.Errorf("%w: items by source key: positive limit and a locator key required", domain.ErrInvalidRecord)
	}
	ids, err := t.boundedIDs(limit, sourceKeySQL, t.session, domain.LocatorRuleVersion, locatorKey, limit+1)
	if err != nil {
		return nil, err
	}
	return t.itemsByID(ids)
}

// sourceKeySQL matches the item_sources primary key (migration 0011).
const sourceKeySQL = "SELECT item_id FROM item_sources WHERE session_id=? AND rule_version=? AND locator_key=? LIMIT ?"
