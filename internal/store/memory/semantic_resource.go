package memory

import (
	"fmt"
	"sort"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Resource registration and ordered reporting (P3-19), workspace bindings
// (P3-20), pre-execution runs, typed observations and the subject-state
// watermark (P3-21/22). Stores enforce registration, exact references,
// monotonic revisions and CAS; services authenticate reporters and prove
// applicability.

type wbKey struct {
	id      string
	version uint64
}

type wsContext struct {
	kind domain.WorkspaceContextKind
	id   string
}

type subjectKey struct {
	subject, task string
	access        domain.AccessBoundary
}

type resRequest struct{ resource, request string }

// resPath keys the path-change index: a ChangedPaths entry of a resource,
// or allPathsKey for an ALL-paths update (G2).
type resPath struct{ resource, path string }

// allPathsKey is never a canonical path.
const allPathsKey = "\x00all"

type resState struct {
	bindings     map[string]domain.ResourceBinding // by resource
	bindingIDs   map[string]string
	updates      map[string]domain.ResourceUpdate
	updByRes     map[string][]seqRef
	updByPath    map[resPath][]seqRef
	updRequests  map[resRequest]string
	states       map[string]domain.ResourceState     // by resource
	paths        map[string]domain.ResourcePathState // by locator key
	wbindings    map[wbKey]domain.WorkspaceBinding
	wbLatest     map[string]uint64
	wbByContext  map[wsContext][]seqRef
	runs         map[string]domain.ObservationRun
	runsBySubj   map[string][]seqRef
	observations map[string]domain.ObservationRecord
	obsByRun     map[string][]seqRef
	runClosing   map[string]string     // run -> closing observation (H2)
	highWater    map[subjectKey]uint64 // partition -> highest complete PASS/FAIL run ordinal (H1)
	subjects     map[subjectKey]domain.SubjectState
	subjByRes    map[string][]seqRef
	subjIDs      map[string]subjectKey
}

func newResState() resState {
	return resState{
		bindings: map[string]domain.ResourceBinding{}, bindingIDs: map[string]string{}, updates: map[string]domain.ResourceUpdate{},
		updByRes: map[string][]seqRef{}, updByPath: map[resPath][]seqRef{}, updRequests: map[resRequest]string{}, states: map[string]domain.ResourceState{},
		paths: map[string]domain.ResourcePathState{}, wbindings: map[wbKey]domain.WorkspaceBinding{}, wbLatest: map[string]uint64{},
		wbByContext: map[wsContext][]seqRef{}, runs: map[string]domain.ObservationRun{}, runsBySubj: map[string][]seqRef{},
		observations: map[string]domain.ObservationRecord{}, obsByRun: map[string][]seqRef{}, runClosing: map[string]string{}, highWater: map[subjectKey]uint64{}, subjects: map[subjectKey]domain.SubjectState{},
		subjByRes: map[string][]seqRef{}, subjIDs: map[string]subjectKey{},
	}
}

type resView struct {
	bindings     table[string, domain.ResourceBinding]
	bindingIDs   table[string, string]
	updates      table[string, domain.ResourceUpdate]
	updByRes     orderedIndex[string]
	updByPath    orderedIndex[resPath]
	updRequests  table[resRequest, string]
	states       table[string, domain.ResourceState]
	paths        table[string, domain.ResourcePathState]
	wbindings    table[wbKey, domain.WorkspaceBinding]
	wbLatest     table[string, uint64]
	wbByContext  orderedIndex[wsContext]
	runs         table[string, domain.ObservationRun]
	runsBySubj   orderedIndex[string]
	observations table[string, domain.ObservationRecord]
	obsByRun     orderedIndex[string]
	runClosing   table[string, string]
	highWater    table[subjectKey, uint64]
	subjects     table[subjectKey, domain.SubjectState]
	subjByRes    orderedIndex[string]
	subjIDs      table[string, subjectKey]
}

func newResView(st *resState, w bool) resView {
	return resView{
		bindings: newTable(st.bindings, w, domain.ResourceBinding.Clone), bindingIDs: newTable(st.bindingIDs, w, same[string]),
		updates: newTable(st.updates, w, domain.ResourceUpdate.Clone), updByRes: newOrderedIndex(st.updByRes, w),
		updByPath:   newOrderedIndex(st.updByPath, w),
		updRequests: newTable(st.updRequests, w, same[string]), states: newTable(st.states, w, domain.ResourceState.Clone),
		paths: newTable(st.paths, w, domain.ResourcePathState.Clone), wbindings: newTable(st.wbindings, w, domain.WorkspaceBinding.Clone),
		wbLatest: newTable(st.wbLatest, w, same[uint64]), wbByContext: newOrderedIndex(st.wbByContext, w),
		runs: newTable(st.runs, w, domain.ObservationRun.Clone), runsBySubj: newOrderedIndex(st.runsBySubj, w),
		observations: newTable(st.observations, w, domain.ObservationRecord.Clone), obsByRun: newOrderedIndex(st.obsByRun, w),
		runClosing: newTable(st.runClosing, w, same[string]),
		highWater:  newTable(st.highWater, w, same[uint64]),
		subjects:   newTable(st.subjects, w, domain.SubjectState.Clone), subjByRes: newOrderedIndex(st.subjByRes, w),
		subjIDs: newTable(st.subjIDs, w, same[subjectKey]),
	}
}

func (v *resView) dirty() bool {
	return v.bindings.dirty() || v.updates.dirty() || v.states.dirty() || v.paths.dirty() || v.wbindings.dirty() ||
		v.runs.dirty() || v.observations.dirty() || v.subjects.dirty()
}

func (v *resView) commit() {
	v.bindings.commit()
	v.bindingIDs.commit()
	v.updates.commit()
	v.updByRes.commit()
	v.updByPath.commit()
	v.updRequests.commit()
	v.states.commit()
	v.paths.commit()
	v.wbindings.commit()
	v.wbLatest.commit()
	v.wbByContext.commit()
	v.runs.commit()
	v.runsBySubj.commit()
	v.observations.commit()
	v.obsByRun.commit()
	v.runClosing.commit()
	v.highWater.commit()
	v.subjects.commit()
	v.subjByRes.commit()
	v.subjIDs.commit()
}

func conflict(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), domain.ErrVersionConflict)
}

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

