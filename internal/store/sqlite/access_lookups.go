package sqlite

import (
	"errors"
	"fmt"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Access-filtered lookups (F1) over the lookup_* tables of migration 0012.

// indexLookups adds a new item's rows to the lookup tables, inside
// InsertItem's savepoint.
func (t *transaction) indexLookups(v domain.ContextItem) error {
	a := v.Access
	seen := map[string]bool{}
	for _, p := range v.Parts {
		if p.BlobHash == "" || seen[p.BlobHash] {
			continue
		}
		seen[p.BlobHash] = true
		if _, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_blob(session_id,blob_hash,workflow_id,task_id,agent_id,seq,item_id) VALUES(?,?,?,?,?,?,?)",
			t.session, p.BlobHash, a.WorkflowID, a.TaskID, a.AgentID, v.Seq, v.ID); err != nil {
			return err
		}
	}
	if _, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_canonical(session_id,content_hash,task_id,section,directive_id,kind,role,authority,scope,access_session_id,workflow_id,access_task_id,agent_id,seq,item_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
		t.session, v.ContentHash, v.TaskID, v.Section, v.DirectiveID, v.Kind, v.Role, v.Authority, a.Scope, a.SessionID, a.WorkflowID, a.TaskID, a.AgentID, v.Seq, v.ID); err != nil {
		return err
	}
	if v.Section == domain.SectionWorking {
		if _, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_working(session_id,task_id,authority,scope,access_session_id,workflow_id,access_task_id,agent_id,seq,item_id,directive_id) VALUES(?,?,?,?,?,?,?,?,?,?,?)",
			t.session, v.TaskID, v.Authority, a.Scope, a.SessionID, a.WorkflowID, a.TaskID, a.AgentID, v.Seq, v.ID, v.DirectiveID); err != nil {
			return err
		}
	}
	if v.Source != nil {
		if key, ok := domain.LocatorKey(v.Source.Kind, v.Source.Locator); ok {
			if _, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_source(session_id,rule_version,locator_key,workflow_id,task_id,agent_id,seq,item_id) VALUES(?,?,?,?,?,?,?,?)",
				t.session, domain.LocatorRuleVersion, key, a.WorkflowID, a.TaskID, a.AgentID, v.Seq, v.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// retireLookups drops an item that stopped being live (superseded or
// classified a duplicate) from the live lookup tables.
func (t *transaction) retireLookups(itemID string) error {
	for _, table := range []string{"lookup_canonical", "lookup_working", "lookup_source"} {
		if _, err := t.conn.ExecContext(t.ctx, "DELETE FROM "+table+" WHERE session_id=? AND item_id=?", t.session, itemID); err != nil {
			return err
		}
	}
	return nil
}

// ownerClause restricts the given owner columns to values whose boundary
// permits every principal in ps (the intersection of their permitted
// owners). ok is false when no boundary can permit them all.
func ownerClause(wf, task, agent string, ps ...domain.Principal) (string, []any, bool) {
	var clauses []string
	var args []any
	for i, col := range []string{wf, task, agent} {
		allowed := map[string]int{}
		for _, p := range ps {
			wfs, tasks, agents := store.PermittedOwners(p)
			for _, v := range [][]string{wfs, tasks, agents}[i] {
				allowed[v]++
			}
		}
		var vals []string
		for v, n := range allowed {
			if n == len(ps) {
				vals = append(vals, v)
			}
		}
		if len(vals) == 0 {
			return "", nil, false
		}
		clauses = append(clauses, col+" IN (?"+strings.Repeat(",?", len(vals)-1)+")")
		for _, v := range vals {
			args = append(args, v)
		}
	}
	return strings.Join(clauses, " AND "), args, true
}

// scanItems loads the items named by q's (item_id) rows in order. It stops
// after limit verified items plus one more row (reported as more), and
// records visible items failing verification as Unverified.
func (t *transaction) scanItems(viewer domain.Principal, limit int, q string, args ...any) (store.Lookup, bool, error) {
	rows, err := t.conn.QueryContext(t.ctx, q, args...)
	if err != nil {
		return store.Lookup{}, false, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return store.Lookup{}, false, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return store.Lookup{}, false, err
	}
	out := store.Lookup{Items: []domain.ContextItem{}}
	for _, id := range ids {
		it, err := t.Item(id)
		switch {
		case errors.Is(err, domain.ErrIntegrity):
			out.Unverified = append(out.Unverified, id)
			continue
		case err != nil:
			return store.Lookup{}, false, integrityIfMissing(err, "indexed item")
		case !it.Access.Permits(viewer):
			continue
		}
		if len(out.Items) == limit {
			return out, true, nil
		}
		out.Items = append(out.Items, it)
		out.Next = store.Cursor{Seq: it.Seq, ID: it.ID}
	}
	return out, false, nil
}

func (t *transaction) BlobReferrer(f store.BlobReferrerFilter) (store.Lookup, error) {
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	// The referrer must permit the viewer and contain Within, i.e. carry
	// only owner constraints Within carries (Within.Within(referrer)).
	within := domain.Principal{SessionID: f.Within.SessionID, WorkflowID: f.Within.WorkflowID, TaskID: f.Within.TaskID, AgentID: f.Within.AgentID, Authority: f.Viewer.Authority}
	clause, args, ok := ownerClause("workflow_id", "task_id", "agent_id", f.Viewer, within)
	empty := store.Lookup{Items: []domain.ContextItem{}}
	if !ok || f.Within.SessionID != t.session {
		return empty, nil
	}
	q := "SELECT item_id FROM lookup_blob WHERE session_id=? AND blob_hash=? AND " + clause + " ORDER BY seq, item_id"
	l, _, err := t.scanItems(f.Viewer, 1, q, append([]any{t.session, f.BlobHash}, args...)...)
	l.More, l.Next = false, store.Cursor{}
	return l, err
}

func (t *transaction) CanonicalCandidates(f store.CanonicalFilter) (store.Lookup, error) {
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	if !f.Access.Permits(f.Viewer) {
		return store.Lookup{Items: []domain.ContextItem{}}, nil
	}
	a := f.Access
	l, more, err := t.scanItems(f.Viewer, f.Limit, canonicalSQL, t.session, f.ContentHash, f.TaskID, f.Section, f.DirectiveID, f.Kind, f.Role,
		f.Authority, a.Scope, a.SessionID, a.WorkflowID, a.TaskID, a.AgentID)
	if more {
		return store.Lookup{}, store.ErrLimitExceeded
	}
	l.Next = store.Cursor{}
	return l, err
}

// canonicalSQL is one exact key of lookup_canonical's primary key.
const canonicalSQL = "SELECT item_id FROM lookup_canonical WHERE session_id=? AND content_hash=? AND task_id=? AND section=? AND directive_id=? AND kind=? AND role=?" +
	" AND authority=? AND scope=? AND access_session_id=? AND workflow_id=? AND access_task_id=? AND agent_id=? ORDER BY seq, item_id"

func (t *transaction) CurrentWorking(f store.WorkingFilter) (store.Lookup, error) {
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	if !f.Access.Permits(f.Viewer) {
		return store.Lookup{Items: []domain.ContextItem{}}, nil
	}
	a := f.Access
	l, more, err := t.scanItems(f.Viewer, f.Limit, workingSQL, t.session, f.TaskID, f.Authority, a.Scope, a.SessionID, a.WorkflowID, a.TaskID, a.AgentID)
	if more {
		return store.Lookup{}, store.ErrLimitExceeded
	}
	l.Next = store.Cursor{}
	return l, err
}

// workingSQL is one partition of lookup_working, restricted to items the
// current-version map names (a Working item is always DIRECTIVE
// namespace).
const workingSQL = "SELECT w.item_id FROM lookup_working AS w JOIN directives AS d ON d.session_id=w.session_id AND d.task_id=w.task_id" +
	" AND d.namespace='DIRECTIVE' AND d.directive_id=w.directive_id AND d.boundary_scope=w.scope AND d.boundary_session_id=w.access_session_id" +
	" AND d.boundary_workflow_id=w.workflow_id AND d.boundary_task_id=w.access_task_id AND d.boundary_agent_id=w.agent_id AND d.item_id=w.item_id" +
	" WHERE w.session_id=? AND w.task_id=? AND w.authority=? AND w.scope=? AND w.access_session_id=? AND w.workflow_id=? AND w.access_task_id=? AND w.agent_id=?" +
	" ORDER BY w.seq, w.item_id"

func (t *transaction) SourceItems(f store.SourceFilter) (store.Lookup, error) {
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	clause, args, _ := ownerClause("workflow_id", "task_id", "agent_id", f.Viewer)
	q := "SELECT item_id FROM lookup_source WHERE session_id=? AND rule_version=? AND locator_key=? AND " + clause +
		" AND (seq > ? OR seq = ? AND item_id > ?) ORDER BY seq, item_id"
	c := f.Page.After
	all := append([]any{t.session, domain.LocatorRuleVersion, f.LocatorKey}, args...)
	l, more, err := t.scanItems(f.Viewer, f.Page.Limit, q, append(all, c.Seq, c.Seq, c.ID)...)
	if err != nil {
		return store.Lookup{}, err
	}
	l.More = more
	if len(l.Items) == 0 {
		l.Next = c
	}
	return l, nil
}

func (t *transaction) VisibleReferences(f store.VisibleReferenceFilter) ([]domain.UnresolvedReference, bool, store.Cursor, error) {
	if err := f.Validate(); err != nil {
		return nil, false, store.Cursor{}, err
	}
	clause, args, _ := ownerClause("f_access_workflow_id", "f_access_task_id", "f_access_agent_id", f.Viewer)
	s := schemas["reference"]
	c := f.Page.After
	q := s.selectSQL + " WHERE session_id=? AND f_locator_key=? AND f_rule_version=? AND " + clause +
		" AND (f_seq > ? OR f_seq = ? AND id > ?) ORDER BY f_seq, id LIMIT ?"
	all := append([]any{t.session, f.LocatorKey, domain.LocatorRuleVersion}, args...)
	rows, err := t.conn.QueryContext(t.ctx, q, append(all, c.Seq, c.Seq, c.ID, f.Page.Limit+1)...)
	if err != nil {
		return nil, false, store.Cursor{}, err
	}
	defer rows.Close()
	out, next := []domain.UnresolvedReference{}, c
	for rows.Next() {
		if len(out) == f.Page.Limit {
			return out, true, next, nil
		}
		v, err := s.scan(rows)
		if err != nil {
			return nil, false, store.Cursor{}, fmt.Errorf("unresolved reference: %w", err)
		}
		r := v.Interface().(domain.UnresolvedReference)
		if !r.Access.Permits(f.Viewer) {
			continue
		}
		out = append(out, r)
		next = store.Cursor{Seq: r.Seq, ID: r.ID}
	}
	return out, false, next, rows.Err()
}
