package obligation

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// p3_14BothStores runs one exercise on the memory and SQLite backends, the
// suite's way: the SQLite side skips until both the obligation/proof facet
// and the OBSERVATION state namespace are published.
func p3_14BothStores(t *testing.T, exercise func(*testing.T)) {
	t.Helper()
	for name, backend := range map[string]func(*testing.T) store.Store{
		"memory": func(*testing.T) store.Store { return memory.New() },
		"sqlite": sqliteBackend,
	} {
		t.Run(name, func(t *testing.T) {
			if name == "sqlite" {
				st := backend(t)
				if !familySupported(t, st, func(r store.SemanticReader) error {
					_, err := r.ExactObligation(domain.ObligationRef{SessionID: testSession, ObligationID: "probe", Version: 1})
					return err
				}) || !observationNamespaceSupported(t, st) {
					t.Fatal("obligation/observation family unpublished on this backend")
				}
			}
			prev := backendFactory
			backendFactory = backend
			t.Cleanup(func() { backendFactory = prev })
			exercise(t)
		})
	}
}

// TestP3_14_PrivatePassNeverSatisfiesTaskObligation closes the P3-42 row
// "task-wide obligation with agent-private PASS rejected" in its missing
// satisfaction direction: an agent-private PASS is never applicable to a
// TASK-wide obligation. It publishes nothing (no proof, no cached evidence,
// no transition), the trusted reevaluation never selects it — not even for
// its own reporter — and the SATISFIES view stays empty, while the same
// result reported at the task boundary satisfies. The mirror rejection
// direction is TestPrivateFailNeverRejectsTaskProof_SEC29.
func TestP3_14_PrivatePassNeverSatisfiesTaskObligation(t *testing.T) {
	p3_14BothStores(t, exerciseP3_14PrivatePass)
}

func exerciseP3_14PrivatePass(t *testing.T) {
	t.Helper()
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	private := f.reportPrivate(t, "b-pass", domain.OutcomePass, hashOf("W1"))

	if st, pending := f.effective(t, f.sysTests); st != domain.ObligationUnresolved || pending {
		t.Fatalf("agent-private PASS satisfied the TASK-wide obligation: %s pending=%v", st, pending)
	}
	o := f.status(t, f.sysTests)
	if o.CurrentProofID != "" || o.EvidenceIDs != nil || o.Revision != 1 {
		t.Errorf("private PASS left a trace on the obligation: %+v", o)
	}
	if h := f.history(t, f.sysTests); len(h) != 0 {
		t.Errorf("private PASS produced transitions: %+v", h)
	}
	v, err := f.satisfiesOf(t, f.harness, f.sysTests, true)
	if err != nil || len(v.Relations) != 0 || v.Truncated {
		t.Errorf("current SATISFIES after a private PASS = %+v (%v)", v, err)
	}

	// The trusted reevaluation selects only partitions publishable at the
	// obligation's boundary: the private observation is never selected, not
	// even for the harness agent that reported it, and its ID appears in no
	// receipt.
	for name, actor := range map[string]domain.Principal{"harness agent": f.harness, "reporter b": {
		SessionID: testSession, WorkflowID: "wf", TaskID: "task", AgentID: "b", Authority: domain.AuthorityHarness,
	}} {
		res, err := f.reevaluate(t, actor, f.sysTests, 1)
		if err != nil {
			t.Fatalf("reevaluate by %s: %v", name, err)
		}
		if res.Records == nil || len(res.Records.IDs) != 1 {
			t.Errorf("reevaluate by %s selected a non-publishable observation: %+v", name, res)
		}
		for _, id := range res.Records.IDs {
			if id == private {
				t.Errorf("reevaluation receipt by %s named the private observation %s", name, private)
			}
		}
		if st, pending := f.effective(t, f.sysTests); st != domain.ObligationUnresolved || pending {
			t.Fatalf("reevaluate by %s satisfied the obligation: %s pending=%v", name, st, pending)
		}
	}

	// Control: the same result reported at the task boundary satisfies, and
	// the published proof names the task-wide evidence, never the private one.
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	if st, pending := f.effective(t, f.sysTests); st != domain.ObligationSatisfied || pending {
		t.Fatalf("task-wide control did not satisfy: %s pending=%v", st, pending)
	}
	v, err = f.satisfiesOf(t, f.harness, f.sysTests, true)
	if err != nil || len(v.Relations) != 1 || !v.Relations[0].Current || v.Truncated {
		t.Fatalf("satisfied view = %+v (%v)", v, err)
	}
	if ev := v.Relations[0].Evidence.ItemID; ev == "ev-b-pass" || ev == private {
		t.Errorf("published proof rests on private evidence %q", ev)
	}
}