// --- Resources ---

func (t *semTx) InsertResourceBinding(b domain.ResourceBinding) error {
	if err := t.t.companion("resource binding", b.SemanticMeta, b.Validate); err != nil {
		return err
	}
	if t.r.sem.res.bindings.has(b.ResourceID) || t.r.sem.res.bindingIDs.has(b.ID) {
		return immutable("resource binding", b.ResourceID)
	}
	t.r.sem.res.bindings.put(b.ResourceID, b)
	t.r.sem.res.bindingIDs.put(b.ID, b.ResourceID)
	t.t.sequencedWrite(b.Seq)
	return nil
}

func (r semRead) ResourceBinding(resourceID string) (domain.ResourceBinding, error) {
	if err := r.r.check(); err != nil {
		return domain.ResourceBinding{}, err
	}
	b, ok := r.r.sem.res.bindings.get(resourceID)
	if !ok {
		return b, notFound("resource binding", resourceID)
	}
	return b, nil
}

// InsertResourceUpdate records a report by the resource's registered
// reporter; its request identity is unique per resource so a retry replays
// rather than reapplies.
func (t *semTx) InsertResourceUpdate(u domain.ResourceUpdate) error {
	if err := t.t.companion("resource update", u.SemanticMeta, u.Validate); err != nil {
		return err
	}
	b, ok := t.r.sem.res.bindings.peek(u.ResourceID)
	if !ok {
		return invalid("resource update %s: resource %s is not registered", u.ID, u.ResourceID)
	}
	if u.Reporter != b.Reporter {
		return invalid("resource update %s: reporter is not the resource's registered reporter", u.ID)
	}
	if t.r.sem.res.updates.has(u.ID) || t.r.sem.res.updRequests.has(resRequest{u.ResourceID, u.RequestID}) {
		return immutable("resource update", u.ID)
	}
	t.r.sem.res.updates.put(u.ID, u)
	t.r.sem.res.updRequests.put(resRequest{u.ResourceID, u.RequestID}, u.ID)
	t.r.sem.res.updByRes.add(u.ResourceID, seqRef{u.Seq, u.ID})
	if u.AllPaths {
		t.r.sem.res.updByPath.add(resPath{u.ResourceID, allPathsKey}, seqRef{u.Seq, u.ID})
	}
	for _, p := range u.ChangedPaths {
		t.r.sem.res.updByPath.add(resPath{u.ResourceID, p}, seqRef{u.Seq, u.ID})
	}
	t.t.sequencedWrite(u.Seq)
	return nil
}

