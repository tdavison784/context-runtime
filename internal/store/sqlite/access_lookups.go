package sqlite

import (
	"cmp"
	"errors"
	"slices"
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
// (session_id, item_id) index (migration 0017, SPEC-3.1).
func retireLookupSQL(table string) string {
	return "DELETE FROM " + table + " WHERE session_id=? AND item_id=?"
}

// ownerCombo is one exact (workflow, task, agent) owner value of a lookup
// row.
type ownerCombo struct{ workflowID, taskID, agentID string }

// ownerCombos lists the owner values whose boundary permits every principal
// in ps (the intersection of their permitted owners), at most eight for
// one viewer. Each becomes one exact-key query, so a lookup's cursor seeks
// within an index range instead of sorting a union (SPEC-3.1 item 4).
func ownerCombos(ps ...domain.Principal) []ownerCombo {
	allowed := func(pick func(domain.Principal) string) []string {
		var out []string
		for _, v := range []string{"", pick(ps[0])} {
			ok := true
			for _, p := range ps {
				ok = ok && (v == "" || v == pick(p))
			}
			if ok && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	var out []ownerCombo
	for _, w := range allowed(func(p domain.Principal) string { return p.WorkflowID }) {
		for _, tk := range allowed(func(p domain.Principal) string { return p.TaskID }) {
			for _, a := range allowed(func(p domain.Principal) string { return p.AgentID }) {
				out = append(out, ownerCombo{w, tk, a})
			}
		}
	}
	return out
}

// lookupBatch is the number of index rows a lookup reads per query
// (DUR-2.1): lookups resume from a (seq, item_id) cursor that seeks within
// an index range, so their work is bounded by what they return plus the
// rows they skip, never by the number of matches in the session.
const lookupBatch = 32

// query builds one batch of a lookup: at most n (seq, item_id) rows
// strictly after the cursor, in (seq, item_id) order.
type query func(after store.Cursor, n int) (string, []any)

// cursorSQL seeks a lookup table query past a cursor with a row-value
// range, which SQLite applies inside the index search, then orders and
// limits it.
const cursorSQL = " AND (seq, item_id) > (?, ?) ORDER BY seq, item_id LIMIT ?"

func cursorArgs(args []any, after store.Cursor, n int) []any {
	return append(args, int64(after.Seq), after.ID, n)
}

// nextRows runs one batch of every query after cursor and returns the
// first n rows of their merged (seq, item_id) order, and whether every
// query ran out (so no rows remain past those returned).
func (t *transaction) nextRows(qs []query, after store.Cursor, n int) ([]store.Cursor, bool, error) {
	var all []store.Cursor
	exhausted := true
	for _, build := range qs {
		q, args := build(after, n)
		rows, err := t.query(q, args...)
		if err != nil {
			return nil, false, err
		}
		got := 0
		for rows.Next() {
			var c store.Cursor
			var seq int64
			if err := rows.Scan(&seq, &c.ID); err != nil {
				rows.Close()
				return nil, false, err
			}
			c.Seq = uint64(seq)
			all = append(all, c)
			got++
		}
		if err := rows.Close(); err != nil {
			return nil, false, err
		}
		t.lookupRows += got
		if got == n {
			exhausted = false
		}
	}
	slices.SortFunc(all, func(a, b store.Cursor) int { return cmp.Or(cmp.Compare(a.Seq, b.Seq), strings.Compare(a.ID, b.ID)) })
	if len(all) > n {
		all, exhausted = all[:n], false
	}
	return all, exhausted, nil
}

// scanLookup reads the queries' merged rows from after in batches, loading
// items until want visible verified items are found or the rows run out.
// Visible items failing verification are recorded in Unverified, with
// their positions in unverifiedAt, and skipped. found reports whether want
// items were found; Next is the last returned item.
func (t *transaction) scanLookup(viewer domain.Principal, want int, after store.Cursor, qs ...query) (out store.Lookup, found bool, unverifiedAt []store.Cursor, err error) {
	out = store.Lookup{Items: []domain.ContextItem{}, Next: after}
	cursor := after
	for {
		batch, exhausted, err := t.nextRows(qs, cursor, lookupBatch)
		if err != nil {
			return store.Lookup{}, false, nil, err
		}
		for _, c := range batch {
			cursor = c
			t.lookupLoads++
			it, err := t.loadItem(c.ID, false)
			switch {
			case errors.Is(err, domain.ErrIntegrity):
				out.Unverified = append(out.Unverified, c.ID)
				unverifiedAt = append(unverifiedAt, c)
				continue
			case err != nil:
				return store.Lookup{}, false, nil, integrityIfMissing(err, "indexed item")
			case !it.Access.Permits(viewer):
				continue
			}
			out.Items = append(out.Items, it)
			if len(out.Items) == want {
				return out, true, unverifiedAt, nil
			}
			out.Next = c
		}
		if exhausted {
			return out, false, unverifiedAt, nil
		}
	}
}

// blobReferrerQueries reads lookup_blob rows for a blob, one query per
// owner combination.
func blobReferrerQueries(session, hash string, combos []ownerCombo) []query {
	qs := make([]query, len(combos))
	for i, o := range combos {
		qs[i] = func(after store.Cursor, n int) (string, []any) {
			return "SELECT seq, item_id FROM lookup_blob WHERE session_id=? AND blob_hash=? AND workflow_id=? AND task_id=? AND agent_id=?" + cursorSQL,
				cursorArgs([]any{session, hash, o.workflowID, o.taskID, o.agentID}, after, n)
		}
	}
	return qs
}

func (t *transaction) BlobReferrer(f store.BlobReferrerFilter) (store.Lookup, error) {
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	if f.Within.SessionID != t.session {
		return store.Lookup{Items: []domain.ContextItem{}}, nil
	}
	// The referrer must permit the viewer and contain Within, i.e. carry
	// only owner constraints Within carries (Within.Within(referrer)).
	within := domain.Principal{SessionID: f.Within.SessionID, WorkflowID: f.Within.WorkflowID, TaskID: f.Within.TaskID, AgentID: f.Within.AgentID, Authority: f.Viewer.Authority}
	l, _, _, err := t.scanLookup(f.Viewer, 1, store.Cursor{}, blobReferrerQueries(t.session, f.BlobHash, ownerCombos(f.Viewer, within))...)
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
	l, over, _, err := t.scanLookup(f.Viewer, f.Limit+1, store.Cursor{}, canonicalQuery(t.session, f))
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
				" AND (w.seq, w.item_id) > (?, ?) ORDER BY w.seq, w.item_id LIMIT ?",
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
	l, over, _, err := t.scanLookup(f.Viewer, f.Limit+1, store.Cursor{}, workingQuery(t.session, f))
	if over {
		return store.Lookup{}, store.ErrLimitExceeded
	}
	l.Next = store.Cursor{}
	return l, err
}

// sourceItemsQueries reads lookup_source rows for a locator key, one query
// per owner combination.
func sourceItemsQueries(session, key string, combos []ownerCombo) []query {
	qs := make([]query, len(combos))
	for i, o := range combos {
		qs[i] = func(after store.Cursor, n int) (string, []any) {
			return "SELECT seq, item_id FROM lookup_source WHERE session_id=? AND rule_version=? AND locator_key=? AND workflow_id=? AND task_id=? AND agent_id=?" + cursorSQL,
				cursorArgs([]any{session, domain.LocatorRuleVersion, key, o.workflowID, o.taskID, o.agentID}, after, n)
		}
	}
	return qs
}

func (t *transaction) SourceItems(f store.SourceFilter) (store.Lookup, error) {
	if err := f.Validate(); err != nil {
		return store.Lookup{}, err
	}
	l, more, unverifiedAt, err := t.scanLookup(f.Viewer, f.Page.Limit+1, f.Page.After, sourceItemsQueries(t.session, f.LocatorKey, ownerCombos(f.Viewer))...)
	if err != nil {
		return store.Lookup{}, err
	}
	if more { // the extra item only proves more remain
		l.Items = l.Items[:f.Page.Limit]
		l.More = true
		// Unverified rows past Next belong to the next page, which reads
		// from Next again; report each on one page only (DUR-3.1).
		l.Unverified = nil
		for _, c := range unverifiedAt {
			if !after(c, l.Next) {
				l.Unverified = append(l.Unverified, c.ID)
			}
		}
	}
	return l, nil
}

// visibleReferencesQueries read references for a locator key under the
// current rule, one query per owner combination, returning (f_seq, id).
func visibleReferencesQueries(session, key string, combos []ownerCombo) []query {
	qs := make([]query, len(combos))
	for i, o := range combos {
		qs[i] = func(after store.Cursor, n int) (string, []any) {
			return "SELECT f_seq, id FROM rec_reference WHERE session_id=? AND f_locator_key=? AND f_rule_version=?" +
					" AND f_access_workflow_id=? AND f_access_task_id=? AND f_access_agent_id=? AND (f_seq, id) > (?, ?) ORDER BY f_seq, id LIMIT ?",
				cursorArgs([]any{session, key, domain.LocatorRuleVersion, o.workflowID, o.taskID, o.agentID}, after, n)
		}
	}
	return qs
}

func (t *transaction) VisibleReferences(f store.VisibleReferenceFilter) ([]domain.UnresolvedReference, bool, store.Cursor, error) {
	if err := f.Validate(); err != nil {
		return nil, false, store.Cursor{}, err
	}
	qs := visibleReferencesQueries(t.session, f.LocatorKey, ownerCombos(f.Viewer))
	out, next, cursor := []domain.UnresolvedReference{}, f.Page.After, f.Page.After
	for {
		batch, exhausted, err := t.nextRows(qs, cursor, f.Page.Limit+1)
		if err != nil {
			return nil, false, store.Cursor{}, err
		}
		for _, c := range batch {
			cursor = c
			r, err := t.UnresolvedReference(c.ID)
			if err != nil {
				return nil, false, store.Cursor{}, integrityIfMissing(err, "indexed reference")
			}
			if !r.Access.Permits(f.Viewer) {
				continue
			}
			if len(out) == f.Page.Limit {
				return out, true, next, nil
			}
			out = append(out, r)
			next = c
		}
		if exhausted {
			return out, false, next, nil
		}
	}
}

// after reports whether a is strictly after b in (Seq, ID) order.
func after(a, b store.Cursor) bool {
	return a.Seq > b.Seq || a.Seq == b.Seq && a.ID > b.ID
}
