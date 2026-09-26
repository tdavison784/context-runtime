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

func (t *transaction) Item(id string) (domain.ContextItem, error) {
	var v domain.ContextItem
	if err := t.get("item", id, 0, &v); err != nil {
		return domain.ContextItem{}, err
	}
	if err := verifyItemContent(v); err != nil {
		return domain.ContextItem{}, err
	}
	return v, nil
}
func (t *transaction) Items(f store.ItemFilter) ([]domain.ContextItem, error) {
	items, err := listRecords[domain.ContextItem](t, "item")
	if err != nil {
		return nil, err
	}
	out := make([]domain.ContextItem, 0)
	for _, v := range items {
		if err := verifyItemContent(v); err != nil {
			return nil, err
		}
		if f.TaskID != "" && f.TaskID != v.TaskID || f.AgentID != "" && f.AgentID != v.AgentID || f.Residency != "" && f.Residency != v.Residency || f.DirectiveID != "" && f.DirectiveID != v.DirectiveID || f.EventID != "" && f.EventID != v.EventID || v.Seq < f.MinSeq || f.MaxSeq != 0 && v.Seq > f.MaxSeq {
			continue
		}
		if len(f.Kinds) > 0 {
			ok := false
			for _, k := range f.Kinds {
				if k == v.Kind {
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
		if out[i].Seq != out[j].Seq {
			return out[i].Seq < out[j].Seq
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
func (t *transaction) Relationships(f store.RelationshipFilter) ([]domain.Relationship, error) {
	// Filtering is performed after decoding so all list methods share the
	// same per-record typed-column decoder.
	records, err := listRecords[domain.Relationship](t, "relationship")
	if err != nil {
		return nil, err
	}
	out := make([]domain.Relationship, 0)
	for _, v := range records {
		if f.Type != "" && f.Type != v.Type || f.FromID != "" && f.FromID != v.FromID || f.ToID != "" && f.ToID != v.ToID {
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
	rows, err := t.conn.QueryContext(t.ctx, "SELECT item_id FROM directives WHERE session_id=? AND task_id=? AND namespace=? AND directive_id=? ORDER BY item_id", t.session, taskID, ns, id)
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

func (t *transaction) CurrentDirective(taskID, directiveID string, boundary domain.AccessBoundary) (string, error) {
	if err := boundary.Validate(); err != nil {
		return "", err
	}
	id, err := t.current(taskID, directiveID, boundary, domain.NamespaceDirective)
	if errors.Is(err, domain.ErrNotFound) {
		return t.current(taskID, directiveID, boundary, domain.NamespaceAgentKey)
	}
	return id, err
}

func (t *transaction) CurrentDirectives(taskID, directiveID string) ([]string, error) {
	return currentDirectives(t, taskID, directiveID)
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
	records, err := listRecords[domain.ObligationVersion](t, "obligation")
	if err != nil {
		return nil, err
	}
	out := make([]domain.ObligationVersion, 0)
	for _, v := range records {
		if v.ObligationID == id {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
func (t *transaction) ObligationsBySource(sourceItemID string, limit int) ([]domain.ObligationVersion, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("%w: obligations by source: limit must be positive", domain.ErrInvalidRecord)
	}
	s := schemas["obligation"]
	rows, err := t.conn.QueryContext(t.ctx, s.selectSQL+" WHERE session_id=? AND f_source_item_id=? ORDER BY id, subkey LIMIT ?", t.session, sourceItemID, limit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []domain.ObligationVersion{}
	for rows.Next() {
		if len(out) == limit {
			return nil, store.ErrLimitExceeded
		}
		v, err := s.scan(rows)
		if err != nil {
			return nil, fmt.Errorf("obligation: %w", err)
		}
		out = append(out, v.Interface().(domain.ObligationVersion))
	}
	return out, rows.Err()
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

// currentDirectives is the deprecated namespace-agnostic view: the pointers
// of both namespaces, ordered by item ID.
func currentDirectives(r store.ReadTx, taskID, directiveID string) ([]string, error) {
	var out []string
	for _, ns := range []domain.DirectiveNamespace{domain.NamespaceDirective, domain.NamespaceAgentKey} {
		ids, err := r.CurrentVersions(taskID, ns, directiveID)
		if err != nil {
			return nil, err
		}
		out = append(out, ids...)
	}
	slices.Sort(out)
	return out, nil
}