// TestP3_14_PrivateEvidenceNeverLeaks closes the P3-42 row "private evidence
// never leaks via receipt/cache/view" for the obligation-proof case: an
// agent-private obligation legitimately satisfied by agent-private evidence
// is invisible to another agent of the same task through every channel. The
// view refuses identically to a nonexistent version, the visible-obligations
// cache omits it without a trace, and replaying the private mutation's exact
// receipt as another principal is refused exactly like a fresh request —
// never handing back the frozen private result — while the owning agent sees
// the full view.
func TestP3_14_PrivateEvidenceNeverLeaks(t *testing.T) {
	p3_14BothStores(t, exerciseP3_14Leaks)
}

func exerciseP3_14Leaks(t *testing.T) {
	t.Helper()
	f := newEvalFixture(t)
	b := domain.Principal{SessionID: testSession, WorkflowID: "wf", TaskID: "task", AgentID: "b", Authority: domain.AuthorityHarness}
	bAccess := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task", AgentID: "b"}
	other := actorOf(domain.AuthorityHarness) // agent "agent": same task, cannot see b's boundary

	// The private obligation: a b-owned pin, bound through a b-private
	// workspace binding, declared by b. Its boundary is the source's.
	var privRef domain.ObligationRef
	mustUpdate(t, f.st, func(tx store.Tx) error {
		it := storetest.NewDirective(testSession, "p-b-tests", "b-tests", tx.NextSeq(), "All tests must pass.")
		it.Authority = domain.AuthorityHarness
		it.Access, it.AgentID, it.TaskID = bAccess, "b", "task"
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := storetest.UncheckedSetCurrentVersion(tx, it.ID); err != nil {
			return err
		}
		bin := bindIntent("ws-b", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceSource, ID: "p-b-tests"})
		bin.Access = bAccess
		if _, err := f.s.BindWorkspaceTx(tx, b, bin, tx.NextSeq()); err != nil {
			return err
		}
		ref, err := f.s.DeclarePinnedTx(tx, b, it.ID, "", tx.NextSeq())
		if err != nil {
			return err
		}
		privRef = *ref
		return nil
	})
	if o := f.status(t, privRef); o.BindingState != domain.BindingBound || o.Access != bAccess || o.Claim != "tests_pass" {
		t.Fatalf("private obligation = %+v", o)
	}

	// b satisfies it with b-private evidence through the matcher. The grant's
	// issuer must itself be inside the private boundary — a grant never
	// carries authority its issuer lacks (authz) — so b issues it.
	f.matcherGrant(t, "g-b", privRef, TestsPassV1, b)
	ev := seedEvidenceAs(t, f.st, "ev-b-pass", bAccess, "exec-b-pass")
	runN++
	rin := runIntent(fmt.Sprintf("run-%d", runN), "exec-b-pass", f.target)
	// The run rides b's private binding and boundary, so every publication
	// check (evidence, observation, workspace binding) sees b's boundary.
	rin.Access = bAccess
	rin.Binding = domain.WorkspaceBindingRef{ID: "ws-b", Version: 1}
	run, err := f.registerRun(t, b, rin)
	if err != nil {
		t.Fatal(err)
	}
	in := obsIntent("obs-b-pass", run, ev.ID, domain.OutcomePass, hashOf("W1"))
	// The report rides the full receipted path: its mutation receipt is one
	// of the channels under test.
	var obsID string
	mustUpdate(t, f.st, func(tx store.Tx) error {
		res, err := f.s.ReportObservationTx(tx, b, in, tx.NextSeq())
		if err != nil {
			return err
		}
		if res.Records == nil || len(res.Records.IDs) != 1 {
			return errors.New("observation report recorded no result")
		}
		obsID = res.Records.IDs[0]
		return nil
	})
	sat := f.status(t, privRef)
	if st, pending := f.effective(t, privRef); st != domain.ObligationSatisfied || pending || sat.CurrentProofID == "" {
		t.Fatalf("private satisfaction setup failed: %+v (%s pending=%v)", sat, st, pending)
	}

	// View: another agent is refused identically to a nonexistent version —
	// one fixed error that distinguishes nothing.
	_, errPrivate := f.satisfiesOf(t, other, privRef, true)
	_, errMissing := f.satisfiesOf(t, other, domain.ObligationRef{SessionID: testSession, ObligationID: "obl-nonexistent-p3-14", Version: 1}, true)
	if !errors.Is(errPrivate, domain.ErrNotFound) || !errors.Is(errMissing, domain.ErrNotFound) || errPrivate.Error() != errMissing.Error() {
		t.Errorf("Satisfies for another agent = %v, want the uniform refusal %v", errPrivate, errMissing)
	}
	_, errHistory := f.satisfiesOf(t, other, privRef, false)
	if !errors.Is(errHistory, domain.ErrNotFound) || errHistory.Error() != errPrivate.Error() {
		t.Errorf("history view for another agent = %v, want the same uniform refusal", errHistory)
	}

	// Cache: the visible-obligations projection omits the private version
	// without a trace for the other agent and shows it to its owner.
	visible := func(viewer domain.Principal) []ObligationView {
		var out []ObligationView
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			out, _ = f.s.VisibleObligations(tx, viewer, "task")
			return nil
		})
		return out
	}
	for _, v := range visible(other) {
		if v.Target == privRef || v.SourceItemID == "p-b-tests" {
			t.Errorf("another agent's visible obligations include the private version: %+v", v)
		}
	}
	var seen bool
	for _, v := range visible(b) {
		if v.Target == privRef {
			seen = true
			if v.Status != domain.ObligationSatisfied || v.Pending {
				t.Errorf("owner's projection of the private version = %+v", v)
			}
		}
	}
	if !seen {
		t.Error("the owning agent cannot see its own satisfied obligation")
	}

	// Receipt: another principal reusing the satisfying report's request
	// identity is refused with the fixed ErrEventIDConflict every identity
	// collision gets — never b's frozen result; a fresh identity is refused
	// like any request naming a run it cannot see. Neither refusal names a
	// private record. The probes write nothing; the transaction is rolled
	// back on purpose.
	var errSame, errFresh error
	if err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
		_, errSame = f.s.ReportObservationTx(tx, other, in, tx.NextSeq())
		fresh := in
		fresh.RequestID = "obs-b-pass-other"
		_, errFresh = f.s.ReportObservationTx(tx, other, fresh, tx.NextSeq())
		return errProbeRollback
	}); !errors.Is(err, errProbeRollback) {
		t.Fatal(err)
	}
	if !errors.Is(errSame, domain.ErrEventIDConflict) {
		t.Errorf("receipt replay by another principal = %v, want the uniform ErrEventIDConflict", errSame)
	}
	if !errors.Is(errFresh, domain.ErrNotFound) {
		t.Errorf("fresh request by another principal = %v, want ErrNotFound", errFresh)
	}
	for _, e := range []error{errSame, errFresh} {
		for _, id := range []string{run.ID, ev.ID, obsID, "obs-b-pass"} {
			if e != nil && strings.Contains(e.Error(), id) {
				t.Errorf("refusal %q names the private record %s", e, id)
			}
		}
	}

	// The owner's own exact replay returns the frozen receipt result.
	var replayed domain.MutationResult
	mustUpdate(t, f.st, func(tx store.Tx) error {
		var err error
		replayed, err = f.s.ReportObservationTx(tx, b, in, tx.NextSeq())
		return err
	})
	if replayed.Records == nil || len(replayed.Records.IDs) != 1 || replayed.Records.IDs[0] != obsID {
		t.Errorf("owner's exact replay = %+v, want the frozen result %s", replayed, obsID)
	}

	// No probe disturbed the private satisfaction.
	after := f.status(t, privRef)
	if after.Revision != sat.Revision || after.CurrentProofID != sat.CurrentProofID {
		t.Errorf("private version changed under the leak probes:\nbefore %+v\nafter  %+v", sat, after)
	}
	if h := f.history(t, privRef); len(h) != 1 || h[0].ProofID != sat.CurrentProofID {
		t.Errorf("private version's transitions = %+v", h)
	}
	// Positive control: the owner still sees the full current view.
	v, err := f.satisfiesOf(t, b, privRef, true)
	if err != nil || len(v.Relations) != 1 || !v.Relations[0].Current || v.Relations[0].Evidence.ItemID != ev.ID {
		t.Errorf("owner's current view = %+v (%v)", v, err)
	}
}
