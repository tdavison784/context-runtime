package storetest

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Builders for resource, workspace, and observation records (P3-19..22).

var (
	fpA = domain.HashBytes([]byte("workspace A"))
	fpB = domain.HashBytes([]byte("workspace B"))
)

// NewResourceBinding registers resource with a HARNESS owner and reporter.
func NewResourceBinding(sess, resource string, seq uint64) domain.ResourceBinding {
	return domain.ResourceBinding{SemanticMeta: Meta(sess, "rb-"+resource, seq), ResourceID: resource, Owner: HarnessPrincipal(sess),
		Reporter: HarnessPrincipal(sess), Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sess}}
}

// NewResourceUpdate reports resource moving from revision from to from+1
// with a KNOWN workspace fingerprint.
func NewResourceUpdate(sess, id, resource string, seq, from uint64, fp string, paths ...string) domain.ResourceUpdate {
	return domain.ResourceUpdate{SemanticMeta: Meta(sess, id, seq), ResourceID: resource, RequestID: "req-" + id, Reporter: HarnessPrincipal(sess),
		ExpectedAuthoritativeRevision: from, ResultingAuthoritativeRevision: from + 1, WorkspaceFingerprint: fp, Freshness: domain.ResourceKnown,
		ChangedPaths: paths, AllPaths: len(paths) == 0}
}

// StateAfter is the resource state an accepted update produces.
func StateAfter(u domain.ResourceUpdate, seq uint64) domain.ResourceState {
	return domain.ResourceState{SemanticMeta: Meta(u.SessionID, "rs-"+u.ResourceID, seq), ResourceID: u.ResourceID, BindingID: "rb-" + u.ResourceID,
		LastUpdateID: u.ID, AuthoritativeRevision: u.ResultingAuthoritativeRevision, Revision: 1, WorkspaceFingerprint: u.WorkspaceFingerprint, Freshness: u.Freshness}
}

// NewWorkspaceBinding binds task "task" to resource at the repository root.
func NewWorkspaceBinding(sess, id, resource string, version, seq uint64) domain.WorkspaceBinding {
	return domain.WorkspaceBinding{Context: domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"}, SemanticMeta: Meta(sess, id, seq),
		Version: version, ResourceID: resource, TaskID: "task", BaseDir: ".", EnvironmentSpec: "env-1", SuiteSpec: "suite-1", CoverageSpec: "all",
		Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "task"}, Reporter: TaskHarness(sess)}
}

// TaskHarness is a HARNESS principal of task "task", which a task-bound
// workspace binding's boundary permits.
func TaskHarness(sess string) domain.Principal {
	return domain.Principal{SessionID: sess, TaskID: "task", Authority: domain.AuthorityHarness}
}

// putTask creates ACTIVE task "task".
func putTask(t *testing.T, tx store.Tx) {
	t.Helper()
	_, err := tx.PutTask(NewTask(tx.SessionID(), "task"), 0, NewLifecycleEvent(tx.SessionID(), "task-create", tx.NextSeq(), domain.TargetTask, "task"))
	noErr(t, err)
}

// TestsSubject is the tests_pass subject of resource's full suite.
func TestsSubject(resource string) domain.ObservationSubject {
	return domain.ObservationSubject{Family: domain.ObservationTests, Target: domain.TargetSpec{Tests: &domain.TestsTarget{
		ResourceID: resource, BaseDir: ".", WorkingDir: ".", EnvironmentSpec: "env-1", SuiteSpec: "suite-1", CoverageSpec: "all"}}}
}

// NewObservationRun registers a pre-execution run of resource's suite under
// workspace binding wb version 1; its ordinal is its sequence.
func NewObservationRun(t *testing.T, sess, id, resource, wb string, seq uint64) domain.ObservationRun {
	t.Helper()
	subject := TestsSubject(resource)
	key, err := subject.Key()
	if err != nil {
		t.Fatal(err)
	}
	return domain.ObservationRun{SemanticMeta: Meta(sess, id, seq), Subject: subject, SubjectKey: key, Ordinal: seq, ExecutionID: "exec-" + id,
		Binding: domain.WorkspaceBindingRef{ID: wb, Version: 1}, TaskID: "task", Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "task"},
		Reporter: HarnessPrincipal(sess)}
}

