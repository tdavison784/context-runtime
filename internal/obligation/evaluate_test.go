package obligation

import (
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

type evalFixture struct {
	fixture
	sysTests domain.ObligationRef // SYSTEM tests_pass bound through a SYSTEM workspace binding
	r        repo1
	target   domain.TargetSpec
}

func newEvalFixture(t *testing.T) *evalFixture {
	f := &evalFixture{fixture: newFixture(t), target: testsTarget(nil)}
	src := bindIntent("ws-sys", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceSource, ID: "p-sys-tests"})
	var ref *domain.ObligationRef
	mustUpdate(t, f.st, func(tx store.Tx) error {
		// Create the SYSTEM pin, bind its source workspace, then declare.
		it := seedItemTx(tx, "p-sys-tests", "sys-tests", domain.AuthoritySystem, "All tests must pass.")
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := tx.SetCurrentVersion(it.ID); err != nil {
			return err
		}
		if _, err := f.s.BindWorkspaceTx(tx, f.system, src, tx.NextSeq()); err != nil {
			return err
		}
		var err error
		ref, err = f.s.DeclarePinnedTx(tx, f.system, it.ID, "", tx.NextSeq())
		return err
	})
	if o := f.status(t, *ref); o.BindingState != domain.BindingBound || o.TargetSubjectKey != mustSubjectKey(f.target) {
		t.Fatalf("SYSTEM tests obligation = %+v", o)
	}
	f.sysTests = *ref
	f.r.set(t, f.fixture, hashOf("W1"), true)
	return f
}

// matcherGrant lets matcher m assert ref, issued by issuer.
func (f *evalFixture) matcherGrant(t *testing.T, id string, ref domain.ObligationRef, m domain.MatcherRef, issuer domain.Principal) {
	t.Helper()
	mustUpdate(t, f.st, func(tx store.Tx) error {
		return tx.InsertGrant(domain.MutationGrant{
			ID: id, SessionID: testSession, Action: domain.ActionAssertObligation,
			Targets: []domain.GrantTarget{ref.Target()}, Issuer: issuer, Matcher: &m, IssuedSeq: tx.NextSeq(),
		})
	})
}

func (f *evalFixture) revoke(t *testing.T, id string) {
	mustUpdate(t, f.st, func(tx store.Tx) error {
		_, err := tx.RevokeGrant(id, domain.LifecycleEvent{ID: "revoke-" + id, SessionID: testSession, Seq: tx.NextSeq(), TargetKind: domain.TargetGrant, TargetID: id, Action: "revoke", Actor: f.system})
		return err
	})
}

func (f *evalFixture) history(t *testing.T, ref domain.ObligationRef) []domain.ObligationTransition {
	var out []domain.ObligationTransition
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		all, _ := tx.ObligationTransitions(ref.ObligationID)
		for _, tr := range all {
			if tr.Version == ref.Version {
				out = append(out, tr)
			}
		}
		return nil
	})
	return out
}

func TestMatcherGrantT06(t *testing.T) {
	f := newEvalFixture(t)
	// Trusted evidence before any grant changes no status.
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("satisfied without a grant: %+v", o)
	}
	// A grant for another matcher version authorizes nothing.
	f.matcherGrant(t, "g-v2", f.sysTests, domain.MatcherRef{Name: "tests_pass", Version: "2"}, f.system)
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("satisfied under another matcher version: %+v", o)
	}
	// A lower-authority issuer cannot delegate for the SYSTEM source.
	f.matcherGrant(t, "g-user", f.sysTests, TestsPassV1, f.userP)
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("satisfied under a USER-issued grant: %+v", o)
	}
	// T06 step 4: SYSTEM authorizes tests_pass/1 for this exact version.
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	_, obs := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	o := f.status(t, f.sysTests)
	if o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" || len(o.EvidenceIDs) != 1 || o.EvidenceIDs[0] != f.evidence.ID {
		t.Fatalf("granted matcher = %+v", o)
	}
	h := f.history(t, f.sysTests)
	last := h[len(h)-1]
	if last.Cause != domain.CauseMatcher || last.GrantID != "g-sys" || last.Matcher == nil || *last.Matcher != TestsPassV1 || last.Actor != f.harness || last.ProofID != o.CurrentProofID {
		t.Errorf("matcher transition = %+v", last)
	}
	var p domain.ApplicabilityProof
	var cov domain.CoverageRecord
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		p, _ = r.ApplicabilityProof(o.CurrentProofID)
		cov, _ = r.Coverage(p.EvidenceCoverageID)
		return nil
	})
	if p.ObservationID != obs.ID || p.Fingerprint != hashOf("W1") || p.RuleVersion != "tests_pass/1" || cov.Purpose != domain.CoverageEvidenceSupport || cov.MemberCount != 1 {
		t.Errorf("proof = %+v coverage = %+v", p, cov)
	}
	// Tool text gained nothing: the USER obligation without a grant is untouched.
	if u := f.status(t, f.user); u.Status != domain.ObligationUnresolved {
		t.Errorf("ungranted obligation changed: %+v", u)
	}
}

