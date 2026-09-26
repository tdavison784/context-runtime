package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// verifyItemContent fails with domain.ErrIntegrity when a stored item's
// parts no longer match its content hash, as for a row whose text 0001
// altered before migration 0002 (R8). Such an item is never returned.
func verifyItemContent(v domain.ContextItem) error {
	if domain.ContentHash(v.Parts) != v.ContentHash || domain.SemanticBytes(v.Parts) != v.SemanticBytes {
		return fmt.Errorf("%w: item %s content does not match its hash", domain.ErrIntegrity, v.ID)
	}
	return nil
}

// itemCacheFootprint reports the cache's entry count and text bytes.
func (t *transaction) itemCacheFootprint() (entries, bytes int) { return t.itemCache.footprint() }

func (t *transaction) Item(id string) (domain.ContextItem, error) { return t.loadItem(id, true) }

// loadItem decodes and verifies an item, serving it from the transaction's
// bounded cache when present. Decoding and verifying costs the item's size,
// and ingest reads a span's transcript once per derived item, so point reads
// cache what they verify (SPEC-3.1 item 2). Scan-driven lookups pass
// cache=false: paging past many matches must neither grow memory nor evict
// that working set (SPEC-4.1). Returned values are clones; only verified
// items are cached.
func (t *transaction) loadItem(id string, cache bool) (domain.ContextItem, error) {
	if v, ok := t.itemCache.get(id); ok {
		return v.Clone(), nil
	}
	var v domain.ContextItem
	if err := t.get("item", id, 0, &v); err != nil {
		return domain.ContextItem{}, err
	}
	t.itemBytesLoaded += v.SemanticBytes
	if err := verifyItemContent(v); err != nil {
		return domain.ContextItem{}, err
	}
	if cache {
		if t.itemCache == nil {
			t.itemCache = &itemCache{}
		}
		t.itemCache.put(id, v.Clone())
	}
	return v, nil
}
func (t *transaction) Items(f store.ItemFilter) ([]domain.ContextItem, error) {
	q, args := itemQuery(t.session, f)
	items, err := queryRecords[domain.ContextItem](t, "item", q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]domain.ContextItem, 0, len(items))
	for _, v := range items {
		if err := verifyItemContent(v); err != nil {
			return nil, err
		}
		if len(f.Kinds) > 0 && !slices.Contains(f.Kinds, v.Kind) {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

// itemQuery pushes an item filter's equality and range predicates into SQL
// (SPEC-1.3): a task filter uses the item_task index instead of loading
// the session. Kinds are filtered after decoding.
func itemQuery(session string, f store.ItemFilter) (string, []any) {
	q, args := schemas["item"].selectSQL+" WHERE session_id=?", []any{session}
	for _, c := range []struct {
		col, val string
	}{{"f_task_id", f.TaskID}, {"f_agent_id", f.AgentID}, {"f_residency", string(f.Residency)}, {"f_directive_id", f.DirectiveID}, {"f_event_id", f.EventID}} {
		if c.val != "" {
			q += " AND " + c.col + "=?"
			args = append(args, c.val)
		}
	}
	if f.MinSeq > 0 {
		q += " AND f_seq>=?"
		args = append(args, int64(f.MinSeq))
	}
	if f.MaxSeq > 0 {
		q += " AND f_seq<=?"
		args = append(args, int64(f.MaxSeq))
	}
	return q + " ORDER BY f_seq, id", args
}

func (t *transaction) Relationships(f store.RelationshipFilter) ([]domain.Relationship, error) {
	q, args := relationshipQuery(t.session, f)
	return queryRecords[domain.Relationship](t, "relationship", q, args...)
}

// relationshipQuery pushes a relationship filter into SQL (SPEC-1.3): a
// type with a source or target uses relationship_from or relationship_to.
func relationshipQuery(session string, f store.RelationshipFilter) (string, []any) {
	q, args := schemas["relationship"].selectSQL+" WHERE session_id=?", []any{session}
	for _, c := range []struct {
		col, val string
	}{{"f_type", string(f.Type)}, {"f_from_id", f.FromID}, {"f_to_id", f.ToID}} {
		if c.val != "" {
			q += " AND " + c.col + "=?"
			args = append(args, c.val)
		}
	}
	return q + " ORDER BY f_seq, id", args
}

// queryRecords decodes the rows of a select over kind's table.
func queryRecords[T any](t *transaction, kind, q string, args ...any) ([]T, error) {
	s, err := schemaFor(kind)
	if err != nil {
		return nil, err
	}
	rows, err := t.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]T, 0)
	for rows.Next() {
		v, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", kind, err)
		}
		out = append(out, v.Interface().(T))
	}
	return out, rows.Err()
}
func (t *transaction) Event(id string) (domain.EventRecord, error) {
	var v domain.EventRecord
	err := t.get("event", id, 0, &v)
	return v, err
}
func (t *transaction) Blob(hash string) (domain.Blob, error) {
	var v domain.Blob
	v.SessionID = t.session
	v.Hash = hash
	var wasNil bool
	err := t.conn.QueryRowContext(t.ctx, "SELECT media_type,data,data_nil FROM blobs WHERE session_id=? AND hash=?", t.session, hash).Scan(&v.MediaType, &v.Data, &wasNil)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Blob{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Blob{}, err
	}
	if err = v.Validate(); err != nil {
		return domain.Blob{}, err
	}
	if wasNil {
		v.Data = nil
	} else if v.Data == nil {
		v.Data = []byte{}
	}
	return v, nil
}
func (t *transaction) CurrentVersion(key domain.CurrentKey) (string, error) {
	if err := key.Validate(); err != nil {
		return "", err
	}
	return t.current(key.TaskID, key.ID, key.Access, key.Namespace)
}