// ToolEvidence returns a TOOL item within the run's boundary.
func ToolEvidence(sess, id string, seq uint64) domain.ContextItem {
	it := NewItem(sess, id, seq, "tool output "+id)
	it.Authority, it.Scope = domain.AuthorityTool, domain.ScopeTask
	it.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: "task"}
	return it
}

// NewObservation is a complete PASS of run r observed on fingerprint fp,
// evidenced by TOOL item evidence.
func NewObservation(r domain.ObservationRun, id, evidence string, seq uint64, fp string) domain.ObservationRecord {
	return domain.ObservationRecord{SemanticMeta: Meta(r.SessionID, id, seq), Family: r.Subject.Family, RunID: r.ID, ExecutionID: r.ExecutionID,
		SubjectKey: r.SubjectKey, EvidenceItemID: evidence, Binding: r.Binding, Reporter: r.Reporter, Access: r.Access,
		ObservedWorkspaceFingerprint: fp, Outcome: domain.OutcomePass, Completeness: domain.ObservationComplete, Passed: 3, Total: 3,
		ReportingMatcher: domain.MatcherRef{Name: "tests_pass", Version: "1"}}
}

// testSemanticResources checks resource registration, ordered reporting and
// the resource-state CAS (P3-19): one binding per resource, updates only by
// its reporter with unique request identity, and a state that names its
// update exactly, never rolls back, and is compare-and-swapped.
func testSemanticResources(t *testing.T, s store.Store) {
	var u1 domain.ResourceUpdate
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		u1 = NewResourceUpdate(sessA, "u1", "repo", tx.NextSeq(), 0, fpA)
		noErr(t, sem.InsertResourceUpdate(u1))
		got, err := sem.PutResourceState(StateAfter(u1, tx.NextSeq()), 0)
		noErr(t, err)
		if got.Revision != 1 {
			t.Errorf("created resource state Revision = %d, want 1", got.Revision)
		}
		return nil
	})
	rejected(t, s, sessA, domain.ErrImmutable, func(tx store.Tx) error {
		b := NewResourceBinding(sessA, "repo", tx.NextSeq())
		b.ID = "rb-again"
		return semantic(t, tx).InsertResourceBinding(b)
	})
	for _, tc := range []struct {
		name string
		u    func(seq uint64) domain.ResourceUpdate
		want error
	}{
		{"unregistered resource", func(seq uint64) domain.ResourceUpdate { return NewResourceUpdate(sessA, "u2", "other", seq, 1, fpB) }, domain.ErrInvalidRecord},
		{"another reporter", func(seq uint64) domain.ResourceUpdate {
			u := NewResourceUpdate(sessA, "u2", "repo", seq, 1, fpB)
			u.Reporter = NewPrincipal(sessA, domain.AuthoritySystem)
			return u
		}, domain.ErrInvalidRecord},
		{"request reused", func(seq uint64) domain.ResourceUpdate {
			u := NewResourceUpdate(sessA, "u2", "repo", seq, 1, fpB)
			u.RequestID = u1.RequestID
			return u
		}, domain.ErrImmutable},
		{"ID reused", func(seq uint64) domain.ResourceUpdate { return NewResourceUpdate(sessA, "u1", "repo", seq, 1, fpB) }, domain.ErrImmutable},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return semantic(t, tx).InsertResourceUpdate(tc.u(tx.NextSeq())) })
		if !errors.Is(err, tc.want) {
			t.Errorf("update %s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	var u2 domain.ResourceUpdate
	update(t, s, sessA, func(tx store.Tx) error {
		u2 = NewResourceUpdate(sessA, "u2", "repo", tx.NextSeq(), 1, fpB, "src/a.go")
		return semantic(t, tx).InsertResourceUpdate(u2)
	})
	for _, tc := range []struct {
		name     string
		st       func(seq uint64) domain.ResourceState
		expected uint64
		want     error
	}{
		{"stale revision", func(seq uint64) domain.ResourceState { return StateAfter(u2, seq) }, 0, domain.ErrVersionConflict},
		{"state disagrees with its update", func(seq uint64) domain.ResourceState {
			st := StateAfter(u2, seq)
			st.WorkspaceFingerprint = fpA
			return st
		}, 1, domain.ErrInvalidRecord},
		{"missing update", func(seq uint64) domain.ResourceState {
			st := StateAfter(u2, seq)
			st.LastUpdateID = "nope"
			return st
		}, 1, domain.ErrInvalidRecord},
		{"rollback to an older update", func(seq uint64) domain.ResourceState { return StateAfter(u1, seq) }, 1, domain.ErrInvalidTransition},
		{"another binding", func(seq uint64) domain.ResourceState {
			st := StateAfter(u2, seq)
			st.BindingID = "rb-other"
			return st
		}, 1, domain.ErrInvalidRecord},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			_, err := semantic(t, tx).PutResourceState(tc.st(tx.NextSeq()), tc.expected)
			return err
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("state %s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	update(t, s, sessA, func(tx store.Tx) error {
		got, err := semantic(t, tx).PutResourceState(StateAfter(u2, tx.NextSeq()), 1)
		noErr(t, err)
		if got.Revision != 2 {
			t.Errorf("advanced resource state Revision = %d, want 2", got.Revision)
		}
		return nil
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		st, err := r.ResourceState("repo")
		noErr(t, err)
		if st.LastUpdateID != "u2" || st.AuthoritativeRevision != 2 || st.Revision != 2 {
			t.Errorf("ResourceState = %+v", st)
		}
		b, err := r.ResourceBinding("repo")
		noErr(t, err)
		assertEqual(t, "ResourceBinding", b, NewResourceBinding(sessA, "repo", 1))
		got, err := r.ResourceUpdate("u2")
		noErr(t, err)
		assertEqual(t, "ResourceUpdate", got, u2)
		p, err := r.ResourceUpdates("repo", store.Page{Limit: 1})
		noErr(t, err)
		if len(p.Records) != 1 || p.Records[0].ID != "u1" || !p.More {
			t.Errorf("first update page = %+v", p)
		}
		_, err = r.ResourceState("other")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testSemanticResourcePaths checks per-path current content (P3-19): keyed
// by canonical locator, naming an update of the same resource at its
// revision, compare-and-swapped, and never rolled back.
func testSemanticResourcePaths(t *testing.T, s store.Store) {
	loc := domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: "src/a.go"}
	var u1, u2 domain.ResourceUpdate
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		u1 = NewResourceUpdate(sessA, "u1", "repo", tx.NextSeq(), 0, fpA, "src/a.go")
		u2 = NewResourceUpdate(sessA, "u2", "repo", tx.NextSeq(), 1, fpB, "src/a.go")
		noErr(t, sem.InsertResourceUpdate(u1))
		return sem.InsertResourceUpdate(u2)
	})
	path := func(seq uint64, u domain.ResourceUpdate, content string) domain.ResourcePathState {
		return domain.ResourcePathState{SemanticMeta: Meta(sessA, "ps-a", seq), Locator: loc, ContentHash: domain.HashBytes([]byte(content)),
			ResourceUpdateID: u.ID, ResourceRevision: u.ResultingAuthoritativeRevision, Revision: 1, Freshness: domain.ResourceKnown}
	}
	update(t, s, sessA, func(tx store.Tx) error {
		_, err := semantic(t, tx).PutResourcePathState(path(tx.NextSeq(), u2, "v2"), 0)
		return err
	})
	for _, tc := range []struct {
		name     string
		ps       func(seq uint64) domain.ResourcePathState
		expected uint64
		want     error
	}{
		{"stale revision", func(seq uint64) domain.ResourcePathState { return path(seq, u2, "v2") }, 0, domain.ErrVersionConflict},
		{"rollback", func(seq uint64) domain.ResourcePathState { return path(seq, u1, "v1") }, 1, domain.ErrInvalidTransition},
		{"revision disagrees with update", func(seq uint64) domain.ResourcePathState {
			ps := path(seq, u2, "v3")
			ps.ResourceRevision = 5
			return ps
		}, 1, domain.ErrInvalidRecord},
		{"missing update", func(seq uint64) domain.ResourcePathState {
			ps := path(seq, u2, "v3")
			ps.ResourceUpdateID = "nope"
			return ps
		}, 1, domain.ErrInvalidRecord},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			_, err := semantic(t, tx).PutResourcePathState(tc.ps(tx.NextSeq()), tc.expected)
			return err
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("path %s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := readSemantic(t, tx).ResourcePathState(domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: "src/./a.go"})
		noErr(t, err)
		if got.ContentHash != domain.HashBytes([]byte("v2")) || got.Revision != 1 {
			t.Errorf("ResourcePathState via a noncanonical spelling = %+v", got)
		}
		return nil
	})
}

// testSemanticWorkspaceBindings checks immutable workspace binding versions
// (P3-20): a registered resource, dense versions per binding, and indexed
// reads by context.
func testSemanticWorkspaceBindings(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		return sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 1, tx.NextSeq()))
	})
	for _, tc := range []struct {
		name string
		b    func(seq uint64) domain.WorkspaceBinding
		want error
	}{
		{"version reused", func(seq uint64) domain.WorkspaceBinding { return NewWorkspaceBinding(sessA, "wb", "repo", 1, seq) }, domain.ErrImmutable},
		{"version skipped", func(seq uint64) domain.WorkspaceBinding { return NewWorkspaceBinding(sessA, "wb", "repo", 3, seq) }, domain.ErrInvalidRecord},
		{"unregistered resource", func(seq uint64) domain.WorkspaceBinding { return NewWorkspaceBinding(sessA, "wb2", "other", 1, seq) }, domain.ErrInvalidRecord},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return semantic(t, tx).InsertWorkspaceBinding(tc.b(tx.NextSeq())) })
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	// A later version needs a later sequence.
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		return semantic(t, tx).InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 2, 2))
	})
	update(t, s, sessA, func(tx store.Tx) error {
		return semantic(t, tx).InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 2, tx.NextSeq()))
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		got, err := r.WorkspaceBinding(domain.WorkspaceBindingRef{ID: "wb", Version: 1})
		noErr(t, err)
		assertEqual(t, "WorkspaceBinding v1", got, NewWorkspaceBinding(sessA, "wb", "repo", 1, 2))
		_, err = r.WorkspaceBinding(domain.WorkspaceBindingRef{ID: "wb", Version: 9})
		wantErr(t, err, domain.ErrNotFound)
		p, err := r.WorkspaceBindingsByContext("", "task", "", store.Page{Limit: 5})
		noErr(t, err)
		if len(p.Records) != 2 || p.Records[0].Version != 1 || p.Records[1].Version != 2 {
			t.Errorf("WorkspaceBindingsByContext(task) = %+v", p.Records)
		}
		_, err = r.WorkspaceBindingsByContext("src", "task", "", store.Page{Limit: 5})
		wantErr(t, err, domain.ErrInvalidRecord)
		return nil
	})
}

