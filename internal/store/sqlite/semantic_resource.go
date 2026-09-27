package sqlite

import (
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// pathStateRow is a path's current content, keyed by its canonical
// resource locator key (P3-19).
type pathStateRow struct {
	SessionID  string
	LocatorKey string
	State      domain.ResourcePathState
}

// SemanticSeq places the row's sequence in the TargetCall sharing check
// (P3-1, SPEC-1.4).
func (r pathStateRow) SemanticSeq() uint64 { return r.State.Seq }

// subjectStateRow is a subject's current state in one (task, boundary)
// partition, keyed by that partition; Resource and FirstSeq place it in
// the by-resource index in first-filing order (P3-22).
type subjectStateRow struct {
	SessionID string
	Key       string
	Resource  string
	FirstSeq  uint64
	State     domain.SubjectState
}

// SemanticSeq places the row's sequence in the TargetCall sharing check
// (P3-1, SPEC-1.4).
func (r subjectStateRow) SemanticSeq() uint64 { return r.State.Seq }

func subjectPartitionKey(subject, task string, a domain.AccessBoundary) string {
	parts := []string{subject, task, string(a.Scope), a.SessionID, a.WorkflowID, a.TaskID, a.AgentID}
	for i, p := range parts {
		parts[i] = hex.EncodeToString([]byte(p))
	}
	return strings.Join(parts, ".")
}

// closingObservation is store.ClosesRun over rec_observation columns. It
// is also the predicate of migration 0030's partial unique index.
const closingObservation = "(f_outcome IN ('ERROR','TIMEOUT','CANCELLED') OR f_completeness='COMPLETE' AND f_outcome IN ('PASS','FAIL'))"

// subjectResource is the resource a subject's target lives in.
func subjectResource(s domain.ObservationSubject) string {
	if s.Target.Tests != nil {
		return s.Target.Tests.ResourceID
	}
	if s.Target.File != nil {
		return s.Target.File.Locator.ResourceID
	}
	return ""
}

func conflict(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), domain.ErrVersionConflict)
}

// --- Resources (rules as in the memory store) ---

