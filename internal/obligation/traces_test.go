package obligation

import (
	"fmt"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
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
			smu.Lock()
			satisfied++
			smu.Unlock()
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
					_, err = f.s.reportObservation(tx, sem, f.harness, obsIntent(fmt.Sprintf("co-%d", id), run, f.evidence.ID, domain.OutcomePass, fps[i%3]), tx.NextSeq())
					return err
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
