package retrieve

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// P3-22 (SPEC-4.10, ruling L1 item 2): an OBSERVATION task_state is CURRENT
// only while it still describes the current authoritative resource state of
// its subject. The supersession pointer cannot say that - it moves only when
// a later run files a newer state - so Get must derive applicability at read
// and never present an old PASS as current truth.

// observationStores runs f on the memory store and a fresh SQLite store.
func observationStores(t *testing.T, f func(*testing.T, store.Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) { f(t, memory.New()) })
	t.Run("sqlite", func(t *testing.T) { f(t, sqlitetest.Open(t)) })
}

var observationFP = domain.HashBytes([]byte("observation workspace"))

// fileObservationState files one current obs-state/1 item for a complete
// PASS observed at observationFP over resource "repo", and returns the item.
// The supersession pointer names it, so only derived applicability can make
// it historical.
func fileObservationState(t *testing.T, s store.Store) domain.ContextItem {
	t.Helper()
	actor := storetest.TaskHarness("s")
	var it domain.ContextItem
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		if _, err := tx.PutTask(storetest.NewTask("s", "task"), 0, domain.LifecycleEvent{ID: "task-open", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: "task", Action: "open", Actor: actor}); err != nil {
			return err
		}
		if err := sem.InsertResourceBinding(storetest.NewResourceBinding("s", "repo", tx.NextSeq())); err != nil {
			return err
		}
		if err := sem.InsertWorkspaceBinding(storetest.NewWorkspaceBinding("s", "wb", "repo", 1, tx.NextSeq())); err != nil {
			return err
		}
		u1 := storetest.NewResourceUpdate("s", "u1", "repo", tx.NextSeq(), 0, observationFP)
		if err := sem.InsertResourceUpdate(u1); err != nil {
			return err
		}
		if _, err := sem.PutResourceState(storetest.StateAfter(u1, tx.NextSeq()), 0); err != nil {
			return err
		}
		run := storetest.NewObservationRun(t, "s", "run1", "repo", "wb", tx.NextSeq())
		if err := sem.InsertObservationRun(run); err != nil {
			return err
		}
		if err := tx.InsertItem(storetest.ProducedEvidence("s", "ev1", tx.NextSeq(), run.ExecutionID)); err != nil {
			return err
		}
		obs := storetest.NewObservation(run, "obs1", "ev1", tx.NextSeq(), observationFP)
		if err := sem.InsertObservation(obs); err != nil {
			return err
		}
		it = observationStateItem(run, obs, tx.NextSeq())
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := graph.FileObservationState(tx, actor, it.ID, run.SubjectKey, ""); err != nil {
			return err
		}
		_, err = sem.PutSubjectState(domain.SubjectState{
			SemanticMeta: storetest.Meta("s", "ss1", tx.NextSeq()), SubjectKey: run.SubjectKey, TaskID: "task",
			CurrentItemID: it.ID, ObservationID: obs.ID, Access: run.Access, AcceptedOrdinal: run.Ordinal,
			Revision: 1, Applicability: domain.ApplicabilityCurrent,
		}, 0, obs.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return it
}

// observationStateItem renders the fixed obs-state/1 shape the obligation
// service files, from typed fields only.
func observationStateItem(run domain.ObservationRun, obs domain.ObservationRecord, seq uint64) domain.ContextItem {
	identity := obs.ObservedWorkspaceFingerprint
	if run.Subject.Family == domain.ObservationFileRead {
		identity = obs.ObservedContentHash
	}
	text := fmt.Sprintf("Observed %s %s (%s): %d passed, %d failed, %d skipped of %d; subject %s at %s.",
		obs.Family, obs.Outcome, obs.Completeness, obs.Passed, obs.Failed, obs.Skipped, obs.Total, run.SubjectKey, identity)
	parts := []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: text}}
	return domain.ContextItem{
		ID: "ost-1", EventID: obs.ID, DirectiveID: run.SubjectKey, Namespace: domain.NamespaceObservation,
		Role: domain.RoleSemantic, Seq: seq, SessionID: "s", TaskID: "task",
		Kind:       domain.KindTaskState,
		Generation: domain.GenerationWorking, Authority: domain.AuthorityTool,
		Scope: domain.ScopeTask, Access: run.Access, Residency: domain.ResidencyResident, Retention: domain.RetentionNormal,
		Parts: parts, ContentHash: domain.ContentHash(parts), SemanticBytes: domain.SemanticBytes(parts), Version: 1,
	}
}