// testSemanticObservations checks pre-execution runs, typed observations and
// the subject-state watermark (P3-21/22): runs name their stored task and a
// registered binding of their subject's resource, observations are bound to their run's exact
// execution and to TOOL evidence in their boundary, and subject state names
// an observation of its subject, never lowers its watermark, and names a
// stored cause.
func testSemanticObservations(t *testing.T, s store.Store) {
	var run1, run2 domain.ObservationRun
	var o1 domain.ObservationRecord
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		putTask(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		noErr(t, sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 1, tx.NextSeq())))
		run1 = NewObservationRun(t, sessA, "run1", "repo", "wb", tx.NextSeq())
		run2 = NewObservationRun(t, sessA, "run2", "repo", "wb", tx.NextSeq())
		noErr(t, sem.InsertObservationRun(run1))
		noErr(t, sem.InsertObservationRun(run2))
		noErr(t, tx.InsertItem(ToolEvidence(sessA, "ev1", tx.NextSeq())))
		o1 = NewObservation(run1, "obs1", "ev1", tx.NextSeq(), fpA)
		noErr(t, sem.InsertObservation(o1))
		noErr(t, tx.InsertItem(ToolEvidence(sessA, "ev2", tx.NextSeq())))
		return sem.InsertObservation(NewObservation(run2, "obs2", "ev2", tx.NextSeq(), fpB))
	})
	for _, tc := range []struct {
		name string
		r    func(seq uint64) domain.ObservationRun
	}{
		{"missing binding", func(seq uint64) domain.ObservationRun {
			r := NewObservationRun(t, sessA, "run3", "repo", "wb", seq)
			r.Binding.Version = 2
			return r
		}},
		{"binding of another resource", func(seq uint64) domain.ObservationRun { return NewObservationRun(t, sessA, "run3", "other", "wb", seq) }},
		{"task missing", func(seq uint64) domain.ObservationRun {
			r := NewObservationRun(t, sessA, "run3", "repo", "wb", seq)
			r.TaskID = "ghost-task"
			return r
		}},
	} {
		rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error { return semantic(t, tx).InsertObservationRun(tc.r(tx.NextSeq())) })
	}
	for _, tc := range []struct {
		name string
		edit func(o *domain.ObservationRecord)
	}{
		{"missing run", func(o *domain.ObservationRecord) { o.RunID = "nope" }},
		{"another execution", func(o *domain.ObservationRecord) { o.ExecutionID = "exec-other" }},
		{"another subject", func(o *domain.ObservationRecord) { o.SubjectKey = "sub_" + o.SubjectKey[4:67] + "0" }},
		{"missing evidence", func(o *domain.ObservationRecord) { o.EvidenceItemID = "ghost" }},
		{"non-TOOL evidence", func(o *domain.ObservationRecord) { o.EvidenceItemID = "user-item" }},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			if tc.name == "non-TOOL evidence" {
				noErr(t, tx.InsertItem(NewItem(sessA, "user-item", tx.NextSeq(), "forged PASS")))
			}
			o := NewObservation(run2, "obs3", "ev1", tx.NextSeq(), fpA)
			tc.edit(&o)
			return semantic(t, tx).InsertObservation(o)
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("observation %s: error = %v, want ErrInvalidRecord", tc.name, err)
		}
	}
	state := func(seq, ordinal uint64, obs string) domain.SubjectState {
		return domain.SubjectState{SemanticMeta: Meta(sessA, "ss", seq), SubjectKey: run1.SubjectKey, TaskID: "task", CurrentItemID: "ev1",
			ObservationID: obs, Access: run1.Access, AcceptedOrdinal: ordinal, Revision: 1, Applicability: domain.ApplicabilityCurrent}
	}
	update(t, s, sessA, func(tx store.Tx) error {
		_, err := semantic(t, tx).PutSubjectState(state(tx.NextSeq(), run2.Ordinal, "obs2"), 0, "obs2")
		return err
	})
	for _, tc := range []struct {
		name     string
		st       func(seq uint64) domain.SubjectState
		expected uint64
		cause    string
		want     error
	}{
		{"stale revision", func(seq uint64) domain.SubjectState { return state(seq, run2.Ordinal, "obs2") }, 0, "obs2", domain.ErrVersionConflict},
		{"missing cause", func(seq uint64) domain.SubjectState { return state(seq, run2.Ordinal, "obs2") }, 1, "nope", domain.ErrInvalidRecord},
		{"missing observation", func(seq uint64) domain.SubjectState { return state(seq, run2.Ordinal, "nope") }, 1, "obs2", domain.ErrInvalidRecord},
		{"watermark lowered by an older run", func(seq uint64) domain.SubjectState { return state(seq, run1.Ordinal, "obs1") }, 1, "obs1", domain.ErrInvalidTransition},
		{"ordinal is not the observation's run", func(seq uint64) domain.SubjectState { return state(seq, run2.Ordinal, "obs1") }, 1, "obs1", domain.ErrInvalidRecord},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			_, err := semantic(t, tx).PutSubjectState(tc.st(tx.NextSeq()), tc.expected, tc.cause)
			return err
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("subject state %s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		got, err := r.Observation("obs1")
		noErr(t, err)
		assertEqual(t, "Observation", got, o1)
		runs, err := r.RunsBySubject(run1.SubjectKey, store.Page{Limit: 5})
		noErr(t, err)
		if len(runs.Records) != 2 || runs.Records[0].ID != "run1" || runs.Records[1].ID != "run2" {
			t.Errorf("RunsBySubject = %+v, want run1 then run2 in registration order", runs.Records)
		}
		obs, err := r.ObservationsByRun("run1", store.Page{Limit: 5})
		noErr(t, err)
		if len(obs.Records) != 1 {
			t.Errorf("ObservationsByRun = %+v", obs.Records)
		}
		st, err := r.SubjectState(run1.SubjectKey, "task", run1.Access)
		noErr(t, err)
		if st.Revision != 1 || st.ObservationID != "obs2" {
			t.Errorf("SubjectState = %+v", st)
		}
		byRes, err := r.SubjectStatesByResource("repo", store.Page{Limit: 5})
		noErr(t, err)
		if len(byRes.Records) != 1 {
			t.Errorf("SubjectStatesByResource = %+v", byRes.Records)
		}
		_, err = r.SubjectState(run1.SubjectKey, "other", run1.Access)
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testSemanticRunOrdinalUnique checks the run key (subject, Ordinal)
// (P3-22, SEC-1.13): two runs of one subject never share an ordinal, so
// run order is never ambiguous; runs of different subjects may.
func testSemanticRunOrdinalUnique(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		putTask(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "other", tx.NextSeq())))
		noErr(t, sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 1, tx.NextSeq())))
		return sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb-other", "other", 1, tx.NextSeq()))
	})
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		sem := semantic(t, tx)
		seq := tx.NextSeq()
		noErr(t, sem.InsertObservationRun(NewObservationRun(t, sessA, "run1", "repo", "wb", seq)))
		return sem.InsertObservationRun(NewObservationRun(t, sessA, "run2", "repo", "wb", seq))
	})
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		seq := tx.NextSeq()
		noErr(t, sem.InsertObservationRun(NewObservationRun(t, sessA, "run1", "repo", "wb", seq)))
		return sem.InsertObservationRun(NewObservationRun(t, sessA, "run2", "other", "wb-other", seq))
	})
}

