package sqlite

import (
	"database/sql"
	"errors"
	"sort"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func (t *transaction) Item(id string) (domain.ContextItem, error) {
	var v domain.ContextItem
	err := t.get("item", id, 0, &v)
	return v, err
}
func (t *transaction) Items(f store.ItemFilter) ([]domain.ContextItem, error) {
	bs, err := t.list("item")
	if err != nil {
		return nil, err
	}
	out := make([]domain.ContextItem, 0)
	for _, b := range bs {
		v, e := decode[domain.ContextItem](b)
		if e != nil {
			return nil, e
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
	query := "SELECT data FROM records WHERE session_id=? AND kind='relationship'"
	args := []any{t.session}
	if f.Type != "" {
		query += " AND state=?"
		args = append(args, string(f.Type))
	}
	if f.FromID != "" {
		query += " AND from_id=?"
		args = append(args, f.FromID)
	}
	if f.ToID != "" {
		query += " AND to_id=?"
		args = append(args, f.ToID)
	}
	query += " ORDER BY seq,id"
	rows, err := t.conn.QueryContext(t.ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Relationship, 0)
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		v, e := decode[domain.Relationship](b)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
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
func (t *transaction) CurrentDirective(taskID, directiveID string) (string, error) {
	var id string
	err := t.conn.QueryRowContext(t.ctx, "SELECT item_id FROM directives WHERE session_id=? AND task_id=? AND directive_id=?", t.session, taskID, directiveID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", domain.ErrNotFound
	}
	return id, err
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
	bs, err := t.list("obligation")
	if err != nil {
		return nil, err
	}
	out := make([]domain.ObligationVersion, 0)
	for _, b := range bs {
		v, e := decode[domain.ObligationVersion](b)
		if e != nil {
			return nil, e
		}
		if v.ObligationID == id {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
func (t *transaction) Obligations(taskID string) ([]domain.ObligationVersion, error) {
	bs, err := t.list("obligation")
	if err != nil {
		return nil, err
	}
	latest := map[string]domain.ObligationVersion{}
	for _, b := range bs {
		v, e := decode[domain.ObligationVersion](b)
		if e != nil {
			return nil, e
		}
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
	bs, err := t.list("obligation_transition")
	if err != nil {
		return nil, err
	}
	out := make([]domain.ObligationTransition, 0)
	for _, b := range bs {
		v, e := decode[domain.ObligationTransition](b)
		if e != nil {
			return nil, e
		}
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
	bs, err := t.list("grant")
	if err != nil {
		return nil, err
	}
	out := make([]domain.MutationGrant, 0, len(bs))
	for _, b := range bs {
		v, e := decode[domain.MutationGrant](b)
		if e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (t *transaction) Task(id string) (domain.TaskState, error) {
	var v domain.TaskState
	err := t.get("task", id, 0, &v)
	return v, err
}
func (t *transaction) LifecycleEvents(f store.LifecycleFilter) ([]domain.LifecycleEvent, error) {
	bs, err := t.list("lifecycle")
	if err != nil {
		return nil, err
	}
	out := make([]domain.LifecycleEvent, 0)
	for _, b := range bs {
		v, e := decode[domain.LifecycleEvent](b)
		if e != nil {
			return nil, e
		}
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
	bs, err := t.list("call")
	if err != nil {
		return nil, err
	}
	out := make([]domain.CallRecord, 0)
	for _, b := range bs {
		v, e := decode[domain.CallRecord](b)
		if e != nil {
			return nil, e
		}
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
	bs, err := t.list("attempt")
	if err != nil {
		return nil, err
	}
	out := make([]domain.CallAttempt, 0)
	for _, b := range bs {
		v, e := decode[domain.CallAttempt](b)
		if e != nil {
			return nil, e
		}
		if v.CallID == id {
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Attempt < out[j].Attempt })
	return out, nil
}