func (r semRead) ResourceUpdate(id string) (domain.ResourceUpdate, error) {
	if err := r.r.check(); err != nil {
		return domain.ResourceUpdate{}, err
	}
	u, ok := r.r.sem.res.updates.get(id)
	if !ok {
		return u, notFound("resource update", id)
	}
	return u, nil
}

func (r semRead) ResourceUpdates(resourceID string, p store.Page) (store.ResultPage[domain.ResourceUpdate], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ResourceUpdate]{}, err
	}
	return page(p, r.r.sem.res.updByRes.after(resourceID, cursorRef(p.After)), loadAll(&r.r.sem.res.updates, ident))
}

// PutResourceState creates (expected 0) or advances a resource's current
// state. It names the resource's binding and the stored update whose result
// it is, exactly; the authoritative revision only increases, so a delayed
// report can never roll the pointer back. Its Seq is this write's sequence.
func (t *semTx) PutResourceState(s domain.ResourceState, expectedRevision uint64) (domain.ResourceState, error) {
	if err := t.t.companion("resource state", s.SemanticMeta, s.Validate); err != nil {
		return domain.ResourceState{}, err
	}
	cur, found := t.r.sem.res.states.peek(s.ResourceID)
	if cur.Revision != expectedRevision {
		return domain.ResourceState{}, conflict("resource state %s: revision %d, expected %d", s.ResourceID, cur.Revision, expectedRevision)
	}
	if found && cur.ID != s.ID {
		return domain.ResourceState{}, immutable("resource state", s.ResourceID)
	}
	b, ok := t.r.sem.res.bindings.peek(s.ResourceID)
	if !ok || b.ID != s.BindingID {
		return domain.ResourceState{}, invalid("resource state %s: binding %s is not the resource's", s.ResourceID, s.BindingID)
	}
	u, ok := t.r.sem.res.updates.peek(s.LastUpdateID)
	if !ok || u.ResourceID != s.ResourceID || u.ResultingAuthoritativeRevision != s.AuthoritativeRevision ||
		u.Freshness != s.Freshness || u.WorkspaceFingerprint != s.WorkspaceFingerprint {
		return domain.ResourceState{}, invalid("resource state %s: state is not the result of stored update %s", s.ResourceID, s.LastUpdateID)
	}
	if found && s.AuthoritativeRevision <= cur.AuthoritativeRevision {
		return domain.ResourceState{}, transition("resource state %s: revision %d does not advance %d", s.ResourceID, s.AuthoritativeRevision, cur.AuthoritativeRevision)
	}
	s.Revision = expectedRevision + 1
	t.r.sem.res.states.put(s.ResourceID, s)
	t.t.sequencedWrite(s.Seq)
	return s, nil
}

func (r semRead) ResourceState(resourceID string) (domain.ResourceState, error) {
	if err := r.r.check(); err != nil {
		return domain.ResourceState{}, err
	}
	s, ok := r.r.sem.res.states.get(resourceID)
	if !ok {
		return s, notFound("resource state", resourceID)
	}
	return s, nil
}

// PutResourcePathState creates or advances one path's current content,
// keyed by canonical locator, naming a stored update of the same resource
// at its revision; the resource revision never goes backward.
func (t *semTx) PutResourcePathState(s domain.ResourcePathState, expectedRevision uint64) (domain.ResourcePathState, error) {
	if err := t.t.companion("path state", s.SemanticMeta, s.Validate); err != nil {
		return domain.ResourcePathState{}, err
	}
	key, err := s.Locator.Key()
	if err != nil {
		return domain.ResourcePathState{}, err
	}
	cur, found := t.r.sem.res.paths.peek(key)
	if cur.Revision != expectedRevision {
		return domain.ResourcePathState{}, conflict("path state %s: revision %d, expected %d", s.Locator.Path, cur.Revision, expectedRevision)
	}
	if found && cur.ID != s.ID {
		return domain.ResourcePathState{}, immutable("path state", s.Locator.Path)
	}
	u, ok := t.r.sem.res.updates.peek(s.ResourceUpdateID)
	if !ok || u.ResourceID != s.Locator.ResourceID || u.ResultingAuthoritativeRevision != s.ResourceRevision {
		return domain.ResourcePathState{}, invalid("path state %s: not the result of stored update %s", s.Locator.Path, s.ResourceUpdateID)
	}
	if found && s.ResourceRevision <= cur.ResourceRevision {
		return domain.ResourcePathState{}, transition("path state %s: revision %d does not advance %d", s.Locator.Path, s.ResourceRevision, cur.ResourceRevision)
	}
	s.Revision = expectedRevision + 1
	t.r.sem.res.paths.put(key, s)
	t.t.sequencedWrite(s.Seq)
	return s, nil
}