func TestMatcherInvalidationAndReproofT07(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	first := f.status(t, f.sysTests).CurrentProofID
	// W2 reported before the next plan: UNRESOLVED without another run.
	f.r.set(t, f.fixture, hashOf("W2"), false)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Fatalf("W2 left satisfaction: %+v", o)
	}
	// Same command elsewhere or with partial coverage never satisfies.
	for name, mod := range map[string]func(*domain.TestsTarget){
		"other directory": func(v *domain.TestsTarget) { v.WorkingDir = "svc" },
		"subset":          func(v *domain.TestsTarget) { v.CoverageSpec = "subset-a" },
	} {
		f.observeTests(t, testsTarget(mod), domain.OutcomePass, hashOf("W2"), nil)
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
			t.Errorf("%s satisfied the full-suite obligation", name)
		}
	}
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W2"), func(in *domain.ObservationIntent) { in.Completeness = domain.ObservationPartial })
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Error("partial PASS satisfied")
	}
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W2"), nil)
	o := f.status(t, f.sysTests)
	if o.Status != domain.ObligationSatisfied || o.CurrentProofID == first {
		t.Errorf("TEST2 proof = %+v", o)
	}
}

func TestProofRejection(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	// Reversed arrival: run2 PASS lands first, then run1's FAIL.
	runN++
	run1, _ := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), "exec-old", f.target))
	runN++
	run2, _ := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), "exec-new", f.target))
	f.report(t, run2, domain.OutcomePass, hashOf("W1"), nil)
	f.report(t, run1, domain.OutcomeFail, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Fatalf("older FAIL rejected newer proof: %+v", o)
	}
	// Partial or timed-out failures do not reject a valid proof.
	f.observeTests(t, f.target, domain.OutcomeFail, hashOf("W1"), func(in *domain.ObservationIntent) { in.Completeness = domain.ObservationPartial })
	f.observeTests(t, f.target, domain.OutcomeTimeout, "", nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Fatalf("partial/timeout rejected proof: %+v", o)
	}
	proof := f.status(t, f.sysTests).CurrentProofID
	// PASS then a newer complete FAIL at the same fingerprint, even after the
	// grant was revoked: rejected through the restricted path.
	f.revoke(t, "g-sys")
	_, fail := f.observeTests(t, f.target, domain.OutcomeFail, hashOf("W1"), nil)
	o := f.status(t, f.sysTests)
	if o.Status != domain.ObligationUnresolved {
		t.Fatalf("newer FAIL kept satisfaction: %+v", o)
	}
	h := f.history(t, f.sysTests)
	last := h[len(h)-1]
	if last.Cause != domain.CauseProofRejected || last.GrantID != "" || last.CauseRecordID != fail.ID || last.PriorProofID != proof || last.OriginAuthorizationRef.GrantID != "g-sys" {
		t.Errorf("rejection = %+v", last)
	}
}

