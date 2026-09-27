package obligation

import (
	"fmt"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

func (f fixture) lastSeq(t *testing.T) uint64 {
	var seq uint64
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		seq = tx.LastSeq()
		return nil
	})
	return seq
}

// TestTraceT06 is the obligation-state portion of T06: every lifecycle path
// applies the same authorization, atomically. Goal resolution and
// CompleteTask itself are W3's; completion feasibility is checked here.
func TestTraceT06(t *testing.T) {
	f := newEvalFixture(t)
	o := f.sysTests
	before := f.lastSeq(t)
	// Step 2: USER attempts Block(O) and Waive(O).
	for _, to := range []domain.ObligationStatus{domain.ObligationBlocked, domain.ObligationWaived} {
		if _, err := f.s.transition(t, f.st, f.userP, intent(o, 1, to)); err == nil {
			t.Fatalf("USER %s succeeded", to)
		}
	}
	// Step 3: HARNESS asserts O SATISFIED without a SYSTEM-issued grant.
	if _, err := f.s.transition(t, f.st, f.harness, intent(o, 1, domain.ObligationSatisfied)); err == nil {
		t.Fatal("HARNESS assertion without grant succeeded")
	}
	if got := f.status(t, o); got.Status != domain.ObligationUnresolved || got.Revision != 1 || f.lastSeq(t) != before {
		t.Fatalf("denied attempts left traces: %+v seq %d -> %d", got, before, f.lastSeq(t))
	}
	// Unpinning a source does not make its obligation finished.
	var src domain.ContextItem
	mustUpdate(t, f.st, func(tx store.Tx) error {
		it, err := tx.Item("pu")
		if err != nil {
			return err
		}
		durable, high := domain.GenerationDurable, domain.RetentionHigh
		ev := domain.LifecycleEvent{ID: "unpin-pu", SessionID: testSession, Seq: tx.NextSeq(), TargetKind: domain.TargetItem, TargetID: it.ID, Action: "unpin", Actor: f.userP}
		src, err = tx.UpdateItem(it.ID, it.Version, domain.ItemChange{Generation: &durable, Retention: &high}, ev)
		return err
	})
	if src.Generation != domain.GenerationDurable {
		t.Fatalf("unpin = %+v", src)
	}
	if u, err := f.unfinished(t, f.s); err != nil || !u {
		t.Fatalf("unpinned source completed its obligation: %v %v", u, err)
	}
	// Step 4: SYSTEM authorizes tests_pass/1 for O; trusted evidence satisfies.
	f.matcherGrant(t, "g-sys", o, TestsPassV1, f.system)
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	if got := f.status(t, o); got.Status != domain.ObligationSatisfied {
		t.Fatalf("granted matcher did not satisfy: %+v", got)
	}
	// The USER obligation (unpinned, unresolved) still blocks completion.
	if u, _ := f.unfinished(t, f.s); !u {
		t.Error("completion feasible with an unresolved obligation")
	}
}