func (s semTx) InsertResourceBinding(b domain.ResourceBinding) error {
	t := s.t
	if err := t.companion(b.SemanticMeta, b.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("resource_binding", b.ResourceID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "resource binding", b.ResourceID))
	}
	var prior domain.ResourceBinding
	if err := t.getWhere("resource_binding", "f_id=?", &prior, b.ID); err == nil {
		return immutable("resource binding", b.ID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return t.put("resource_binding", b.ResourceID, 0, b, false)
}

func (s semRead) ResourceBinding(resourceID string) (domain.ResourceBinding, error) {
	var b domain.ResourceBinding
	return b, s.t.get("resource_binding", resourceID, 0, &b)
}

func (s semTx) InsertResourceUpdate(u domain.ResourceUpdate) error {
	t := s.t
	if err := t.companion(u.SemanticMeta, u.Validate); err != nil {
		return err
	}
	var b domain.ResourceBinding
	if err := t.get("resource_binding", u.ResourceID, 0, &b); err != nil {
		return notStored(err, "resource update %s: resource %s is not registered", u.ID, u.ResourceID)
	}
	if u.Reporter != b.Reporter {
		return invalid("resource update %s: reporter is not the resource's registered reporter", u.ID)
	}
	if ok, err := t.exists("resource_update", u.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "resource update", u.ID))
	}
	var prior domain.ResourceUpdate
	if err := t.getWhere("resource_update", "f_resource_id=? AND f_request_id=?", &prior, u.ResourceID, u.RequestID); err == nil {
		return immutable("resource update request", u.RequestID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return t.atomic(func() error {
		if err := t.put("resource_update", u.ID, 0, u, false); err != nil {
			return err
		}
		return t.indexUpdatePaths(u)
	})
}

func (s semRead) ResourceUpdate(id string) (domain.ResourceUpdate, error) {
	var u domain.ResourceUpdate
	return u, s.t.get("resource_update", id, 0, &u)
}

func (s semRead) ResourceUpdates(resourceID string, p store.Page) (store.ResultPage[domain.ResourceUpdate], error) {
	return pageQuery[domain.ResourceUpdate](s.t, "resource_update", "f_resource_id=?", []any{resourceID}, "f_seq", p, false, nil)
}

func (s semTx) PutResourceState(st domain.ResourceState, expectedRevision uint64) (domain.ResourceState, error) {
	t := s.t
	if err := t.companion(st.SemanticMeta, st.Validate); err != nil {
		return domain.ResourceState{}, err
	}
	var cur domain.ResourceState
	err := t.get("resource_state", st.ResourceID, 0, &cur)
	found := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.ResourceState{}, err
	}
	if cur.Revision != expectedRevision {
		return domain.ResourceState{}, conflict("resource state %s: revision %d, expected %d", st.ResourceID, cur.Revision, expectedRevision)
	}
	if found && cur.ID != st.ID {
		return domain.ResourceState{}, immutable("resource state", st.ResourceID)
	}
	var b domain.ResourceBinding
	if err := t.get("resource_binding", st.ResourceID, 0, &b); err != nil || b.ID != st.BindingID {
		return domain.ResourceState{}, notStored(errors.Join(err, domain.ErrNotFound), "resource state %s: binding %s is not the resource's", st.ResourceID, st.BindingID)
	}
	var u domain.ResourceUpdate
	if err := t.get("resource_update", st.LastUpdateID, 0, &u); err != nil || u.ResourceID != st.ResourceID || u.ResultingAuthoritativeRevision != st.AuthoritativeRevision ||
		u.Freshness != st.Freshness || u.WorkspaceFingerprint != st.WorkspaceFingerprint {
		return domain.ResourceState{}, notStored(errors.Join(err, domain.ErrNotFound), "resource state %s: state is not the result of stored update %s", st.ResourceID, st.LastUpdateID)
	}
	if found && st.AuthoritativeRevision <= cur.AuthoritativeRevision {
		return domain.ResourceState{}, transition("resource state %s: revision %d does not advance %d", st.ResourceID, st.AuthoritativeRevision, cur.AuthoritativeRevision)
	}
	st.Revision = expectedRevision + 1
	if err := t.put("resource_state", st.ResourceID, 0, st, found); err != nil {
		return domain.ResourceState{}, err
	}
	t.semanticSeqRecord = true
	return st, nil
}

func (s semRead) ResourceState(resourceID string) (domain.ResourceState, error) {
	var st domain.ResourceState
	return st, s.t.get("resource_state", resourceID, 0, &st)
}

func (s semTx) PutResourcePathState(st domain.ResourcePathState, expectedRevision uint64) (domain.ResourcePathState, error) {
	t := s.t
	if err := t.companion(st.SemanticMeta, st.Validate); err != nil {
		return domain.ResourcePathState{}, err
	}
	key, err := st.Locator.Key()
	if err != nil {
		return domain.ResourcePathState{}, err
	}
	var row pathStateRow
	err = t.get("path_state", key, 0, &row)
	found, cur := err == nil, row.State
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.ResourcePathState{}, err
	}
	if cur.Revision != expectedRevision {
		return domain.ResourcePathState{}, conflict("path state %s: revision %d, expected %d", st.Locator.Path, cur.Revision, expectedRevision)
	}
	if found && cur.ID != st.ID {
		return domain.ResourcePathState{}, immutable("path state", st.Locator.Path)
	}
	var u domain.ResourceUpdate
	if err := t.get("resource_update", st.ResourceUpdateID, 0, &u); err != nil || u.ResourceID != st.Locator.ResourceID || u.ResultingAuthoritativeRevision != st.ResourceRevision {
		return domain.ResourcePathState{}, notStored(errors.Join(err, domain.ErrNotFound), "path state %s: not the result of stored update %s", st.Locator.Path, st.ResourceUpdateID)
	}
	if found && st.ResourceRevision <= cur.ResourceRevision {
		return domain.ResourcePathState{}, transition("path state %s: revision %d does not advance %d", st.Locator.Path, st.ResourceRevision, cur.ResourceRevision)
	}
	st.Revision = expectedRevision + 1
	if err := t.put("path_state", key, 0, pathStateRow{SessionID: t.session, LocatorKey: key, State: st}, found); err != nil {
		return domain.ResourcePathState{}, err
	}
	t.semanticSeqRecord = true
	return st, nil
}