// testSemanticRunClosesOnce checks a run's single closing outcome (P3-16/22,
// DUR-1.1, G1): partial progress may precede it, but once a run reports a
// complete PASS/FAIL or an ERROR/TIMEOUT/CANCELLED it accepts no further
// observation, so no second, contradictory result exists for one run.
func testSemanticRunClosesOnce(t *testing.T, s store.Store) {
	var run domain.ObservationRun
	obs := func(tx store.Tx, id string, outcome domain.ObservationOutcome, c domain.ObservationCompleteness) domain.ObservationRecord {
		noErr(t, tx.InsertItem(ToolEvidence(sessA, "ev-"+id, tx.NextSeq())))
		o := NewObservation(run, id, "ev-"+id, tx.NextSeq(), fpA)
		o.Outcome, o.Completeness = outcome, c
		switch {
		case outcome == domain.OutcomeFail:
			o.Passed, o.Failed = 2, 1
		case c == domain.ObservationPartial:
			o.Passed, o.Skipped = 1, 2
		}
		return o
	}
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		putTask(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		noErr(t, sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 1, tx.NextSeq())))
		run = NewObservationRun(t, sessA, "run1", "repo", "wb", tx.NextSeq())
		noErr(t, sem.InsertObservationRun(run))
		noErr(t, sem.InsertObservation(obs(tx, "partial", domain.OutcomeFail, domain.ObservationPartial)))
		return sem.InsertObservation(obs(tx, "pass", domain.OutcomePass, domain.ObservationComplete))
	})
	for _, tc := range []struct {
		name    string
		outcome domain.ObservationOutcome
		c       domain.ObservationCompleteness
	}{
		{"contradictory complete FAIL", domain.OutcomeFail, domain.ObservationComplete},
		{"repeated complete PASS", domain.OutcomePass, domain.ObservationComplete},
		{"ERROR", domain.OutcomeError, domain.ObservationComplete},
		{"late partial", domain.OutcomePass, domain.ObservationPartial},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			return semantic(t, tx).InsertObservation(obs(tx, "late-"+string(tc.outcome)+"-"+string(tc.c), tc.outcome, tc.c))
		})
		if !errors.Is(err, domain.ErrInvalidTransition) {
			t.Errorf("%s after the run closed: error = %v, want ErrInvalidTransition", tc.name, err)
		}
	}
}