func (t *transaction) CurrentVersions(taskID string, ns domain.DirectiveNamespace, id string) ([]string, error) {
	if !ns.Valid() {
		return nil, fmt.Errorf("%w: invalid namespace %q", domain.ErrInvalidRecord, ns)
	}
	q, args := currentVersionsQuery(t.session, taskID, ns, id)
	rows, err := t.query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var itemID string
		if err := rows.Scan(&itemID); err != nil {
			return nil, err
		}
		ids = append(ids, itemID)
	}
	return ids, rows.Err()
}

// currentVersionsQuery reads the pointers of one (task, namespace, ID)
// across boundaries through the directives primary key (SPEC-2.1).
func currentVersionsQuery(session, taskID string, ns domain.DirectiveNamespace, id string) (string, []any) {
	return "SELECT item_id FROM directives WHERE session_id=? AND task_id=? AND namespace=? AND directive_id=? ORDER BY item_id",
		[]any{session, taskID, ns, id}
}

// current looks up one pointer. A boundary in another session names nothing
// here: reads report it as missing, never as invalid, so they cannot probe
// other sessions.
func (t *transaction) current(taskID, id string, boundary domain.AccessBoundary, ns domain.DirectiveNamespace) (string, error) {
	if boundary.SessionID != t.session {
		return "", domain.ErrNotFound
	}
	var itemID string
	err := t.conn.QueryRowContext(t.ctx, "SELECT item_id FROM directives WHERE session_id=? AND task_id=? AND namespace=? AND directive_id=? AND boundary_scope=? AND boundary_session_id=? AND boundary_workflow_id=? AND boundary_task_id=? AND boundary_agent_id=?", t.session, taskID, ns, id, boundary.Scope, boundary.SessionID, boundary.WorkflowID, boundary.TaskID, boundary.AgentID).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	return itemID, err
}

func (t *transaction) Obligation(id string) (domain.ObligationVersion, error) {
	vs, err := t.ObligationVersions(id)
	if err != nil {
		return domain.ObligationVersion{}, err
	}
	if len(vs) == 0 {
		return domain.ObligationVersion{}, domain.ErrNotFound
	}
	return vs[len(vs)-1], nil
}
func (t *transaction) ObligationVersions(id string) ([]domain.ObligationVersion, error) {
	q, args := obligationVersionsQuery(t.session, id)
	return queryRecords[domain.ObligationVersion](t, "obligation", q, args...)
}

// obligationVersionsQuery reads one obligation's versions by primary key,
// in version order (SPEC-2.1).
func obligationVersionsQuery(session, id string) (string, []any) {
	return schemas["obligation"].selectSQL + " WHERE session_id=? AND id=? ORDER BY subkey", []any{session, id}
}