// TestTraceT07 is T07's semantic state: proofs expire when their subject
// changes, before the next plan and without another run.
func TestTraceT07(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	// Step 1: complete declared suite passes at W1.
	_, test1 := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	proof1 := f.status(t, f.sysTests).CurrentProofID
	// Step 2: the harness reports a source edit producing W2.
	f.r.set(t, f.fixture, hashOf("W2"), false)
	if got := f.status(t, f.sysTests); got.Status != domain.ObligationUnresolved {
		t.Fatalf("tests not UNRESOLVED before the next plan: %+v", got)
	}
	hist, _ := f.satisfies(t, f.system, false)
	if len(hist.Relations) != 1 || hist.Relations[0].ProofID != proof1 || hist.Relations[0].Current {
		t.Errorf("TEST1 lost its historical satisfaction: %+v", hist)
	}
	if st, _ := f.subject(t, f.target); st.ObservationID != test1.ID || st.Applicability != domain.ApplicabilityStale {
		t.Errorf("TEST1 state presented as current: %+v", st)
	}
	// Repeats: the same command elsewhere or with incomplete coverage.
	for _, mod := range []func(*domain.TestsTarget){
		func(v *domain.TestsTarget) { v.WorkingDir = "svc" },
		func(v *domain.TestsTarget) { v.CoverageSpec = "subset" },
	} {
		f.observeTests(t, testsTarget(mod), domain.OutcomePass, hashOf("W2"), nil)
	}
	if got := f.status(t, f.sysTests); got.Status != domain.ObligationUnresolved {
		t.Fatalf("unrelated evidence satisfied: %+v", got)
	}
	if st, _ := f.subject(t, f.target); st.ObservationID != test1.ID {
		t.Errorf("unrelated evidence superseded the subject: %+v", st)
	}
	// Step 3: the suite passes at W2.
	_, test2 := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W2"), nil)
	got := f.status(t, f.sysTests)
	cur, _ := f.satisfies(t, f.system, true)
	if got.Status != domain.ObligationSatisfied || len(cur.Relations) != 1 || cur.Relations[0].ProofID == proof1 {
		t.Errorf("TEST2 = %+v %+v", got, cur)
	}
	if st, _ := f.subject(t, f.target); st.ObservationID != test2.ID || st.Applicability != domain.ApplicabilityCurrent {
		t.Errorf("state after TEST2 = %+v", st)
	}
}

// TestConcurrentINV16 interleaves resource edits and observations from
// concurrent writers and checks INV-16 after every commit: a current
// SATISFIED matcher proof always matches the current KNOWN fingerprint.
func TestConcurrentINV16(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	fps := []string{hashOf("W1"), hashOf("W2"), hashOf("W3")}
	var satisfied int
	var smu sync.Mutex
	check := func() {
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			o, _ := r.ExactObligation(f.sysTests)
			if o.Status != domain.ObligationSatisfied {
				return nil
			}
			p, _ := r.ApplicabilityProof(o.CurrentProofID)
			rs, _ := r.ResourceState("repo1")
			if rs.Freshness != domain.ResourceKnown || p.Fingerprint != rs.WorkspaceFingerprint {
				t.Errorf("INV-16 violated: proof at %s, resource %+v", p.Fingerprint, rs)
			}
			return nil
		})
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	next := func() int { mu.Lock(); defer mu.Unlock(); n++; return n }
	for w := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 8 {
				id := next()
				_ = f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
					sem, err := store.Semantic(tx)
					if err != nil {
						return err
					}
					rs, err := sem.ResourceState("repo1")
					if err != nil {
						return err
					}
					if w%2 == 0 {
						in := domain.ReportResourceChangeIntent{RequestID: fmt.Sprintf("c-%d", id), ResourceID: "repo1", ExpectedRevision: rs.Revision,
							ExpectedAuthoritativeRevision: rs.AuthoritativeRevision, ResultingAuthoritativeRevision: rs.AuthoritativeRevision + 1,
							WorkspaceFingerprint: fps[(i+w)%3], AllPaths: true}
						_, err = f.s.ReportResourceChangeTx(tx, f.harness, in, tx.NextSeq())
						return err
					}
					seq := tx.NextSeq()
					run, err := f.s.registerRun(tx, sem, f.harness, runIntent(fmt.Sprintf("cr-%d", id), fmt.Sprintf("ce-%d", id), f.target), seq)
					if err != nil {
						return err
					}
					fp := fps[i%3] // often stale
					if i%2 == 0 {
						fp = rs.WorkspaceFingerprint // current when it commits
					}
					ev := evidenceItem(tx.NextSeq(), run)
					if err := tx.InsertItem(ev); err != nil {
						return err
					}
					if _, err = f.s.reportObservation(tx, sem, f.harness, obsIntent(fmt.Sprintf("co-%d", id), run, ev.ID, domain.OutcomePass, fp), tx.NextSeq()); err != nil {
						return err
					}
					if o, err := sem.ExactObligation(f.sysTests); err == nil && o.Status == domain.ObligationSatisfied {
						smu.Lock()
						satisfied++
						smu.Unlock()
					}
					return nil
				})
				check()
			}
		}()
	}
	wg.Wait()
	if satisfied == 0 {
		t.Error("no interleaving produced a satisfaction; the property was not exercised")
	}
}