func (r semRead) ResourcePathState(locator domain.ResourceLocator) (domain.ResourcePathState, error) {
	if err := r.r.check(); err != nil {
		return domain.ResourcePathState{}, err
	}
	key, err := locator.Key()
	if err != nil {
		return domain.ResourcePathState{}, invalid("path state: %v", err)
	}
	s, ok := r.r.sem.res.paths.get(key)
	if !ok {
		return s, notFound("path state", locator.Path)
	}
	return s, nil
}

// --- Workspace bindings ---

// InsertWorkspaceBinding stores an immutable binding version: versions of
// one binding are dense from 1 at increasing sequences, and its resource is
// registered.
func (t *semTx) InsertWorkspaceBinding(b domain.WorkspaceBinding) error {
	if err := t.t.companion("workspace binding", b.SemanticMeta, b.Validate); err != nil {
		return err
	}
	if t.r.sem.res.wbindings.has(wbKey{b.ID, b.Version}) {
		return immutable("workspace binding", fmt.Sprintf("%s/%d", b.ID, b.Version))
	}
	last, _ := t.r.sem.res.wbLatest.peek(b.ID)
	if b.Version != last+1 {
		return invalid("workspace binding %s: version %d, want %d", b.ID, b.Version, last+1)
	}
	// A later version takes a later sequence, so (Seq, ID) orders a
	// binding's versions unambiguously in every backend's pages.
	if prev, ok := t.r.sem.res.wbindings.peek(wbKey{b.ID, last}); ok && b.Seq <= prev.Seq {
		return invalid("workspace binding %s: version %d needs a later sequence than version %d", b.ID, b.Version, last)
	}
	if !t.r.sem.res.bindings.has(b.ResourceID) {
		return invalid("workspace binding %s: resource %s is not registered", b.ID, b.ResourceID)
	}
	t.r.sem.res.wbindings.put(wbKey{b.ID, b.Version}, b)
	t.r.sem.res.wbLatest.put(b.ID, b.Version)
	t.r.sem.res.wbByContext.add(wsContext{b.Context.Kind, b.Context.ID}, seqRef{b.Seq, b.ID})
	t.t.sequencedWrite(b.Seq)
	return nil
}

// wbRefID is a binding version's index entry ID.
// bindingAt is the version of binding id filed at seq: versions take
// strictly increasing sequences, so it is found by binary search.
func (r semRead) bindingAt(id string, seq uint64) (domain.WorkspaceBinding, bool) {
	latest, ok := r.r.sem.res.wbLatest.peek(id)
	if !ok {
		return domain.WorkspaceBinding{}, false
	}
	v := uint64(sort.Search(int(latest), func(i int) bool {
		b, _ := r.r.sem.res.wbindings.peek(wbKey{id, uint64(i) + 1})
		return b.Seq >= seq
	})) + 1
	b, ok := r.r.sem.res.wbindings.get(wbKey{id, v})
	return b, ok && b.Seq == seq
}

func (r semRead) WorkspaceBinding(ref domain.WorkspaceBindingRef) (domain.WorkspaceBinding, error) {
	if err := r.r.check(); err != nil {
		return domain.WorkspaceBinding{}, err
	}
	b, ok := r.r.sem.res.wbindings.get(wbKey{ref.ID, ref.Version})
	if !ok {
		return b, notFound("workspace binding", ref.ID)
	}
	return b, nil
}