func (s semRead) ResourcePathState(locator domain.ResourceLocator) (domain.ResourcePathState, error) {
	key, err := locator.Key()
	if err != nil {
		return domain.ResourcePathState{}, invalid("path state: %v", err)
	}
	var row pathStateRow
	return row.State, s.t.get("path_state", key, 0, &row)
}

// --- Workspace bindings ---

func (s semTx) InsertWorkspaceBinding(b domain.WorkspaceBinding) error {
	t := s.t
	if err := t.companion(b.SemanticMeta, b.Validate); err != nil {
		return err
	}
	var prior domain.WorkspaceBinding
	err := t.get("workspace_binding", b.ID, int(b.Version), &prior)
	if err == nil {
		return immutable("workspace binding", fmt.Sprintf("%s/%d", b.ID, b.Version))
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	var last, lastSeq uint64
	if err := t.conn.QueryRowContext(t.ctx, "SELECT COALESCE(MAX(subkey),0), COALESCE(MAX(f_seq),0) FROM rec_workspace_binding WHERE session_id=? AND id=?", t.session, b.ID).Scan(&last, &lastSeq); err != nil {
		return err
	}
	if b.Version != last+1 || last > 0 && b.Seq <= lastSeq {
		return invalid("workspace binding %s: version %d, want %d at a later sequence", b.ID, b.Version, last+1)
	}
	if ok, err := t.exists("resource_binding", b.ResourceID); err != nil || !ok {
		return errors.Join(err, invalidIf(!ok, "workspace binding %s: resource %s is not registered", b.ID, b.ResourceID))
	}
	return t.put("workspace_binding", b.ID, int(b.Version), b, false)
}

func (s semRead) WorkspaceBinding(ref domain.WorkspaceBindingRef) (domain.WorkspaceBinding, error) {
	var b domain.WorkspaceBinding
	return b, s.t.get("workspace_binding", ref.ID, int(ref.Version), &b)
}

func (s semRead) WorkspaceBindingsByContext(sourceItemID, taskID, conversationID string, p store.Page) (store.ResultPage[domain.WorkspaceBinding], error) {
	var kinds []domain.WorkspaceSourceContext
	for _, c := range []domain.WorkspaceSourceContext{{Kind: domain.WorkspaceSource, ID: sourceItemID}, {Kind: domain.WorkspaceTask, ID: taskID}, {Kind: domain.WorkspaceConversation, ID: conversationID}} {
		if c.ID != "" {
			kinds = append(kinds, c)
		}
	}
	if len(kinds) != 1 {
		return store.ResultPage[domain.WorkspaceBinding]{}, invalid("workspace bindings: exactly one context required")
	}
	return pageQuery[domain.WorkspaceBinding](s.t, "workspace_binding", "f_context_kind=? AND f_context_id=?", []any{string(kinds[0].Kind), kinds[0].ID}, "f_seq", p, false, nil)
}

// --- Runs and observations ---

func (s semTx) InsertObservationRun(run domain.ObservationRun) error {
	t := s.t
	if err := t.companion(run.SemanticMeta, run.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("observation_run", run.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "observation run", run.ID))
	}
	// (subject, Ordinal) is the run's second key (SEC-1.13), backed by the
	// unique index of migration 0029.
	var other domain.ObservationRun
	if err := t.getWhere("observation_run", "f_subject_key=? AND f_ordinal=?", &other, run.SubjectKey, run.Ordinal); err == nil {
		return invalid("observation run %s: subject ordinal %d is already run %s", run.ID, run.Ordinal, other.ID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if ok, err := t.exists("task", run.TaskID); err != nil || !ok {
		return errors.Join(err, invalidIf(!ok, "observation run %s: task %s is not stored", run.ID, run.TaskID))
	}
	var b domain.WorkspaceBinding
	if err := t.get("workspace_binding", run.Binding.ID, int(run.Binding.Version), &b); err != nil || b.ResourceID != subjectResource(run.Subject) {
		return notStored(errors.Join(err, domain.ErrNotFound), "observation run %s: binding is not a stored binding of its subject's resource", run.ID)
	}
	return t.put("observation_run", run.ID, 0, run, false)
}

func (s semTx) InsertObservation(o domain.ObservationRecord) error {
	t := s.t
	if err := t.companion(o.SemanticMeta, o.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("observation", o.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "observation", o.ID))
	}
	var run domain.ObservationRun
	if err := t.get("observation_run", o.RunID, 0, &run); err != nil || run.ExecutionID != o.ExecutionID || run.SubjectKey != o.SubjectKey ||
		run.Subject.Family != o.Family || run.Binding != o.Binding || run.Reporter != o.Reporter {
		return notStored(errors.Join(err, domain.ErrNotFound), "observation %s: not an observation of its run's exact execution", o.ID)
	}
	ev, err := t.loadItem(o.EvidenceItemID, false)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if err != nil || ev.Authority != domain.AuthorityTool || ev.Access != o.Access {
		return invalid("observation %s: evidence is not a stored TOOL item in its boundary", o.ID)
	}
	// A run closes once (DUR-1.1, G1); migration 0030's partial unique
	// index backs this check.
	var closed domain.ObservationRecord
	if err := t.getWhere("observation", "f_run_id=? AND "+closingObservation, &closed, o.RunID); err == nil {
		return fmt.Errorf("observation %s: run %s already closed with %s: %w", o.ID, o.RunID, closed.ID, domain.ErrInvalidTransition)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return t.put("observation", o.ID, 0, o, false)
}

func (s semRead) Observation(id string) (domain.ObservationRecord, error) {
	var o domain.ObservationRecord
	return o, s.t.get("observation", id, 0, &o)
}

func (s semRead) ObservationRun(id string) (domain.ObservationRun, error) {
	var r domain.ObservationRun
	return r, s.t.get("observation_run", id, 0, &r)
}

func (s semRead) RunsBySubject(subjectKey string, p store.Page) (store.ResultPage[domain.ObservationRun], error) {
	return pageQuery[domain.ObservationRun](s.t, "observation_run", "f_subject_key=?", []any{subjectKey}, "f_seq", p, false, nil)
}

func (s semRead) ObservationsByRun(runID string, p store.Page) (store.ResultPage[domain.ObservationRecord], error) {
	return pageQuery[domain.ObservationRecord](s.t, "observation", "f_run_id=?", []any{runID}, "f_seq", p, false, nil)
}

func (s semTx) PutSubjectState(st domain.SubjectState, expectedRevision uint64, causeID string) (domain.SubjectState, error) {
	t := s.t
	if err := t.companion(st.SemanticMeta, st.Validate); err != nil {
		return domain.SubjectState{}, err
	}
	key := subjectPartitionKey(st.SubjectKey, st.TaskID, st.Access)
	var row subjectStateRow
	err := t.get("subject_state", key, 0, &row)
	found, cur := err == nil, row.State
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.SubjectState{}, err
	}
	if cur.Revision != expectedRevision {
		return domain.SubjectState{}, conflict("subject state %s: revision %d, expected %d", st.SubjectKey, cur.Revision, expectedRevision)
	}
	if found && cur.ID != st.ID {
		return domain.SubjectState{}, immutable("subject state", st.ID)
	}
	if !found {
		var other subjectStateRow
		if err := t.getWhere("subject_state", "f_state_semantic_meta_id=?", &other, st.ID); err == nil {
			return domain.SubjectState{}, immutable("subject state", st.ID)
		} else if !errors.Is(err, domain.ErrNotFound) {
			return domain.SubjectState{}, err
		}
	}
	isObs, err := t.exists("observation", causeID)
	if err != nil {
		return domain.SubjectState{}, err
	}
	isUpd, err := t.exists("resource_update", causeID)
	if err != nil {
		return domain.SubjectState{}, err
	}
	if !isObs && !isUpd {
		return domain.SubjectState{}, invalid("subject state %s: cause %s is not a stored observation or resource update", st.SubjectKey, causeID)
	}
	var o domain.ObservationRecord
	if err := t.get("observation", st.ObservationID, 0, &o); err != nil || o.SubjectKey != st.SubjectKey {
		return domain.SubjectState{}, notStored(errors.Join(err, domain.ErrNotFound), "subject state %s: observation %s is not of its subject", st.SubjectKey, st.ObservationID)
	}
	var run domain.ObservationRun
	if err := t.get("observation_run", o.RunID, 0, &run); err != nil {
		return domain.SubjectState{}, err
	}
	if run.Ordinal != st.AcceptedOrdinal {
		return domain.SubjectState{}, invalid("subject state %s: watermark is not its observation's run ordinal", st.SubjectKey)
	}
	it, err := t.loadItem(st.CurrentItemID, false)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.SubjectState{}, err
	}
	if err != nil || it.Authority != domain.AuthorityTool {
		return domain.SubjectState{}, invalid("subject state %s: current item is not stored TOOL content", st.SubjectKey)
	}
	if found && st.AcceptedOrdinal < cur.AcceptedOrdinal {
		return domain.SubjectState{}, transition("subject state %s: watermark %d is below %d", st.SubjectKey, st.AcceptedOrdinal, cur.AcceptedOrdinal)
	}
	st.Revision = expectedRevision + 1
	next := subjectStateRow{SessionID: t.session, Key: key, Resource: subjectResource(run.Subject), FirstSeq: st.Seq, State: st}
	if found {
		next.Resource, next.FirstSeq = row.Resource, row.FirstSeq
	}
	if err := t.put("subject_state", key, 0, next, found); err != nil {
		return domain.SubjectState{}, err
	}
	t.semanticSeqRecord = true
	return st, nil
}

