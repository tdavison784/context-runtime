package obligation

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_20_TaskOneBindingUnaffectedByTaskTwoDeclaration: T1's workspace
// binding and the obligations declared under it are untouched by T2's
// binding, declaration, and binding-version bump (P3-20, the missing half of
// the H2 fix — the cited test exercises one task's binding-version history
// only). Two tasks of one session share a resource with identical specs; a
// binding applies only to its own context, so T2's binding is never a
// candidate for T1's sources (no re-binding, no ambiguity, no wedge), T2's
// version numbering is its own, and T2's bump to v2 changes neither T1's
// current binding nor the ws2@v1 reference T2's first obligation recorded.
func TestP3_20_TaskOneBindingUnaffectedByTaskTwoDeclaration(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newFixture(t)
		seedTask(t, f.st, "task2")
		h2 := f.harness
		h2.TaskID = "task2"
		task2Boundary := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task2"}

		// T2's own binding of the same resource with the same specs.
		ws2 := bindIntent("ws2", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task2"})
		ws2.Access = task2Boundary
		if _, err := f.s.bindWS(t, f.st, h2, ws2); err != nil {
			t.Fatalf("bind ws2: %v", err)
		}
		// T2's pinned source lives in task2's boundary.
		var t2src domain.ContextItem
		mustUpdate(t, f.st, func(tx store.Tx) error {
			t2src = storetest.NewDirective(testSession, "p-t2src", "dirt2", tx.NextSeq(), "All tests must pass, twice.")
			t2src.Authority = domain.AuthorityUser
			t2src.TaskID = "task2"
			t2src.Access = task2Boundary
			if err := tx.InsertItem(t2src); err != nil {
				return err
			}
			return storetest.UncheckedSetCurrentVersion(tx, t2src.ID)
		})

		// T1 declares first: bound to its own ws1 v1.
		t1src := seedPinned(t, f.st, "p-t1src", "dirt1", domain.AuthorityUser, "All tests must pass.")
		if _, err := f.s.declare(t, f.st, f.harness, harnessDecl("d20-t1a", t1src.ID, 1, "1")); err != nil {
			t.Fatalf("T1 declaration: %v", err)
		}
		t1key, _ := t1src.CurrentKey()
		t1ref := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(t1key, 1), Version: 1}
		bound := func(ref domain.ObligationRef) domain.ObligationVersion {
			o, _ := loadObligation(t, f.st, ref)
			return o
		}
		o1 := bound(t1ref)
		if o1.BindingState != domain.BindingBound || o1.WorkspaceBindingRef == nil ||
			o1.WorkspaceBindingRef.ID != "ws1" || o1.WorkspaceBindingRef.Version != 1 {
			t.Fatalf("T1 obligation = %+v, want bound to ws1 v1", o1)
		}

		// T2 declares under the same resource and specs: it binds ws2 v1,
		// never T1's binding.
		if _, err := f.s.declare(t, f.st, h2, harnessDecl("d20-t2a", t2src.ID, 1, "1")); err != nil {
			t.Fatalf("T2 declaration: %v", err)
		}
		t2key, _ := t2src.CurrentKey()
		t2ref := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(t2key, 1), Version: 1}
		o2 := bound(t2ref)
		if o2.BindingState != domain.BindingBound || o2.WorkspaceBindingRef == nil ||
			o2.WorkspaceBindingRef.ID != "ws2" || o2.WorkspaceBindingRef.Version != 1 {
			t.Fatalf("T2 obligation = %+v, want bound to ws2 v1", o2)
		}
		// T1 is untouched by T2's declaration: same recorded binding, still
		// current, same revision.
		if after := bound(t1ref); after.BindingState != o1.BindingState || after.WorkspaceBindingRef == nil ||
			*after.WorkspaceBindingRef != *o1.WorkspaceBindingRef || after.Revision != o1.Revision || !after.Current {
			t.Fatalf("T2 declaration changed T1's obligation: %+v -> %+v", o1, after)
		}

		// T2 bumps its binding to v2 over a narrower base directory.
		ws2v2 := ws2
		ws2v2.Version, ws2v2.RequestID, ws2v2.BaseDir = 2, "bind-ws2-v2b", "pkg"
		if _, err := f.s.bindWS(t, f.st, h2, ws2v2); err != nil {
			t.Fatalf("bind ws2 v2: %v", err)
		}
		// T2's first obligation keeps the ws2@v1 it recorded; T1's obligation
		// and current binding are still ws1 v1; and T1 can still declare a
		// second slot resolving ws1 v1 — never v2, never ambiguous, never
		// wedged.
		if after := bound(t2ref); after.WorkspaceBindingRef == nil || after.WorkspaceBindingRef.ID != "ws2" || after.WorkspaceBindingRef.Version != 1 {
			t.Fatalf("ws2 v2 rewrote T2's recorded binding: %+v", after.WorkspaceBindingRef)
		}
		if after := bound(t1ref); after.BindingState != o1.BindingState || after.Revision != o1.Revision || !after.Current ||
			after.WorkspaceBindingRef == nil || *after.WorkspaceBindingRef != *o1.WorkspaceBindingRef {
			t.Fatalf("T2 binding bump changed T1's obligation: %+v -> %+v", o1, after)
		}
		if _, err := f.s.declare(t, f.st, f.harness, harnessDecl("d20-t1b", t1src.ID, 1, "2")); err != nil {
			t.Fatalf("T1 second declaration after T2's bump: %v", err)
		}
		t1b := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(t1key, 2), Version: 1}
		if o1b := bound(t1b); o1b.BindingState != domain.BindingBound || o1b.WorkspaceBindingRef == nil ||
			o1b.WorkspaceBindingRef.ID != "ws1" || o1b.WorkspaceBindingRef.Version != 1 {
			t.Fatalf("T1 second declaration = %+v, want ws1 v1", o1b)
		}

		// The context partition itself: each task's current bindings are
		// exactly its own.
		currentBindings := func(task string) []domain.WorkspaceBinding {
			var out []domain.WorkspaceBinding
			_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
				r, _ := store.ReadSemantic(tx)
				pg, err := r.CurrentWorkspaceBindingsByContext("", task, "", store.Page{Limit: 16})
				if err != nil {
					return err
				}
				out = pg.Records
				return nil
			})
			return out
		}
		if bs := currentBindings("task"); len(bs) != 1 || bs[0].ID != "ws1" || bs[0].Version != 1 {
			t.Fatalf("T1 current bindings = %+v, want exactly ws1 v1", bs)
		}
		if bs := currentBindings("task2"); len(bs) != 1 || bs[0].ID != "ws2" || bs[0].Version != 2 {
			t.Fatalf("T2 current bindings = %+v, want exactly ws2 v2", bs)
		}
	})
}
