package obligation

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

const testSession = "s1"

func hashOf(s string) string { return domain.HashBytes([]byte(s)) }

func testsTarget(mod func(*domain.TestsTarget)) domain.TargetSpec {
	t := domain.TestsTarget{ResourceID: "repo1", BaseDir: ".", WorkingDir: ".", EnvironmentSpec: "env1", SuiteSpec: "go-test-all", CoverageSpec: "all"}
	if mod != nil {
		mod(&t)
	}
	return domain.TargetSpec{Tests: &t}
}

func fileTarget(resource, p string, mode domain.FileContentMode, required string) domain.TargetSpec {
	return domain.TargetSpec{File: &domain.FileTarget{
		Locator:      domain.ResourceLocator{ResourceID: resource, BaseDir: ".", Path: p},
		Mode:         mode,
		RequiredHash: required,
	}}
}

func mustSubjectKey(t domain.TargetSpec) string {
	k, err := SubjectKeyFor(t)
	if err != nil {
		panic(err)
	}
	return k
}

// Seeding helpers. Principals and items use storetest's "wf"/"task"/"agent"
// owners.

func actorOf(a domain.Authority) domain.Principal { return storetest.NewPrincipal(testSession, a) }

func mustUpdate(t *testing.T, st store.Store, fn func(tx store.Tx) error) {
	t.Helper()
	if err := st.Update(context.Background(), testSession, fn); err != nil {
		t.Fatal(err)
	}
}

// seedPinned stores a current Pinned directive of the given authority.
func seedPinned(t *testing.T, st store.Store, id, dirID string, a domain.Authority, text string) domain.ContextItem {
	t.Helper()
	var it domain.ContextItem
	mustUpdate(t, st, func(tx store.Tx) error {
		it = storetest.NewDirective(testSession, id, dirID, tx.NextSeq(), text)
		it.Authority = a
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		return tx.SetCurrentVersion(it.ID)
	})
	return it
}

func seedTask(t *testing.T, st store.Store, taskID string) {
	t.Helper()
	mustUpdate(t, st, func(tx store.Tx) error {
		ev := storetest.NewLifecycleEvent(testSession, "task-"+taskID, tx.NextSeq(), domain.TargetTask, taskID)
		_, err := tx.PutTask(storetest.NewTask(testSession, taskID), 0, ev)
		return err
	})
}

// seedResource registers a resource directly through the facet; the
// registration service lands with its result kind (W1 request).
func seedResource(t *testing.T, st store.Store, resourceID string, reporter domain.Principal) {
	t.Helper()
	mustUpdate(t, st, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		return sem.InsertResourceBinding(domain.ResourceBinding{
			SemanticMeta: domain.SemanticMeta{ID: "rb-" + resourceID, SessionID: testSession, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
			ResourceID:   resourceID, Owner: reporter, Reporter: reporter,
			Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: testSession},
		})
	})
}

func taskBoundary() domain.AccessBoundary { return storetest.DirectiveBoundary(testSession) }

// seedResourceState records an authoritative KNOWN state directly through the
// facet (a stand-in until resource reporting is exercised end to end).
func seedResourceState(t *testing.T, st store.Store, resourceID string, rev uint64, fp string) {
	t.Helper()
	mustUpdate(t, st, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		seq := tx.NextSeq()
		var prior domain.ResourceState
		if cur, err := sem.ResourceState(resourceID); err == nil {
			prior = cur
		}
		u := domain.ResourceUpdate{
			SemanticMeta: domain.SemanticMeta{ID: "ru-seed-" + fp[7:15], SessionID: testSession, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
			ResourceID:   resourceID, RequestID: "seed-" + fp[7:15], Reporter: actorOf(domain.AuthorityHarness),
			ExpectedAuthoritativeRevision: prior.AuthoritativeRevision, ResultingAuthoritativeRevision: rev,
			WorkspaceFingerprint: fp, Freshness: domain.ResourceKnown, Resynchronization: true, AllPaths: true,
		}
		if err := sem.InsertResourceUpdate(u); err != nil {
			return err
		}
		_, err = sem.PutResourceState(domain.ResourceState{
			SemanticMeta: domain.SemanticMeta{ID: "rs-" + resourceID, SessionID: testSession, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
			ResourceID:   resourceID, BindingID: "rb-" + resourceID, LastUpdateID: u.ID,
			AuthoritativeRevision: rev, WorkspaceFingerprint: fp, Freshness: domain.ResourceKnown, Revision: prior.Revision + 1,
		}, prior.Revision)
		return err
	})
}

// seedItemTx builds a Pinned directive item for insertion in tx.
func seedItemTx(tx store.Tx, id, dirID string, a domain.Authority, text string) domain.ContextItem {
	it := storetest.NewDirective(testSession, id, dirID, tx.NextSeq(), text)
	it.Authority = a
	return it
}

func equalTargetPtr(a, b *domain.TargetSpec) bool {
	return a == nil && b == nil || a != nil && b != nil && equalTarget(*a, *b)
}

// seedEvidenceAs stores TOOL evidence with an arbitrary boundary, aligning
// the item's owner fields with it.
func seedEvidenceAs(t *testing.T, st store.Store, id string, access domain.AccessBoundary) domain.ContextItem {
	t.Helper()
	var it domain.ContextItem
	mustUpdate(t, st, func(tx store.Tx) error {
		it = storetest.NewItem(testSession, id, tx.NextSeq(), "PASS 42 tests")
		it.Kind, it.Authority, it.Scope, it.Access = domain.KindToolResult, domain.AuthorityTool, access.Scope, access
		it.TaskID, it.WorkflowID, it.AgentID = access.TaskID, access.WorkflowID, access.AgentID
		if it.TaskID == "" {
			it.TaskID = "task"
		}
		return tx.InsertItem(it)
	})
	return it
}
