package obligation

import (
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// DUR-2.14 (DUR-1.3 obligation half): a caller may defer the sequence (0);
// the service allocates it only after the replay check, so an exact replay
// consumes no sequence.
func TestDeferredSeqAllocatedAfterReplay_DUR214(t *testing.T) {
	f := newBareFixture(t)
	rep := sessionReporter()
	in := domain.RegisterResourceIntent{RequestID: "reg-deferred", ResourceID: "repo-deferred", Reporter: rep, Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: testSession}}
	var first, again domain.MutationResult
	var afterFirst, afterReplay uint64
	mustUpdate(t, f.st, func(tx store.Tx) error {
		var err error
		first, err = f.s.RegisterResourceTx(tx, rep, in, 0)
		afterFirst = tx.LastSeq()
		return err
	})
	mustUpdate(t, f.st, func(tx store.Tx) error {
		var err error
		again, err = f.s.RegisterResourceTx(tx, rep, in, 0)
		afterReplay = tx.LastSeq()
		return err
	})
	if first.Records == nil || again.Records == nil || first.Records.IDs[0] != again.Records.IDs[0] {
		t.Fatalf("replay = %+v, want %+v", again, first)
	}
	if afterReplay != afterFirst {
		t.Errorf("exact replay allocated a sequence: last seq %d -> %d", afterFirst, afterReplay)
	}
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		b, err := r.ResourceBinding("repo-deferred")
		if err != nil || b.Seq != afterFirst {
			t.Errorf("binding = %+v %v, want seq %d", b, err, afterFirst)
		}
		return nil
	})
}

// SPEC-2.3 (P3-39, H4): an observation-state supersession produces one
// SUPERSESSION GC request for the run's task under the recorded policy; the
// first state of a subject supersedes nothing and produces none.
func TestObservationStateSupersessionEnqueuesGC_SPEC23(t *testing.T) {
	f := newFixture(t)
	var r repo1
	target := testsTarget(nil)
	r.set(t, f, hashOf("W1"), true)
	requests := func() []domain.GCRequest {
		var out []domain.GCRequest
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			rd, _ := store.ReadSemantic(tx)
			pg, err := rd.PendingGCRequests(store.Page{Limit: 16})
			if err != nil {
				t.Fatal(err)
			}
			for _, g := range pg.Records {
				if g.Trigger == domain.GCSupersession {
					out = append(out, g)
				}
			}
			return nil
		})
		return out
	}
	f.observeTests(t, target, domain.OutcomeFail, hashOf("W1"), nil)
	if got := requests(); len(got) != 0 {
		t.Fatalf("first subject state enqueued %+v", got)
	}
	r.set(t, f, hashOf("W2"), false)
	f.observeTests(t, target, domain.OutcomePass, hashOf("W2"), nil)
	got := requests()
	if len(got) != 1 || got[0].Scope != domain.CollectTask || got[0].TaskID != "task" || got[0].PolicyVersion != testPolicy().Version {
		t.Fatalf("supersession requests = %+v", got)
	}
}

// reportPrivate registers and reports a run private to agent b on the
// fixture's subject, returning the observation's record ID.
func (f *evalFixture) reportPrivate(t *testing.T, name string, outcome domain.ObservationOutcome, fp string) string {
	t.Helper()
	b := domain.Principal{SessionID: testSession, WorkflowID: "wf", TaskID: "task", AgentID: "b", Authority: domain.AuthorityHarness}
	private := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task", AgentID: "b"}
	ev := seedEvidenceAs(t, f.st, "ev-"+name, private, "exec-"+name)
	runN++
	in := runIntent(fmt.Sprintf("run-%d", runN), "exec-"+name, f.target)
	in.Access = private
	run, err := f.registerRun(t, b, in)
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.observe(t, b, obsIntent("obs-"+name, run, ev.ID, outcome, fp))
	if err != nil {
		t.Fatal(err)
	}
	return res.ID
}

// SEC-2.9 (H1): a FAIL is applicable to a proof only if its evidence
// boundary covers the proof's boundary. A newer FAIL private to another
// agent never rejects a TASK-wide proof, and its ID is never recorded in the
// task's visible transition history.
func TestPrivateFailNeverRejectsTaskProof_SEC29(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Fatalf("setup = %+v", o)
	}
	private := f.reportPrivate(t, "b-fail", domain.OutcomeFail, hashOf("W1"))
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Errorf("agent-private FAIL rejected the TASK-wide proof: %+v", o)
	}
	for _, tr := range f.history(t, f.sysTests) {
		if tr.CauseRecordID == private || tr.RequestID == private {
			t.Errorf("transition %s records the private observation %s", tr.ID, private)
		}
	}
	// Control: a newer task-wide FAIL still rejects.
	f.report(t, f.newRun(t), domain.OutcomeFail, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Errorf("task-wide FAIL did not reject: %+v", o)
	}
}
