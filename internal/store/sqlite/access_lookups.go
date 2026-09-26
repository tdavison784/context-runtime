package sqlite

import (
	"errors"
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
// classified a duplicate) from the live lookup tables. A duplicate also
// leaves lookup_blob (DUR-2.1): it has its canonical item's exact content
// and boundary, so the canonical item authorizes every reference it could,
// and repeated identical content never grows the table.
func (t *transaction) retireLookups(itemID string, duplicate bool) error {
	tables := []string{"lookup_canonical", "lookup_working", "lookup_source"}
	if duplicate {
		tables = append(tables, "lookup_blob")
	}
	for _, table := range tables {
		if _, err := t.conn.ExecContext(t.ctx, retireLookupSQL(table), t.session, itemID); err != nil {
			return err
		}
	}
	return nil
}

// retireLookupSQL deletes one item's rows from a lookup table through its
// (session_id, item_id) index (migration 0016, SPEC-3.1).
func retireLookupSQL(table string) string {
	return "DELETE FROM " + table + " WHERE session_id=? AND item_id=?"
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

// lookupBatch is the number of index rows a lookup reads per query
// (DUR-2.1): lookups resume from a (seq, item_id) cursor, so their work is
// bounded by what they return plus the rows they skip, never by the number
// of matches in the session.
const lookupBatch = 32

// query builds one batch of a lookup: at most n (seq, item_id) rows
// strictly after the cursor, in (seq, item_id) order.
type query func(after store.Cursor, n int) (string, []any)

// cursorSQL restricts a lookup table query to rows after a cursor and
// orders and limits it.
const cursorSQL = " AND (seq > ? OR seq = ? AND item_id > ?) ORDER BY seq, item_id LIMIT ?"

func cursorArgs(args []any, after store.Cursor, n int) []any {
	return append(args, int64(after.Seq), int64(after.Seq), after.ID, n)
}

// scanLookup reads build's batches from after, loading items until want
// visible verified items are found or the rows run out. Visible items
// failing verification are recorded in Unverified and skipped. found
// reports whether want items were found; Next is the last returned item.
func (t *transaction) scanLookup(viewer domain.Principal, want int, after store.Cursor, build query) (store.Lookup, bool, error) {
	out := store.Lookup{Items: []domain.ContextItem{}, Next: after}
	cursor := after
	for {
		q, args := build(cursor, lookupBatch)
		rows, err := t.conn.QueryContext(t.ctx, q, args...)
		if err != nil {
			return store.Lookup{}, false, err
		}
		var batch []store.Cursor
		for rows.Next() {
			var c store.Cursor
			var seq int64
			if err := rows.Scan(&seq, &c.ID); err != nil {
				rows.Close()
				return store.Lookup{}, false, err
			}
			c.Seq = uint64(seq)
			batch = append(batch, c)
		}
		if err := rows.Close(); err != nil {
			return store.Lookup{}, false, err
		}
		t.lookupRows += len(batch)
		for _, c := range batch {
			cursor = c
			t.lookupLoads++
			it, err := t.Item(c.ID)
			switch {
			case errors.Is(err, domain.ErrIntegrity):
				out.Unverified = append(out.Unverified, c.ID)
				continue
			case err != nil:
				return store.Lookup{}, false, integrityIfMissing(err, "indexed item")
			case !it.Access.Permits(viewer):
				continue
			}
			out.Items = append(out.Items, it)
			if len(out.Items) == want {
				return out, true, nil
			}
			out.Next = c
		}
		if len(batch) < lookupBatch {
			return out, false, nil
		}
	}
}

// blobReferrerQuery reads lookup_blob rows for a blob whose owners permit
// every principal in ps; ok is false when none can.
func blobReferrerQuery(session, hash string, ps ...domain.Principal) (query, bool) {
	clause, args, ok := ownerClause("workflow_id", "task_id", "agent_id", ps...)
	return func(after store.Cursor, n int) (string, []any) {
		return "SELECT seq, item_id FROM lookup_blob WHERE session_id=? AND blob_hash=? AND " + clause + cursorSQL,
			cursorArgs(append([]any{session, hash}, args...), after, n)
	}, ok
}

func (t *transaction) BlobReferrer(f store.BlobReferrerFilter) (store.Lookup, error) {
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	// The referrer must permit the viewer and contain Within, i.e. carry
	// only owner constraints Within carries (Within.Within(referrer)).
	within := domain.Principal{SessionID: f.Within.SessionID, WorkflowID: f.Within.WorkflowID, TaskID: f.Within.TaskID, AgentID: f.Within.AgentID, Authority: f.Viewer.Authority}
	build, ok := blobReferrerQuery(t.session, f.BlobHash, f.Viewer, within)
	if !ok || f.Within.SessionID != t.session {
		return store.Lookup{Items: []domain.ContextItem{}}, nil
	}
	l, _, err := t.scanLookup(f.Viewer, 1, store.Cursor{}, build)
	l.Next = store.Cursor{}
	return l, err
}

// canonicalQuery reads one exact key of lookup_canonical's primary key.
func canonicalQuery(session string, f store.CanonicalFilter) query {
	a := f.Access
	return func(after store.Cursor, n int) (string, []any) {
		return "SELECT seq, item_id FROM lookup_canonical WHERE session_id=? AND content_hash=? AND task_id=? AND section=? AND directive_id=? AND kind=? AND role=?" +
				" AND authority=? AND scope=? AND access_session_id=? AND workflow_id=? AND access_task_id=? AND agent_id=?" + cursorSQL,
			cursorArgs([]any{session, f.ContentHash, f.TaskID, f.Section, f.DirectiveID, f.Kind, f.Role, f.Authority, a.Scope, a.SessionID, a.WorkflowID, a.TaskID, a.AgentID}, after, n)
	}
}

func (t *transaction) CanonicalCandidates(f store.CanonicalFilter) (store.Lookup, error) {
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	if !f.Access.Permits(f.Viewer) {
		return store.Lookup{Items: []domain.ContextItem{}}, nil
	}
	l, over, err := t.scanLookup(f.Viewer, f.Limit+1, store.Cursor{}, canonicalQuery(t.session, f))
	if over {
		return store.Lookup{}, store.ErrLimitExceeded
	}
	l.Next = store.Cursor{}
	return l, err
}

// workingQuery reads one partition of lookup_working, restricted to items
// the current-version map names (a Working item is always DIRECTIVE
// namespace).
func workingQuery(session string, f store.WorkingFilter) query {
	a := f.Access
	return func(after store.Cursor, n int) (string, []any) {
		return "SELECT w.seq, w.item_id FROM lookup_working AS w JOIN directives AS d ON d.session_id=w.session_id AND d.task_id=w.task_id" +
				" AND d.namespace='DIRECTIVE' AND d.directive_id=w.directive_id AND d.boundary_scope=w.scope AND d.boundary_session_id=w.access_session_id" +
				" AND d.boundary_workflow_id=w.workflow_id AND d.boundary_task_id=w.access_task_id AND d.boundary_agent_id=w.agent_id AND d.item_id=w.item_id" +
				" WHERE w.session_id=? AND w.task_id=? AND w.authority=? AND w.scope=? AND w.access_session_id=? AND w.workflow_id=? AND w.access_task_id=? AND w.agent_id=?" +
				" AND (w.seq > ? OR w.seq = ? AND w.item_id > ?) ORDER BY w.seq, w.item_id LIMIT ?",
			cursorArgs([]any{session, f.TaskID, f.Authority, a.Scope, a.SessionID, a.WorkflowID, a.TaskID, a.AgentID}, after, n)
	}
}

func (t *transaction) CurrentWorking(f store.WorkingFilter) (store.Lookup, error) {
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	if !f.Access.Permits(f.Viewer) {
		return store.Lookup{Items: []domain.ContextItem{}}, nil
	}
	l, over, err := t.scanLookup(f.Viewer, f.Limit+1, store.Cursor{}, workingQuery(t.session, f))
	if over {
		return store.Lookup{}, store.ErrLimitExceeded
	}
	l.Next = store.Cursor{}
	return l, err
}

// sourceItemsQuery reads lookup_source rows for a locator key whose owners
// permit the viewer.
func sourceItemsQuery(session, key string, viewer domain.Principal) query {
	clause, args, _ := ownerClause("workflow_id", "task_id", "agent_id", viewer)
	return func(after store.Cursor, n int) (string, []any) {
		return "SELECT seq, item_id FROM lookup_source WHERE session_id=? AND rule_version=? AND locator_key=? AND " + clause + cursorSQL,
			cursorArgs(append([]any{session, domain.LocatorRuleVersion, key}, args...), after, n)
	}
}

func (t *transaction) SourceItems(f store.SourceFilter) (store.Lookup, error) {
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	l, more, err := t.scanLookup(f.Viewer, f.Page.Limit+1, f.Page.After, sourceItemsQuery(t.session, f.LocatorKey, f.Viewer))
	if err != nil {
		return store.Lookup{}, err
	}
	if more { // the extra item only proves more remain
		l.Items = l.Items[:f.Page.Limit]
		l.More = true
	}
	return l, nil
}

// visibleReferencesQuery reads at most n references for a locator key under
// the current rule whose owners permit the viewer, after a cursor.
func visibleReferencesQuery(session, key string, viewer domain.Principal, after store.Cursor, n int) (string, []any) {
	clause, args, _ := ownerClause("f_access_workflow_id", "f_access_task_id", "f_access_agent_id", viewer)
	return schemas["reference"].selectSQL + " WHERE session_id=? AND f_locator_key=? AND f_rule_version=? AND " + clause +
			" AND (f_seq > ? OR f_seq = ? AND id > ?) ORDER BY f_seq, id LIMIT ?",
		append(append([]any{session, key, domain.LocatorRuleVersion}, args...), int64(after.Seq), int64(after.Seq), after.ID, n)
}

func (t *transaction) VisibleReferences(f store.VisibleReferenceFilter) ([]domain.UnresolvedReference, bool, store.Cursor, error) {
	if err := f.Validate(); err != nil {
		return nil, false, store.Cursor{}, err
	}
	c := f.Page.After
	q, args := visibleReferencesQuery(t.session, f.LocatorKey, f.Viewer, c, f.Page.Limit+1)
	refs, err := queryRecords[domain.UnresolvedReference](t, "reference", q, args...)
	if err != nil {
		return nil, false, store.Cursor{}, err
	}
	out, next := []domain.UnresolvedReference{}, c
	for _, r := range refs {
		if !r.Access.Permits(f.Viewer) {
			continue
		}
		if len(out) == f.Page.Limit {
			return out, true, next, nil
		}
		out = append(out, r)
		next = store.Cursor{Seq: r.Seq, ID: r.ID}
	}
	return out, false, next, nil
}