// WorkspaceBindingsByContext pages the bindings of exactly one context: a
// source item, a task, or a conversation.
func (r semRead) WorkspaceBindingsByContext(sourceItemID, taskID, conversationID string, p store.Page) (store.ResultPage[domain.WorkspaceBinding], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.WorkspaceBinding]{}, err
	}
	ctx, err := workspaceContext(sourceItemID, taskID, conversationID)
	if err != nil {
		return store.ResultPage[domain.WorkspaceBinding]{}, err
	}
	// Entries are the documented (Seq, ID) cursor itself (DUR-1.13).
	return pageRefs(p, r.r.sem.res.wbByContext.after(ctx, cursorRef(p.After)), func(ref seqRef) (domain.WorkspaceBinding, bool) {
		return r.bindingAt(ref.id, ref.seq)
	})
}

func workspaceContext(sourceItemID, taskID, conversationID string) (wsContext, error) {
	var out []wsContext
	for _, c := range []wsContext{{domain.WorkspaceSource, sourceItemID}, {domain.WorkspaceTask, taskID}, {domain.WorkspaceConversation, conversationID}} {
		if c.id != "" {
			out = append(out, c)
		}
	}
	if len(out) != 1 {
		return wsContext{}, invalid("workspace bindings: exactly one context required")
	}
	return out[0], nil
}

// --- Runs and observations ---

// InsertObservationRun registers a run before execution; its ordinal is its
// sequence. Its task is stored and its binding version names the resource
// its subject's target lives in.
func (t *semTx) InsertObservationRun(run domain.ObservationRun) error {
	if err := t.t.companion("observation run", run.SemanticMeta, run.Validate); err != nil {
		return err
	}
	if t.r.sem.res.runs.has(run.ID) {
		return immutable("observation run", run.ID)
	}
	// (subject, Ordinal) is the run's second key (SEC-1.13); IDs are never
	// empty, so the first entry after (Ordinal, "") shares the ordinal if
	// any does.
	for r := range t.r.sem.res.runsBySubj.after(run.SubjectKey, seqRef{seq: run.Ordinal}) {
		if r.seq == run.Ordinal {
			return invalid("observation run %s: subject ordinal %d is already run %s", run.ID, run.Ordinal, r.id)
		}
		break
	}
	if !t.r.tasks.has(run.TaskID) {
		return invalid("observation run %s: task %s is not stored", run.ID, run.TaskID)
	}
	b, ok := t.r.sem.res.wbindings.peek(wbKey{run.Binding.ID, run.Binding.Version})
	if !ok || b.ResourceID != subjectResource(run.Subject) {
		return invalid("observation run %s: binding is not a stored binding of its subject's resource", run.ID)
	}
	t.r.sem.res.runs.put(run.ID, run)
	t.r.sem.res.runsBySubj.add(run.SubjectKey, seqRef{run.Seq, run.ID})
	t.t.sequencedWrite(run.Seq)
	return nil
}

// InsertObservation stores a typed observation of a registered run: same
// execution, subject, family, binding and reporter, with TOOL evidence
// stored in the observation's boundary.
func (t *semTx) InsertObservation(o domain.ObservationRecord) error {
	if err := t.t.companion("observation", o.SemanticMeta, o.Validate); err != nil {
		return err
	}
	if t.r.sem.res.observations.has(o.ID) {
		return immutable("observation", o.ID)
	}
	run, ok := t.r.sem.res.runs.peek(o.RunID)
	if !ok || run.ExecutionID != o.ExecutionID || run.SubjectKey != o.SubjectKey || run.Subject.Family != o.Family ||
		run.Binding != o.Binding || run.Reporter != o.Reporter {
		return invalid("observation %s: not an observation of its run's exact execution", o.ID)
	}
	ev, ok := t.r.items.peek(o.EvidenceItemID)
	if !ok || ev.Authority != domain.AuthorityTool || ev.Access != o.Access {
		return invalid("observation %s: evidence is not a stored TOOL item in its boundary", o.ID)
	}
	// A run closes once (DUR-1.1, G1).
	if prior, ok := t.r.sem.res.runClosing.peek(o.RunID); ok {
		return fmt.Errorf("observation %s: run %s already closed with %s: %w", o.ID, o.RunID, prior, domain.ErrInvalidTransition)
	}
	if store.ClosesRun(o) {
		t.r.sem.res.runClosing.put(o.RunID, o.ID)
	}
	if key := (subjectKey{run.SubjectKey, run.TaskID, run.Access}); o.TerminalComplete() {
		if hw, _ := t.r.sem.res.highWater.peek(key); run.Ordinal > hw {
			t.r.sem.res.highWater.put(key, run.Ordinal)
		}
	}
	t.r.sem.res.observations.put(o.ID, o)
	t.r.sem.res.obsByRun.add(o.RunID, seqRef{o.Seq, o.ID})
	t.t.sequencedWrite(o.Seq)
	return nil
}