// testSemanticLiveSubjectStates checks the live-only by-resource read (G2,
// SEC-1.8, DUR-1.2): SubjectStatesByResource returns only CURRENT states,
// so STALE/UNKNOWN history never counts toward a page or a work bound; a
// state that becomes CURRENT again returns at its first-filing position.
func testSemanticLiveSubjectStates(t *testing.T, s store.Store) {
	subject := func(suite string) domain.ObservationSubject {
		sub := TestsSubject("repo")
		sub.Target.Tests.SuiteSpec = suite
		return sub
	}
	run := func(t *testing.T, id string, sub domain.ObservationSubject, seq uint64) domain.ObservationRun {
		r := NewObservationRun(t, sessA, id, "repo", "wb", seq)
		key, err := sub.Key()
		noErr(t, err)
		r.Subject, r.SubjectKey = sub, key
		return r
	}
	state := func(r domain.ObservationRun, id, obs string, seq uint64, a domain.ApplicabilityState) domain.SubjectState {
		return domain.SubjectState{SemanticMeta: Meta(sessA, id, seq), SubjectKey: r.SubjectKey, TaskID: "task", CurrentItemID: "ev-" + obs,
			ObservationID: obs, Access: r.Access, AcceptedOrdinal: r.Ordinal, Revision: 1, Applicability: a}
	}
	observe := func(tx store.Tx, r domain.ObservationRun, obs string) {
		noErr(t, tx.InsertItem(ToolEvidence(sessA, "ev-"+obs, tx.NextSeq())))
		noErr(t, semantic(t, tx).InsertObservation(NewObservation(r, obs, "ev-"+obs, tx.NextSeq(), fpA)))
	}
	var ra domain.ObservationRun
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		putTask(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		noErr(t, sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 1, tx.NextSeq())))
		ra = run(t, "run-a", subject("suite-a"), tx.NextSeq())
		rb := run(t, "run-b", subject("suite-b"), tx.NextSeq())
		noErr(t, sem.InsertObservationRun(ra))
		noErr(t, sem.InsertObservationRun(rb))
		observe(tx, ra, "obs-a")
		observe(tx, rb, "obs-b")
		_, err := sem.PutSubjectState(state(ra, "ss-a", "obs-a", tx.NextSeq(), domain.ApplicabilityCurrent), 0, "obs-a")
		noErr(t, err)
		_, err = sem.PutSubjectState(state(rb, "ss-b", "obs-b", tx.NextSeq(), domain.ApplicabilityCurrent), 0, "obs-b")
		noErr(t, err)
		// ss-a goes STALE: dead history.
		_, err = sem.PutSubjectState(state(ra, "ss-a", "obs-a", tx.NextSeq(), domain.ApplicabilityStale), 1, "obs-a")
		return err
	})
	byResource := func(limit int) ([]string, bool) {
		var ids []string
		var more bool
		view(t, s, sessA, func(tx store.ReadTx) error {
			pg, err := readSemantic(t, tx).SubjectStatesByResource("repo", store.Page{Limit: limit})
			noErr(t, err)
			for _, st := range pg.Records {
				ids = append(ids, st.ID)
			}
			more = pg.More
			return nil
		})
		return ids, more
	}
	ids, more := byResource(1)
	if !slicesEqual(ids, []string{"ss-b"}) || more {
		t.Errorf("SubjectStatesByResource(limit 1) = %v more=%v, want only the CURRENT ss-b", ids, more)
	}
	// A newer run makes ss-a CURRENT again.
	update(t, s, sessA, func(tx store.Tx) error {
		ra2 := run(t, "run-a2", subject("suite-a"), tx.NextSeq())
		noErr(t, semantic(t, tx).InsertObservationRun(ra2))
		observe(tx, ra2, "obs-a2")
		_, err := semantic(t, tx).PutSubjectState(state(ra2, "ss-a", "obs-a2", tx.NextSeq(), domain.ApplicabilityCurrent), 2, "obs-a2")
		return err
	})
	if ids, _ := byResource(5); !slicesEqual(ids, []string{"ss-a", "ss-b"}) {
		t.Errorf("SubjectStatesByResource after ss-a is CURRENT again = %v, want [ss-a ss-b]", ids)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// testSemanticResourceUpdatesAffectingPath checks the indexed path-change
// read (G2, SEC-1.7, DUR-1.2): only updates that may change a path, those
// naming it or an ancestor directory and every ALL-paths update, in
// (Seq, ID) order and paged; unrelated edits, sibling prefixes and other
// resources never appear.
func testSemanticResourceUpdatesAffectingPath(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "other", tx.NextSeq())))
		for i, paths := range [][]string{{"docs/b.md"}, {"src/a.go"}, {"src"}, nil, {"docs/b.md", "src/a.go.bak"}, {"src/a.go", "src/z.go"}} {
			noErr(t, sem.InsertResourceUpdate(NewResourceUpdate(sessA, fmt.Sprintf("u%d", i+1), "repo", tx.NextSeq(), uint64(i), fpA, paths...)))
		}
		return sem.InsertResourceUpdate(NewResourceUpdate(sessA, "o1", "other", tx.NextSeq(), 0, fpA, "src/a.go"))
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		var ids []string
		p := store.Page{Limit: 2}
		for {
			pg, err := r.ResourceUpdatesAffectingPath("repo", "src/a.go", p)
			noErr(t, err)
			for _, u := range pg.Records {
				ids = append(ids, u.ID)
			}
			if !pg.More {
				break
			}
			p.After = pg.Next
		}
		if !slicesEqual(ids, []string{"u2", "u3", "u4", "u6"}) {
			t.Errorf("ResourceUpdatesAffectingPath(repo, src/a.go) = %v, want [u2 u3 u4 u6]", ids)
		}
		for _, bad := range []string{"", ".", "../x", "/abs", "src/./a.go"} {
			_, err := r.ResourceUpdatesAffectingPath("repo", bad, store.Page{Limit: 2})
			if !errors.Is(err, domain.ErrInvalidRecord) {
				t.Errorf("path %q: error = %v, want ErrInvalidRecord", bad, err)
			}
		}
		return nil
	})
}