// TestTraceT02Obligation is T02's repeat with a satisfied obligation attached
// to the old pin: replacement retires the old version with its status and
// proof intact; the new version starts UNRESOLVED with no inherited grant or
// proof and is satisfied only through its own grant and explicit revalidation.
func TestTraceT02Obligation(t *testing.T) {
	f := newFixture(t)
	var r repo1
	r.set(t, f, hashOf("W1"), true)
	// A SYSTEM task binding so replacements of a SYSTEM pin stay bound.
	if _, err := f.s.bindWS(t, f.st, f.system, bindIntent("ws-sys", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"})); err != nil {
		t.Fatal(err)
	}
	pin := func(id string) *domain.ObligationRef {
		var ref *domain.ObligationRef
		mustUpdate(t, f.st, func(tx store.Tx) error {
			it := seedItemTx(tx, id, "t2", domain.AuthoritySystem, "All tests must pass.")
			it.Namespace = domain.NamespaceDirective
			if err := tx.InsertItem(it); err != nil {
				return err
			}
			if _, err := graph.ReplaceDirective(tx, f.system, "task", "t2", it.ID, "evt-"+id); err != nil {
				return err
			}
			var err error
			ref, err = f.s.DeclarePinnedTx(tx, f.system, it.ID, "", tx.NextSeq())
			return err
		})
		return ref
	}
	v1 := pin("P1")
	ef := &evalFixture{fixture: f, target: testsTarget(nil), sysTests: *v1, r: r}
	ef.matcherGrant(t, "g-v1", *v1, TestsPassV1, f.system)
	ef.observeTests(t, ef.target, domain.OutcomePass, hashOf("W1"), nil)
	old := f.status(t, *v1)
	if old.Status != domain.ObligationSatisfied {
		t.Fatalf("v1 = %+v", old)
	}

	v2 := pin("P2")
	if v2 == nil || v2.ObligationID != v1.ObligationID || v2.Version != 2 {
		t.Fatalf("replacement = %v", v2)
	}
	retired := f.status(t, *v1)
	if retired.Current || retired.Status != domain.ObligationSatisfied || retired.CurrentProofID != old.CurrentProofID {
		t.Errorf("retired v1 lost its audit state: %+v", retired)
	}
	cur := f.status(t, *v2)
	if !cur.Current || cur.Status != domain.ObligationUnresolved || cur.CurrentProofID != "" {
		t.Fatalf("v2 = %+v", cur)
	}
	// The v1 grant never carries over; nor does evidence alone.
	ef.sysTests = *v2
	if _, err := ef.reevaluate(t, f.harness, *v2, cur.Revision); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, *v2); got.Status != domain.ObligationUnresolved {
		t.Fatalf("v2 satisfied under the v1 grant: %+v", got)
	}
	ef.matcherGrant(t, "g-v2", *v2, TestsPassV1, f.system)
	if _, err := ef.reevaluate(t, f.harness, *v2, cur.Revision); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, *v2); got.Status != domain.ObligationSatisfied || got.CurrentProofID == old.CurrentProofID {
		t.Errorf("revalidated v2 = %+v", got)
	}
	// Retired versions never transition again.
	if _, err := f.s.transition(t, f.st, f.system, intent(*v1, retired.Revision, domain.ObligationWaived)); err == nil {
		t.Error("retired version waived")
	}
}