func (s semRead) SubjectState(subject, taskID string, access domain.AccessBoundary) (domain.SubjectState, error) {
	var row subjectStateRow
	return row.State, s.t.get("subject_state", subjectPartitionKey(subject, taskID, access), 0, &row)
}

// SubjectStatesByResource pages a resource's subject states in first-filing
// (Seq, ID) order.
func (s semRead) SubjectStatesByResource(resourceID string, p store.Page) (store.ResultPage[domain.SubjectState], error) {
	t := s.t
	var out store.ResultPage[domain.SubjectState]
	if p.Limit <= 0 {
		return out, invalid("page limit must be positive")
	}
	sc, err := schemaFor("subject_state")
	if err != nil {
		return out, err
	}
	// Only CURRENT states, through migration 0032's partial index (G2).
	rows, err := t.query(sc.selectSQL+" WHERE session_id=? AND f_resource=? AND f_state_applicability='CURRENT' AND (f_first_seq>? OR (f_first_seq=? AND f_state_semantic_meta_id>?)) ORDER BY f_first_seq, f_state_semantic_meta_id LIMIT ?",
		t.session, resourceID, p.After.Seq, p.After.Seq, p.After.ID, p.Limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := sc.scan(rows)
		if err != nil {
			return out, fmt.Errorf("subject state: %w", err)
		}
		row := v.Interface().(subjectStateRow)
		if len(out.Records) == p.Limit {
			out.More = true
			break
		}
		out.Records = append(out.Records, row.State)
		out.Next = store.Cursor{Seq: row.FirstSeq, ID: row.State.ID}
	}
	return out, rows.Err()
}

