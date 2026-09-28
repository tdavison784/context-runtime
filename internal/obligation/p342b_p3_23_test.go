package obligation

import (
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_23_InvalidationFansOutAcrossTasksAndPrivateProofs: one resource edit
// reaches every live proof bound to that resource — across task partitions and
// into proofs resting on TURN-private evidence (P3-23 — the cited
// TestConcurrency_InvalidationVsObservation runs one task, one obligation, and
// no private proof, so nothing of the fan-out's reach is asserted). Three live
// proofs of one session share resource repo1: task one's public proof, task
// two's own proof in its own partition (own source, binding, declaration,
// grant, run, and report), and a proof of task one resting on TURN-scoped
// evidence narrower than its obligation's TASK boundary (§11's residual: turn
// lifetime never widens or shields a proof — only a resource change
// invalidates it). One edit to W2 invalidates all three in the same
// transaction: every obligation is UNRESOLVED with no current proof, every
// proof non-live. And the fan-out is not a wedge: task one re-satisfies at
// the new authoritative content.
func TestP3_23_InvalidationFansOutAcrossTasksAndPrivateProofs(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		f.matcherGrant(t, "g-23a", f.sysTests, TestsPassV1, f.system)

		// Task one's public proof.
		f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("task-one proof not established: %+v", o)
		}

		// Task two: own source, own binding, own declaration, own grant, own
		// run and report — a proof private to task two's partition.
		seedTask(t, f.st, "task2")
		h2 := f.harness
		h2.TaskID = "task2"
		task2Boundary := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task2"}
		ws2 := bindIntent("ws2-23", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task2"})
		ws2.Access = task2Boundary
		var t2src domain.ContextItem
		mustUpdate(t, f.st, func(tx store.Tx) error {
			t2src = storetest.NewDirective(testSession, "p-23t2", "dir23t2", tx.NextSeq(), "All tests must pass, twice.")
			t2src.Authority = domain.AuthorityUser
			t2src.TaskID = "task2"
			t2src.Access = task2Boundary
			if err := tx.InsertItem(t2src); err != nil {
				return err
			}
			return storetest.UncheckedSetCurrentVersion(tx, t2src.ID)
		})
		if _, err := f.s.bindWS(t, f.st, h2, ws2); err != nil {
			t.Fatalf("bind ws2-23: %v", err)
		}
		if _, err := f.s.declare(t, f.st, h2, harnessDecl("d23-t2", t2src.ID, 1, "1")); err != nil {
			t.Fatalf("task-two declaration: %v", err)
		}
		t2key, _ := t2src.CurrentKey()
		n, _ := harnessSlot("1")
		t2ref := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(t2key, n), Version: 1}
		if o := f.status(t, t2ref); o.BindingState != domain.BindingBound {
			t.Fatalf("task-two obligation = %+v", o)
		}
		sys2 := f.system // a grant for task two's obligation is issued from task two's partition
		sys2.TaskID = "task2"
		f.matcherGrant(t, "g-23b", t2ref, TestsPassV1, sys2)
		in2 := runIntent(fmt.Sprintf("run-23t2-%d", runN+1), fmt.Sprintf("exec-23t2-%d", runN+1), f.target)
		in2.TaskID = "task2"
		in2.Access, in2.Binding = task2Boundary, domain.WorkspaceBindingRef{ID: "ws2-23", Version: 1}
		run2, err := f.registerRun(t, h2, in2)
		if err != nil {
			t.Fatalf("task-two run: %v", err)
		}
		runN++
		if _, err := f.observe(t, h2, obsIntent(fmt.Sprintf("obs-23t2-%d", runN), run2, evidenceFor(t, f.st, run2).ID, domain.OutcomePass, hashOf("W1"))); err != nil {
			t.Fatalf("task-two report: %v", err)
		}
		if o := f.status(t, t2ref); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("task-two proof not established: %+v", o)
		}

		// Task one again, this time on TURN-private evidence: same run
		// discipline, evidence narrower than the obligation's boundary.
		in3 := runIntent(fmt.Sprintf("run-23p-%d", runN+1), fmt.Sprintf("exec-23p-%d", runN+1), f.target)
		run3, err := f.registerRun(t, f.harness, in3)
		if err != nil {
			t.Fatalf("private-evidence run: %v", err)
		}
		runN++
		turnEv := fmt.Sprintf("ev-turn-%d", runN)
		mustUpdate(t, f.st, func(tx store.Tx) error {
			it := evidenceItem(tx.NextSeq(), run3)
			it.ID, it.EventID, it.TurnID, it.CreatedTurn = turnEv, "evt-"+turnEv, "turn-23", 1
			it.Scope = domain.ScopeTurn
			it.Access = domain.AccessBoundary{Scope: domain.ScopeTurn, SessionID: testSession, TaskID: "task"}
			return tx.InsertItem(it)
		})
		if _, err := f.observe(t, f.harness, obsIntent(fmt.Sprintf("obs-23p-%d", runN), run3, turnEv, domain.OutcomePass, hashOf("W1"))); err != nil {
			t.Fatalf("private-evidence report: %v", err)
		}
		private := f.status(t, f.sysTests)
		if private.Status != domain.ObligationSatisfied || private.CurrentProofID == "" {
			t.Fatalf("TURN-evidenced proof not established: %+v", private)
		}

		// One edit. The fan-out must reach all three proofs.
		f.r.set(t, f.fixture, hashOf("W2"), false)
		f.wantInvalidated(t, f.sysTests, "repo1", "edit left task one's public proof live")
		// The TURN-evidenced proof and task two's proof settle through the
		// same restricted path without being left pending, so settle first
		// and read the recorded result.
		for more, passes := true, 0; more; passes++ {
			if passes > 100 {
				t.Fatal("settlement never finishes")
			}
			_, more = f.settle(t, 64)
		}
		f.wantSettled(t, f.sysTests, "repo1", "edit left the TURN-evidenced proof live")
		f.wantSettled(t, t2ref, "repo1", "edit left task two's proof live")
		for name, ref := range map[string]domain.ObligationRef{
			"task one":   f.sysTests,
			"task two":   t2ref,
			"turn proof": f.sysTests,
		} {
			if o := f.status(t, ref); o.Status != domain.ObligationUnresolved || o.CurrentProofID != "" {
				t.Fatalf("%s still carries a live proof after the edit: %+v", name, o)
			}
		}

		// The fan-out is not a wedge: task one re-satisfies at the new
		// authoritative content.
		f.r.set(t, f.fixture, hashOf("W2"), true)
		f.observeTests(t, f.target, domain.OutcomePass, hashOf("W2"), nil)
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("no re-satisfaction after fan-out: %+v", o)
		}
	})
}