// TestTraceT07PublicAPI drives T07 only through the public *Tx entry points
// W7 wires: register the resource, resync a baseline, bind the workspace,
// declare, grant, register and report runs, report the edit.
func TestTraceT07PublicAPI(t *testing.T) {
	s := newTestService(t)
	st := newTestStore(t)
	harness, system := actorOf(domain.AuthorityHarness), actorOf(domain.AuthoritySystem)
	reporter := sessionReporter()
	seedTask(t, st, "task")
	do := func(fn func(tx store.Tx, seq uint64) (domain.MutationResult, error)) domain.MutationResult {
		t.Helper()
		var res domain.MutationResult
		mustUpdate(t, st, func(tx store.Tx) error {
			var err error
			res, err = fn(tx, tx.NextSeq())
			return err
		})
		return res
	}
	session := domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: testSession}
	do(func(tx store.Tx, seq uint64) (domain.MutationResult, error) {
		return s.RegisterResourceTx(tx, reporter, domain.RegisterResourceIntent{RequestID: "reg", ResourceID: "repo1", Reporter: reporter, Access: session}, seq)
	})
	report := func(req string, expRev, auth uint64, fp string, resync bool) {
		do(func(tx store.Tx, seq uint64) (domain.MutationResult, error) {
			return s.ReportResourceChangeTx(tx, reporter, domain.ReportResourceChangeIntent{RequestID: req, ResourceID: "repo1", ExpectedRevision: expRev,
				ExpectedAuthoritativeRevision: auth, ResultingAuthoritativeRevision: auth + 1, WorkspaceFingerprint: fp, Resynchronization: resync, AllPaths: !resync}, seq)
		})
	}
	report("baseline", 0, 0, hashOf("W1"), true)
	do(func(tx store.Tx, seq uint64) (domain.MutationResult, error) {
		return s.BindWorkspaceTx(tx, system, bindIntent("ws1", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"}), seq)
	})
	var ref *domain.ObligationRef
	mustUpdate(t, st, func(tx store.Tx) error {
		it := seedItemTx(tx, "p", "tests", domain.AuthoritySystem, "All tests must pass.")
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := tx.SetCurrentVersion(it.ID); err != nil {
			return err
		}
		var err error
		ref, err = s.DeclarePinnedTx(tx, system, it.ID, "", tx.NextSeq())
		return err
	})
	mustUpdate(t, st, func(tx store.Tx) error {
		m := TestsPassV1
		return tx.InsertGrant(domain.MutationGrant{ID: "g", SessionID: testSession, Action: domain.ActionAssertObligation,
			Targets: []domain.GrantTarget{ref.Target()}, Issuer: system, Matcher: &m, IssuedSeq: tx.NextSeq()})
	})
	run := func(n, fp string) {
		res := do(func(tx store.Tx, seq uint64) (domain.MutationResult, error) {
			return s.RegisterRunTx(tx, harness, runIntent("run-"+n, "exec-"+n, testsTarget(nil)), seq)
		})
		var r domain.ObservationRun
		_ = st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			sem, _ := store.ReadSemantic(tx)
			r, _ = sem.ObservationRun(res.Records.IDs[0])
			return nil
		})
		do(func(tx store.Tx, seq uint64) (domain.MutationResult, error) {
			ev := evidenceItem(tx.NextSeq(), r)
			if err := tx.InsertItem(ev); err != nil {
				return domain.MutationResult{}, err
			}
			return s.ReportObservationTx(tx, harness, obsIntent("obs-"+n, r, ev.ID, domain.OutcomePass, fp), seq)
		})
	}
	status := func() domain.ObligationStatus {
		var o domain.ObligationVersion
		_ = st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			sem, _ := store.ReadSemantic(tx)
			o, _ = sem.ExactObligation(*ref)
			return nil
		})
		return o.Status
	}
	run("1", hashOf("W1"))
	if got := status(); got != domain.ObligationSatisfied {
		t.Fatalf("TEST1 at W1: %s", got)
	}
	report("edit", 1, 1, hashOf("W2"), false)
	if got := status(); got != domain.ObligationUnresolved {
		t.Fatalf("after W2 report: %s", got)
	}
	run("2", hashOf("W2"))
	if got := status(); got != domain.ObligationSatisfied {
		t.Errorf("TEST2 at W2: %s", got)
	}
}