func (r semRead) Observation(id string) (domain.ObservationRecord, error) {
	if err := r.r.check(); err != nil {
		return domain.ObservationRecord{}, err
	}
	o, ok := r.r.sem.res.observations.get(id)
	if !ok {
		return o, notFound("observation", id)
	}
	return o, nil
}

func (r semRead) ObservationRun(id string) (domain.ObservationRun, error) {
	if err := r.r.check(); err != nil {
		return domain.ObservationRun{}, err
	}
	run, ok := r.r.sem.res.runs.get(id)
	if !ok {
		return run, notFound("observation run", id)
	}
	return run, nil
}

func (r semRead) RunsBySubject(subjectKey string, p store.Page) (store.ResultPage[domain.ObservationRun], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ObservationRun]{}, err
	}
	return page(p, r.r.sem.res.runsBySubj.after(subjectKey, cursorRef(p.After)), loadAll(&r.r.sem.res.runs, ident))
}

func (r semRead) ObservationsByRun(runID string, p store.Page) (store.ResultPage[domain.ObservationRecord], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ObservationRecord]{}, err
	}
	return page(p, r.r.sem.res.obsByRun.after(runID, cursorRef(p.After)), loadAll(&r.r.sem.res.observations, ident))
}

// PutSubjectState creates or advances a subject's current state in its
// (task, boundary) partition. It names a stored observation of the subject
// whose run ordinal is the accepted watermark, which never decreases; its
// current item is stored TOOL content; causeID names the stored observation
// or resource update that changed it. Its Seq is this write's sequence.
func (t *semTx) PutSubjectState(s domain.SubjectState, expectedRevision uint64, causeID string) (domain.SubjectState, error) {
	if err := t.t.companion("subject state", s.SemanticMeta, s.Validate); err != nil {
		return domain.SubjectState{}, err
	}
	key := subjectKey{s.SubjectKey, s.TaskID, s.Access}
	cur, found := t.r.sem.res.subjects.peek(key)
	if cur.Revision != expectedRevision {
		return domain.SubjectState{}, conflict("subject state %s: revision %d, expected %d", s.SubjectKey, cur.Revision, expectedRevision)
	}
	if found && cur.ID != s.ID || !found && t.r.sem.res.subjIDs.has(s.ID) {
		return domain.SubjectState{}, immutable("subject state", s.ID)
	}
	if !t.r.sem.res.observations.has(causeID) && !t.r.sem.res.updates.has(causeID) {
		return domain.SubjectState{}, invalid("subject state %s: cause %s is not a stored observation or resource update", s.SubjectKey, causeID)
	}
	o, ok := t.r.sem.res.observations.peek(s.ObservationID)
	if !ok || o.SubjectKey != s.SubjectKey {
		return domain.SubjectState{}, invalid("subject state %s: observation %s is not of its subject", s.SubjectKey, s.ObservationID)
	}
	run, _ := t.r.sem.res.runs.peek(o.RunID)
	if run.Ordinal != s.AcceptedOrdinal {
		return domain.SubjectState{}, invalid("subject state %s: watermark is not its observation's run ordinal", s.SubjectKey)
	}
	if it, ok := t.r.items.peek(s.CurrentItemID); !ok || it.Authority != domain.AuthorityTool {
		return domain.SubjectState{}, invalid("subject state %s: current item is not stored TOOL content", s.SubjectKey)
	}
	if found && s.AcceptedOrdinal < cur.AcceptedOrdinal {
		return domain.SubjectState{}, transition("subject state %s: watermark %d is below %d", s.SubjectKey, s.AcceptedOrdinal, cur.AcceptedOrdinal)
	}
	s.Revision = expectedRevision + 1
	t.r.sem.res.subjects.put(key, s)
	if !found {
		t.r.sem.res.subjIDs.put(s.ID, key)
		t.r.sem.res.subjByRes.add(subjectResource(run.Subject), seqRef{s.Seq, s.ID})
	}
	t.t.sequencedWrite(s.Seq)
	return s, nil
}

