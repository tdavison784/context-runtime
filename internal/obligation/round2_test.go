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

// H1 (SEC-2.1 = SPEC-2.1 = DUR-2.1): ordering is by run ordinal per subject,
// never by fingerprint equality or subject-state freshness. A newer complete
// FAIL defeats an older PASS after the workspace moves away and reverts
// (the FAIL's state is STALE) ...
func TestH1StalePassAfterRevert(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	older, newer := f.newRun(t), f.newRun(t)
	f.report(t, newer, domain.OutcomeFail, hashOf("W1"), nil)
	f.r.set(t, f.fixture, hashOf("W2"), false)
	f.r.set(t, f.fixture, hashOf("W1"), false)
	f.report(t, older, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("older PASS satisfied after a newer FAIL and a revert: %+v", o)
	}
}

// ... and when the newer FAIL was never applicable (it observed another
// fingerprint, so it filed no state).
func TestH1StalePassAfterInapplicableFail(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	older, newer := f.newRun(t), f.newRun(t)
	f.r.set(t, f.fixture, hashOf("W2"), false)
	f.report(t, newer, domain.OutcomeFail, hashOf("W1"), nil)
	f.r.set(t, f.fixture, hashOf("W1"), false)
	f.report(t, older, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("older PASS satisfied after a newer inapplicable FAIL: %+v", o)
	}
}

// H1/SEC-2.9: a newer FAIL private to another agent is not applicable to a
// TASK-wide obligation, so an older task-wide PASS still satisfies it.
func TestH1PrivateFailDoesNotOutrankTaskPass(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	older := f.newRun(t)
	f.reportPrivate(t, "b-newer-fail", domain.OutcomeFail, hashOf("W1"))
	f.report(t, older, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Fatalf("agent-private FAIL outranked the task-wide PASS: %+v", o)
	}
}

// H2 regressions: every "is it current / closed / where did it come from"
// question is one keyed read, so each path below works after more history
// than the test policy's whole work budget (64 units, page size 2).
const h2History = 70

// DUR-2.2 = SEC-2.5 = XREV-2.2 (H2): path currency ignores unrelated edits
// recorded AFTER the path's content, for a CURRENT_PATH assertion and for a
// file_read observation alike.
func TestH2PathCurrencyIgnoresLaterUnrelatedEdits(t *testing.T) {
	f := newEvalFixture(t)
	ref := f.fileObligation(t, "13")
	f.matcherGrant(t, "g-file", ref, FileReadV1, f.userP)
	f.resourceReport(t, "W-a", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	rev := f.r.auth
	for i := range h2History {
		f.resourceReport(t, fmt.Sprintf("later%d", i), false, false, []string{"docs/b.md"})
	}
	if err := f.assertPath(t, ref, rev, "H1"); err != nil {
		t.Fatalf("current-path claim after %d unrelated edits: %v", h2History, err)
	}
	g := newEvalFixture(t)
	ref2 := g.fileObligation(t, "13")
	g.matcherGrant(t, "g-file", ref2, FileReadV1, g.userP)
	g.resourceReport(t, "W-a", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	for i := range h2History {
		g.resourceReport(t, fmt.Sprintf("later%d", i), false, false, []string{"docs/b.md"})
	}
	runN++
	run, err := g.registerRun(t, g.harness, runIntent(fmt.Sprintf("run-%d", runN), fmt.Sprintf("exec-%d", runN), fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, "")))
	if err != nil {
		t.Fatal(err)
	}
	g.report(t, run, domain.OutcomePass, hashOf("H1"), nil)
	if o := g.status(t, ref2); o.Status != domain.ObligationSatisfied {
		t.Errorf("file_read after %d unrelated edits: %+v", h2History, o)
	}
}

// DUR-2.3 = XREV-2.1 (H2): invalidating a proof finds its installing
// transition by ID, so a version's accumulated satisfy/invalidate history
// never wedges resource reports.
func TestH2InvalidationIgnoresTransitionHistory(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	fp := "W1"
	for i := range h2History / 2 {
		f.report(t, f.newRun(t), domain.OutcomePass, hashOf(fp), nil)
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
			t.Fatalf("cycle %d: PASS did not satisfy: %+v", i, o)
		}
		fp = fmt.Sprintf("C%d", i)
		f.r.set(t, f.fixture, hashOf(fp), false)
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
			t.Fatalf("cycle %d: workspace change kept the proof: %+v", i, o)
		}
	}
}

// DUR-2.6 (H2): a run's closing check is one keyed read, so many PARTIAL
// reports never wedge the run or its final complete outcome.
func TestH2PartialReportsDoNotWedgeRun(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	run := f.newRun(t)
	partial := func(in *domain.ObservationIntent) { in.Completeness = domain.ObservationPartial }
	for range h2History {
		f.report(t, run, domain.OutcomePass, hashOf("W1"), partial)
	}
	f.report(t, run, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Errorf("final complete PASS after %d partials: %+v", h2History, o)
	}
}

// H2: the current SATISFIES view reads the version's current proof by key;
// accumulated satisfy/invalidate history never wedges it.
func TestH2CurrentSatisfiesIgnoresHistory(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	fp := "W1"
	for i := range h2History / 2 {
		f.report(t, f.newRun(t), domain.OutcomePass, hashOf(fp), nil)
		fp = fmt.Sprintf("C%d", i)
		f.r.set(t, f.fixture, hashOf(fp), false)
	}
	_, obs := f.observeTests(t, f.target, domain.OutcomePass, hashOf(fp), nil)
	var v SatisfiesView
	var err error
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		v, err = f.s.Satisfies(tx, f.harness, f.sysTests, true)
		return nil
	})
	if err != nil || len(v.Relations) != 1 || v.Relations[0].Evidence.ItemID != obs.EvidenceItemID || !v.Relations[0].Current {
		t.Fatalf("current SATISFIES after %d transitions = %+v, %v", h2History, v, err)
	}
}