func (t *transaction) ObligationsBySource(sourceItemID string, limit int) ([]domain.ObligationVersion, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: obligations by source: limit must be positive", domain.ErrInvalidRecord)
	}
	q, args := obligationsBySourceQuery(t.session, sourceItemID, limit)
	out, err := queryRecords[domain.ObligationVersion](t, "obligation", q, args...)
	if err != nil {
		return nil, err
	}
	if len(out) > limit {
		return nil, store.ErrLimitExceeded
	}
	return out, nil
}

// obligationsBySourceQuery reads at most limit+1 versions bound to a
// source through the obligation_source index (0006).
func obligationsBySourceQuery(session, sourceItemID string, limit int) (string, []any) {
	return schemas["obligation"].selectSQL + " WHERE session_id=? AND f_source_item_id=? ORDER BY id, subkey LIMIT ?", []any{session, sourceItemID, limit + 1}
}

func (t *transaction) Obligations(taskID string) ([]domain.ObligationVersion, error) {
	records, err := listRecords[domain.ObligationVersion](t, "obligation")
	if err != nil {
		return nil, err
	}
	latest := map[string]domain.ObligationVersion{}
	for _, v := range records {
		if taskID != "" && v.TaskID != taskID {
			continue
		}
		if v.Version > latest[v.ObligationID].Version {
			latest[v.ObligationID] = v
		}
	}
	out := make([]domain.ObligationVersion, 0, len(latest))
	for _, v := range latest {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ObligationID < out[j].ObligationID })
	return out, nil
}
func (t *transaction) ObligationTransitions(id string) ([]domain.ObligationTransition, error) {
	records, err := listRecords[domain.ObligationTransition](t, "obligation_transition")
	if err != nil {
		return nil, err
	}
	out := make([]domain.ObligationTransition, 0)
	for _, v := range records {
		if v.ObligationID == id {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
func (t *transaction) Grant(id string) (domain.MutationGrant, error) {
	var v domain.MutationGrant
	err := t.get("grant", id, 0, &v)
	return v, err
}
func (t *transaction) Grants() ([]domain.MutationGrant, error) {
	records, err := listRecords[domain.MutationGrant](t, "grant")
	if err != nil {
		return nil, err
	}
	out := records
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (t *transaction) Task(id string) (domain.TaskState, error) {
	var v domain.TaskState
	err := t.get("task", id, 0, &v)
	return v, err
}
func (t *transaction) LifecycleEvents(f store.LifecycleFilter) ([]domain.LifecycleEvent, error) {
	records, err := listRecords[domain.LifecycleEvent](t, "lifecycle")
	if err != nil {
		return nil, err
	}
	out := make([]domain.LifecycleEvent, 0)
	for _, v := range records {
		if f.TargetKind != "" && f.TargetKind != v.TargetKind || f.TargetID != "" && f.TargetID != v.TargetID || v.Seq < f.MinSeq {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
func (t *transaction) Conversation(id string) (domain.Conversation, error) {
	var v domain.Conversation
	err := t.get("conversation", id, 0, &v)
	return v, err
}
func (t *transaction) Call(id string) (domain.CallRecord, error) {
	var v domain.CallRecord
	err := t.get("call", id, 0, &v)
	return v, err
}
func (t *transaction) Calls(f store.CallFilter) ([]domain.CallRecord, error) {
	records, err := listRecords[domain.CallRecord](t, "call")
	if err != nil {
		return nil, err
	}
	out := make([]domain.CallRecord, 0)
	for _, v := range records {
		if f.ConversationID != "" && f.ConversationID != v.ConversationID {
			continue
		}
		if len(f.States) > 0 {
			ok := false
			for _, s := range f.States {
				if s == v.State {
					ok = true
				}
			}
			if !ok {
				continue
			}
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PreparedSeq != out[j].PreparedSeq {
			return out[i].PreparedSeq < out[j].PreparedSeq
		}
		return out[i].CallID < out[j].CallID
	})
	return out, nil
}
func (t *transaction) CallAttempts(id string) ([]domain.CallAttempt, error) {
	records, err := listRecords[domain.CallAttempt](t, "attempt")
	if err != nil {
		return nil, err
	}
	out := make([]domain.CallAttempt, 0)
	for _, v := range records {
		if v.CallID == id {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Attempt < out[j].Attempt })
	return out, nil
}