// updatePathKey is a lookup_resource_update_path key (migration 0033).
func updatePathKey(p string) string { return "path:" + hex.EncodeToString([]byte(p)) }

// indexUpdatePaths files u under every path it may change.
func (t *transaction) indexUpdatePaths(u domain.ResourceUpdate) error {
	var keys []string
	if u.AllPaths {
		keys = append(keys, "all")
	}
	for _, p := range u.ChangedPaths {
		keys = append(keys, updatePathKey(p))
	}
	for _, k := range keys {
		if _, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_resource_update_path(session_id,resource_id,path_key,seq,update_id) VALUES(?,?,?,?,?)",
			t.session, u.ResourceID, k, u.Seq, u.ID); err != nil {
			return err
		}
	}
	return nil
}

// ResourceUpdatesAffectingPath implements store.ResourceReader over
// migration 0033's index: ALL-paths updates, and updates naming path or an
// ancestor directory, in (Seq, ID) order.
func (s semRead) ResourceUpdatesAffectingPath(resourceID, path string, p store.Page) (store.ResultPage[domain.ResourceUpdate], error) {
	t := s.t
	var out store.ResultPage[domain.ResourceUpdate]
	if p.Limit <= 0 {
		return out, invalid("page limit must be positive")
	}
	affect, err := store.PathAffectKeys(path)
	if err != nil {
		return out, err
	}
	args := []any{t.session, resourceID, "all"}
	for _, k := range affect {
		args = append(args, updatePathKey(k))
	}
	args = append(args, p.After.Seq, p.After.Seq, p.After.ID, p.Limit+1)
	rows, err := t.query("SELECT DISTINCT seq, update_id FROM lookup_resource_update_path WHERE session_id=? AND resource_id=? AND path_key IN (?"+
		strings.Repeat(",?", len(affect))+") AND (seq>? OR (seq=? AND update_id>?)) ORDER BY seq, update_id LIMIT ?", args...)
	if err != nil {
		return out, err
	}
	var refs []store.Cursor
	for rows.Next() {
		var c store.Cursor
		if err := rows.Scan(&c.Seq, &c.ID); err != nil {
			rows.Close()
			return out, err
		}
		refs = append(refs, c)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	if len(refs) > p.Limit {
		refs, out.More = refs[:p.Limit], true
	}
	for _, c := range refs {
		var u domain.ResourceUpdate
		if err := t.get("resource_update", c.ID, 0, &u); err != nil {
			return out, fmt.Errorf("%w: path index names missing update %s", domain.ErrIntegrity, c.ID)
		}
		out.Records = append(out.Records, u)
		out.Next = c
	}
	return out, nil
}

// LatestResourceUpdateAffectingPath implements store.ResourceReader: one
// newest-first LIMIT 1 search of migration 0033's index per affecting key
// (ALL and each path component), O(depth) and independent of history.
func (s semRead) LatestResourceUpdateAffectingPath(resourceID, path string) (domain.ResourceUpdate, error) {
	t := s.t
	affect, err := store.PathAffectKeys(path)
	if err != nil {
		return domain.ResourceUpdate{}, err
	}
	keys := []string{"all"}
	for _, k := range affect {
		keys = append(keys, updatePathKey(k))
	}
	var best store.Cursor
	for _, k := range keys {
		var c store.Cursor
		err := t.conn.QueryRowContext(t.ctx, "SELECT seq, update_id FROM lookup_resource_update_path WHERE session_id=? AND resource_id=? AND path_key=? ORDER BY seq DESC, update_id DESC LIMIT 1",
			t.session, resourceID, k).Scan(&c.Seq, &c.ID)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return domain.ResourceUpdate{}, err
		}
		if c.Seq > best.Seq || c.Seq == best.Seq && c.ID > best.ID {
			best = c
		}
	}
	if best.ID == "" {
		return domain.ResourceUpdate{}, fmt.Errorf("resource update affecting %s: %w", path, domain.ErrNotFound)
	}
	var u domain.ResourceUpdate
	if err := t.get("resource_update", best.ID, 0, &u); err != nil {
		return domain.ResourceUpdate{}, fmt.Errorf("%w: path index names missing update %s", domain.ErrIntegrity, best.ID)
	}
	return u, nil
}

// ClosingObservation implements store.ResourceReader: a keyed search of
// migration 0030's partial unique index, which holds at most one row per
// run.
func (s semRead) ClosingObservation(runID string) (domain.ObservationRecord, error) {
	var o domain.ObservationRecord
	return o, s.t.getWhere("observation", "f_run_id=? AND "+closingObservation, &o, runID)
}