// editObservedResource advances the resource to a new fingerprint without
// any new observation run, so the filed state stays the pointer's target.
func editObservedResource(t *testing.T, s store.Store) {
	t.Helper()
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		u2 := storetest.NewResourceUpdate("s", "u2", "repo", tx.NextSeq(), 1, domain.HashBytes([]byte("edited workspace")), "src/a.go")
		if err := sem.InsertResourceUpdate(u2); err != nil {
			return err
		}
		_, err = sem.PutResourceState(storetest.StateAfter(u2, tx.NextSeq()), 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGetObservationStateStaleAfterResourceEditIsHistorical(t *testing.T) {
	observationStores(t, func(t *testing.T, s store.Store) {
		it := fileObservationState(t, s)
		editObservedResource(t, s)
		got, err := New(s).Get(context.Background(), storetest.NewPrincipal("s", domain.AuthorityHarness), it.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Observed.Currentness != domain.ItemHistorical {
			t.Fatalf("Get after the resource moved on = %s, want HISTORICAL", got.Observed.Currentness)
		}
	})
}

// The derivation fails closed (ruling L1 item 2): a reader that cannot read
// the underlying resource state labels the item HISTORICAL. The label is a
// constant, so Get reports no error and adds no resource detail - no oracle.
func TestGetObservationStateUnreadableResourceIsHistorical(t *testing.T) {
	observationStores(t, func(t *testing.T, s store.Store) {
		it := fileObservationState(t, s)
		got, err := New(unreadableResourceStore{Store: s}).Get(context.Background(), storetest.NewPrincipal("s", domain.AuthorityHarness), it.ID)
		if err != nil {
			t.Fatalf("Get with an unreadable resource = %v, want a HISTORICAL snapshot", err)
		}
		if got.Observed.Currentness != domain.ItemHistorical {
			t.Fatalf("Get with an unreadable resource = %s, want HISTORICAL", got.Observed.Currentness)
		}
	})
}

// A state that still matches the authoritative resource state stays CURRENT:
// the derivation refines the pointer, it does not replace it.
func TestGetObservationStateMatchingResourceStaysCurrent(t *testing.T) {
	observationStores(t, func(t *testing.T, s store.Store) {
		it := fileObservationState(t, s)
		got, err := New(s).Get(context.Background(), storetest.NewPrincipal("s", domain.AuthorityHarness), it.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Observed.Currentness != domain.ItemCurrent {
			t.Fatalf("Get of the matching observation state = %s, want CURRENT", got.Observed.Currentness)
		}
		if got.Observed.Expiry != domain.ExpiryLive {
			t.Fatalf("matching observation state expiry = %s, want LIVE", got.Observed.Expiry)
		}
	})
}

// The file-family counterpart (P3-22, ruling L1.2): a path-content
// observation is CURRENT while the path's current recorded content is what
// it observed. A report that names the path and records changed content
// makes it HISTORICAL; a same-content path report - even with a moved
// workspace fingerprint - keeps it CURRENT.
func TestGetFileObservationStateFollowsPathContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    domain.ItemCurrentness
	}{
		{"content-changing path report", "content-v2", domain.ItemHistorical},
		{"same-content path report", "content-v1", domain.ItemCurrent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			observationStores(t, func(t *testing.T, s store.Store) {
				it := fileReadObservationState(t, s)
				reportPathContent(t, s, tc.content)
				got, err := New(s).Get(context.Background(), storetest.NewPrincipal("s", domain.AuthorityHarness), it.ID)
				if err != nil {
					t.Fatal(err)
				}
				if got.Observed.Currentness != tc.want {
					t.Fatalf("Get after %s = %s, want %s", tc.name, got.Observed.Currentness, tc.want)
				}
			})
		})
	}
}

// fileReadObservationState files one current obs-state/1 item for a
// complete PASS file read of "docs/a.md" at content "content-v1", and
// returns the item.
func fileReadObservationState(t *testing.T, s store.Store) domain.ContextItem {
	t.Helper()
	actor := storetest.TaskHarness("s")
	subject := domain.ObservationSubject{Family: domain.ObservationFileRead, Target: domain.TargetSpec{File: &domain.FileTarget{
		Locator: domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: "docs/a.md"}, Mode: domain.FileCurrentContent}}}
	key, err := subject.Key()
	if err != nil {
		t.Fatal(err)
	}
	var it domain.ContextItem
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		if _, err := tx.PutTask(storetest.NewTask("s", "task"), 0, domain.LifecycleEvent{ID: "task-open", SessionID: "s", Seq: tx.NextSeq(), TargetKind: domain.TargetTask, TargetID: "task", Action: "open", Actor: actor}); err != nil {
			return err
		}
		if err := sem.InsertResourceBinding(storetest.NewResourceBinding("s", "repo", tx.NextSeq())); err != nil {
			return err
		}
		if err := sem.InsertWorkspaceBinding(storetest.NewWorkspaceBinding("s", "wb", "repo", 1, tx.NextSeq())); err != nil {
			return err
		}
		u1 := storetest.NewResourceUpdate("s", "u1", "repo", tx.NextSeq(), 0, observationFP, "docs/a.md")
		if err := sem.InsertResourceUpdate(u1); err != nil {
			return err
		}
		if _, err := sem.PutResourceState(storetest.StateAfter(u1, tx.NextSeq()), 0); err != nil {
			return err
		}
		if _, err := sem.PutResourcePathState(domain.ResourcePathState{
			SemanticMeta: storetest.Meta("s", "ps-docs", tx.NextSeq()), Locator: domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: "docs/a.md"},
			ContentHash: domain.HashBytes([]byte("content-v1")), ResourceUpdateID: u1.ID,
			ResourceRevision: u1.ResultingAuthoritativeRevision, Revision: 1, Freshness: domain.ResourceKnown,
		}, 0); err != nil {
			return err
		}
		runSeq := tx.NextSeq()
		run := domain.ObservationRun{SemanticMeta: storetest.Meta("s", "run-f1", runSeq), Subject: subject, SubjectKey: key, Ordinal: runSeq,
			ExecutionID: "exec-run-f1", Binding: domain.WorkspaceBindingRef{ID: "wb", Version: 1}, TaskID: "task",
			Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}, Reporter: storetest.HarnessPrincipal("s")}
		if err := sem.InsertObservationRun(run); err != nil {
			return err
		}
		if err := tx.InsertItem(storetest.ProducedEvidence("s", "ev-f1", tx.NextSeq(), run.ExecutionID)); err != nil {
			return err
		}
		obs := storetest.NewObservation(run, "obs-f1", "ev-f1", tx.NextSeq(), "")
		obs.ObservedContentHash = domain.HashBytes([]byte("content-v1"))
		if err := sem.InsertObservation(obs); err != nil {
			return err
		}
		it = observationStateItem(run, obs, tx.NextSeq())
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := graph.FileObservationState(tx, actor, it.ID, run.SubjectKey, ""); err != nil {
			return err
		}
		_, err = sem.PutSubjectState(domain.SubjectState{
			SemanticMeta: storetest.Meta("s", "ss-f1", tx.NextSeq()), SubjectKey: run.SubjectKey, TaskID: "task",
			CurrentItemID: it.ID, ObservationID: obs.ID, Access: run.Access, AcceptedOrdinal: run.Ordinal,
			Revision: 1, Applicability: domain.ApplicabilityCurrent,
		}, 0, obs.ID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return it
}