func (r semRead) SubjectState(subject, taskID string, access domain.AccessBoundary) (domain.SubjectState, error) {
	if err := r.r.check(); err != nil {
		return domain.SubjectState{}, err
	}
	s, ok := r.r.sem.res.subjects.get(subjectKey{subject, taskID, access})
	if !ok {
		return s, notFound("subject state", subject)
	}
	return s, nil
}

func (r semRead) SubjectStatesByResource(resourceID string, p store.Page) (store.ResultPage[domain.SubjectState], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.SubjectState]{}, err
	}
	return page(p, r.r.sem.res.subjByRes.after(resourceID, cursorRef(p.After)), func(id string) (domain.SubjectState, bool) {
		key, ok := r.r.sem.res.subjIDs.peek(id)
		if !ok {
			return domain.SubjectState{}, false
		}
		st, ok := r.r.sem.res.subjects.get(key)
		// Only CURRENT states are live dependents (G2); dead ones are
		// skipped without counting toward the page.
		return st, ok && st.Applicability == domain.ApplicabilityCurrent
	})
}

// ResourceUpdatesAffectingPath implements store.ResourceReader over the
// path-change index: ALL-paths updates, and updates naming path or an
// ancestor directory, merged in (Seq, ID) order.
func (r semRead) ResourceUpdatesAffectingPath(resourceID, path string, p store.Page) (store.ResultPage[domain.ResourceUpdate], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ResourceUpdate]{}, err
	}
	affect, err := store.PathAffectKeys(path)
	if err != nil {
		return store.ResultPage[domain.ResourceUpdate]{}, err
	}
	keys := []resPath{{resourceID, allPathsKey}}
	for _, k := range affect {
		keys = append(keys, resPath{resourceID, k})
	}
	return page(p, dedup(mergeAfter(&r.r.sem.res.updByPath, keys, cursorRef(p.After))), loadAll(&r.r.sem.res.updates, ident))
}

// LatestResourceUpdateAffectingPath implements store.ResourceReader: the
// newest entry of each affecting key in the path-change index, O(depth).
func (r semRead) LatestResourceUpdateAffectingPath(resourceID, path string) (domain.ResourceUpdate, error) {
	if err := r.r.check(); err != nil {
		return domain.ResourceUpdate{}, err
	}
	affect, err := store.PathAffectKeys(path)
	if err != nil {
		return domain.ResourceUpdate{}, err
	}
	var newest seqRef
	for _, k := range append([]string{allPathsKey}, affect...) {
		for ref := range r.r.sem.res.updByPath.before(resPath{resourceID, k}, seqRef{}) {
			if newest.less(ref) {
				newest = ref
			}
			break
		}
	}
	if newest == (seqRef{}) {
		return domain.ResourceUpdate{}, notFound("resource update affecting", path)
	}
	u, ok := r.r.sem.res.updates.get(newest.id)
	if !ok {
		return domain.ResourceUpdate{}, domain.ErrIntegrity
	}
	return u, nil
}

// ClosingObservation implements store.ResourceReader through the run's
// closing pointer, written with the closing observation.
func (r semRead) ClosingObservation(runID string) (domain.ObservationRecord, error) {
	if err := r.r.check(); err != nil {
		return domain.ObservationRecord{}, err
	}
	id, ok := r.r.sem.res.runClosing.get(runID)
	if !ok {
		return domain.ObservationRecord{}, notFound("closing observation of run", runID)
	}
	o, ok := r.r.sem.res.observations.get(id)
	if !ok {
		return domain.ObservationRecord{}, domain.ErrIntegrity
	}
	return o, nil
}

// SubjectHighWater implements store.ResourceReader: one keyed read of the
// mark InsertObservation raises.
func (r semRead) SubjectHighWater(subject, taskID string, access domain.AccessBoundary) (uint64, error) {
	if err := r.r.check(); err != nil {
		return 0, err
	}
	hw, ok := r.r.sem.res.highWater.get(subjectKey{subject, taskID, access})
	if !ok {
		return 0, notFound("subject high-water mark", subject)
	}
	return hw, nil
}