func TestProofRefresh(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	old := f.status(t, f.sysTests)
	_, obs2 := f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	o := f.status(t, f.sysTests)
	if o.Status != domain.ObligationSatisfied || o.CurrentProofID == old.CurrentProofID || o.Revision != old.Revision+2 {
		t.Fatalf("refresh = %+v (old %+v)", o, old)
	}
	h := f.history(t, f.sysTests)
	rel, sat := h[len(h)-2], h[len(h)-1]
	if rel.From != domain.ObligationSatisfied || rel.To != domain.ObligationUnresolved || rel.Cause != domain.CauseProofRefresh || rel.PriorProofID != old.CurrentProofID ||
		sat.Cause != domain.CauseProofRefresh || sat.ProofID != o.CurrentProofID || sat.RequestID != obs2.ID || rel.Seq >= sat.Seq {
		t.Errorf("refresh pair = %+v / %+v", rel, sat)
	}
	// Without a live grant the still-valid old proof stays.
	f.revoke(t, "g-sys")
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	if again := f.status(t, f.sysTests); again.CurrentProofID != o.CurrentProofID || again.Revision != o.Revision {
		t.Errorf("refresh without grant changed proof: %+v", again)
	}
}

func TestPrivateEvidenceNotPublished(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
	private := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task", AgentID: "agent"}
	ev := seedEvidence(t, f.st, "ev-agent", private)
	runN++
	in := runIntent(fmt.Sprintf("run-%d", runN), "exec-p", f.target)
	in.Access = private
	run, err := f.registerRun(t, f.harness, in)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.observe(t, f.harness, obsIntent("obs-private", run, ev.ID, domain.OutcomePass, hashOf("W1"))); err != nil {
		t.Fatal(err)
	}
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved {
		t.Errorf("agent-private PASS satisfied a task-wide obligation: %+v", o)
	}
	// Control: the same result reported at the task boundary satisfies.
	f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
	if o := f.status(t, f.sysTests); o.Status != domain.ObligationSatisfied {
		t.Errorf("task-wide control did not satisfy: %+v", o)
	}
}

func TestFileReadEndToEnd(t *testing.T) {
	f := newEvalFixture(t)
	declare := func(slot string, target domain.TargetSpec) domain.ObligationRef {
		t.Helper()
		in := domain.DeclareObligationIntent{RequestID: "d-file-" + slot, SourceItemID: "pu", DeclarationSlot: slot, Description: "read the doc",
			ExpectedSourceVersion: 1, Target: &target, Matcher: &FileReadV1}
		if _, err := f.s.declare(t, f.st, f.harness, in); err != nil {
			t.Fatal(err)
		}
		key, _ := f.item(t, "pu").CurrentKey()
		n, _ := harnessSlot(slot)
		ref := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, n), Version: 1}
		f.matcherGrant(t, "g-file-"+slot, ref, FileReadV1, f.userP)
		return ref
	}
	fixed := declare("2", fileTarget("repo1", "docs/a.md", domain.FileFixedHash, hashOf("v1")))
	current := declare("3", fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, ""))
	read := fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, "")
	readAs := func(content string) {
		runN++
		run, err := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), fmt.Sprintf("exec-%d", runN), read))
		if err != nil {
			t.Fatal(err)
		}
		f.report(t, run, domain.OutcomePass, hashOf(content), nil)
	}
	readAs("v0") // other content never satisfies a fixed snapshot
	if o := f.status(t, fixed); o.Status != domain.ObligationUnresolved {
		t.Fatalf("wrong content satisfied: %+v", o)
	}
	readAs("v1")
	o := f.status(t, fixed)
	if o.Status != domain.ObligationSatisfied {
		t.Fatalf("fixed read = %+v", o)
	}
	// A later edit of the path does not change the required snapshot.
	f.r.n++
	in := domain.ReportResourceChangeIntent{RequestID: "edit-doc", ResourceID: "repo1", ExpectedRevision: f.r.rev, ExpectedAuthoritativeRevision: f.r.auth,
		ResultingAuthoritativeRevision: f.r.auth + 1, WorkspaceFingerprint: hashOf("W2"), ChangedPaths: []string{"docs/a.md"}}
	if _, err := f.s.report(t, f.st, f.harness, in); err != nil {
		t.Fatal(err)
	}
	if got := f.status(t, fixed); got.Status != domain.ObligationSatisfied || got.CurrentProofID != o.CurrentProofID {
		t.Errorf("path edit invalidated a fixed-content proof: %+v", got)
	}
	// Without authoritative per-path content, current-content reads never
	// satisfy (fails closed until path content reporting lands).
	if got := f.status(t, current); got.Status != domain.ObligationUnresolved {
		t.Errorf("current-content read satisfied without path state: %+v", got)
	}
}