// reportPathContent reports "docs/a.md" named on a new update with a moved
// workspace fingerprint, recording content as the path's current content.
func reportPathContent(t *testing.T, s store.Store, content string) {
	t.Helper()
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		u2 := storetest.NewResourceUpdate("s", "u2", "repo", tx.NextSeq(), 1, domain.HashBytes([]byte("edited workspace")), "docs/a.md")
		if err := sem.InsertResourceUpdate(u2); err != nil {
			return err
		}
		if _, err := sem.PutResourceState(storetest.StateAfter(u2, tx.NextSeq()), 1); err != nil {
			return err
		}
		_, err = sem.PutResourcePathState(domain.ResourcePathState{
			SemanticMeta: storetest.Meta("s", "ps-docs", tx.NextSeq()), Locator: domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: "docs/a.md"},
			ContentHash: domain.HashBytes([]byte(content)), ResourceUpdateID: u2.ID,
			ResourceRevision: u2.ResultingAuthoritativeRevision, Revision: 1, Freshness: domain.ResourceKnown,
		}, 1)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// errResourceUnreadable stands in for a failing resource read.
var errResourceUnreadable = errors.New("retrieve test: resource unreadable")

// unreadableResourceStore serves reads through a SemanticReader whose
// ResourceState fails, standing in for a store whose underlying resource
// reads error.
type unreadableResourceStore struct{ store.Store }

func (u unreadableResourceStore) View(ctx context.Context, sessionID string, fn func(store.ReadTx) error) error {
	return u.Store.View(ctx, sessionID, func(tx store.ReadTx) error { return fn(unreadableResourceTx{ReadTx: tx}) })
}

type unreadableResourceTx struct{ store.ReadTx }

// SemanticReadBackend implements store.SemanticReadProvider, so reads through
// store.ReadSemantic keep working with only ResourceState faulted.
func (t unreadableResourceTx) SemanticReadBackend() store.SemanticReader {
	r, err := store.ReadSemantic(t.ReadTx)
	if err != nil {
		return nil
	}
	return unreadableResourceReader{SemanticReader: r}
}

type unreadableResourceReader struct{ store.SemanticReader }

func (r unreadableResourceReader) ResourceState(string) (domain.ResourceState, error) {
	return domain.ResourceState{}, errResourceUnreadable
}
