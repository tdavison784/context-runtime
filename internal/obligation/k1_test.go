package obligation

import (
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// effective reads ref's effective status through the K1 helper.
func (f fixture) effective(t *testing.T, ref domain.ObligationRef) (domain.ObligationStatus, bool) {
	t.Helper()
	o := f.status(t, ref)
	var st domain.ObligationStatus
	var pending bool
	var err error
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		st, pending, err = EffectiveStatus(r, o)
		return nil
	})
	if err != nil {
		t.Fatalf("EffectiveStatus(%s): %v", ref.ObligationID, err)
	}
	return st, pending
}

// repo1Update is the ID of the repo1 update last written by f.r.set.
func (f *evalFixture) repo1Update() string {
	return recordID("ru_", "resource-update", "repo1", fmt.Sprintf("r1-%d", f.r.n))
}

// K1a (SEC-4.1 = SPEC-4.1, SEC-4.3): a report never fans out and is never
// refused for dependent volume. More resource-bound proofs than any budget or
// cap would allow all exist; every report kind reads no proof; each proof is
// effectively UNRESOLVED, pending settlement, the moment its dependency
// changes, while its stored status is untouched.
func TestK1ReportsNeverFanOut(t *testing.T) {
	f := newResourceFixture(t)
	refs := []domain.ObligationRef{f.user, f.sysTests}
	for i := range 10 {
		src := seedPinned(t, f.st, fmt.Sprintf("k1src%d", i), fmt.Sprintf("k1dir%d", i), domain.AuthorityUser, "Keep the suite green.")
		refs = append(refs, f.repo2Obligation(t, src.ID, f.harness))
	}
	for _, ref := range refs {
		f.assertBound(t, ref, f.system)
	}
	f.st.counting.Store(true)
	reads := func(step string) {
		t.Helper()
		if n := f.st.wholeReads.Load() + f.st.workspaceReads.Load() + f.st.pathReads.Load(); n != 0 {
			t.Errorf("%s: report read %d proof pages", step, n)
		}
	}
	f.edit(t, hashOf("W2"))
	reads("fingerprint change")
	for _, ref := range refs {
		if st, pending := f.effective(t, ref); st != domain.ObligationUnresolved || !pending {
			t.Errorf("after W2: %s effective %s pending=%v", ref.ObligationID, st, pending)
		}
		if o := f.status(t, ref); o.Status != domain.ObligationSatisfied {
			t.Errorf("after W2: %s stored %s, want SATISFIED until settled", ref.ObligationID, o.Status)
		}
	}
	if _, err := f.send(t, domain.ReportResourceChangeIntent{ExpectedRevision: f.rev, ExpectedAuthoritativeRevision: f.auth, ResultingAuthoritativeRevision: f.auth + 1, WorkspaceFingerprint: hashOf("W3"), AllPaths: true}); err != nil {
		t.Fatalf("ALL-paths report: %v", err)
	}
	reads("ALL paths")
	f.resync(t, f.auth+1, hashOf("W4"))
	reads("resync")
}

// K1 A1: validity is monotone. A W1 proof invalidated by W2 stays invalid
// after the workspace reverts to W1.
func TestK1ValidityIsMonotone(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	f.r.set(t, f.fixture, hashOf("W2"), false)
	f.r.set(t, f.fixture, hashOf("W1"), false)
	if st, _ := f.effective(t, f.sysTests); st != domain.ObligationUnresolved {
		t.Errorf("W1 proof after W1->W2->W1 is effectively %s", st)
	}
}

// K1 A1: today's per-dependency semantics. A same-fingerprint report keeps
// a WORKSPACE proof; a same-content path report and an unrelated path keep a
// CURRENT_PATH proof, a containing-directory change does not; FIXED_CONTENT
// is never invalidated.
func TestK1DependencySemantics(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	path := f.fileObligation(t, "50")
	f.resourceReport(t, "W1", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	if err := f.assertPath(t, path, f.r.auth, "H1"); err != nil {
		t.Fatal(err)
	}
	fixedTarget := fileTarget("repo1", "docs/a.md", domain.FileFixedHash, hashOf("H1"))
	decl := domain.DeclareObligationIntent{RequestID: "d-fixed", SourceItemID: "pu", DeclarationSlot: "51", Description: "read v1",
		ExpectedSourceVersion: 1, Target: &fixedTarget, Matcher: &FileReadV1}
	if _, err := f.s.declare(t, f.st, f.harness, decl); err != nil {
		t.Fatal(err)
	}
	key, _ := f.item(t, "pu").CurrentKey()
	fixed := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, 51), Version: 1}
	f.matcherGrant(t, "g-fixed", fixed, FileReadV1, f.userP)
	runN++
	run, err := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), fmt.Sprintf("exec-%d", runN), fixedTarget))
	if err != nil {
		t.Fatal(err)
	}
	f.report(t, run, domain.OutcomePass, hashOf("H1"), nil)
	want := func(step string, tests, cur, fix domain.ObligationStatus) {
		t.Helper()
		for _, c := range []struct {
			name string
			ref  domain.ObligationRef
			want domain.ObligationStatus
		}{{"tests", f.sysTests, tests}, {"current path", path, cur}, {"fixed", fixed, fix}} {
			if st, _ := f.effective(t, c.ref); st != c.want {
				t.Errorf("%s: %s effective %s, want %s", step, c.name, st, c.want)
			}
		}
	}
	sat, unres := domain.ObligationSatisfied, domain.ObligationUnresolved
	want("setup", sat, sat, sat)
	f.resourceReport(t, "W1", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	want("same content, same fingerprint", sat, sat, sat)
	f.resourceReport(t, "W1", false, false, []string{"other/z.md"})
	want("unrelated path", sat, sat, sat)
	f.resourceReport(t, "W1", false, false, []string{"docs"})
	want("containing directory", sat, unres, sat)
	f.resourceReport(t, "W5", false, true, nil)
	want("ALL paths, new fingerprint", unres, unres, sat)
}

// K1 A3: before any transition on a version whose effective status differs
// from its stored one, the same transaction records the restricted
// RESOURCE_INVALIDATION settlement, caused by the EARLIEST affecting update,
// as the session's SYSTEM runtime actor, with the original authorization;
// the requested transition then starts from the effective state.
func TestK1InlineSettleBeforeTransition(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	satisfiedBy := f.history(t, f.sysTests)
	origin := satisfiedBy[len(satisfiedBy)-1]
	f.r.set(t, f.fixture, hashOf("W2"), false)
	earliest := f.repo1Update()
	f.r.set(t, f.fixture, hashOf("W3"), false)
	f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W3"), nil)
	h := f.history(t, f.sysTests)
	if len(h) < 3 {
		t.Fatalf("history = %+v", h)
	}
	settle, next := h[len(h)-2], h[len(h)-1]
	runtime := domain.Principal{SessionID: testSession, Authority: domain.AuthoritySystem}
	if settle.Cause != domain.CauseResourceInvalidation || settle.CauseRecordID != earliest || settle.Actor != runtime ||
		settle.From != domain.ObligationSatisfied || settle.To != domain.ObligationUnresolved || settle.GrantID != "" ||
		settle.OriginAuthorizationRef == nil || settle.OriginAuthorizationRef.TransitionID != origin.ID {
		t.Errorf("settlement = %+v", settle)
	}
	if next.Cause != domain.CauseMatcher || next.From != domain.ObligationUnresolved || next.To != domain.ObligationSatisfied {
		t.Errorf("transition after settlement = %+v, want a fresh matcher satisfaction", next)
	}
	// An explicit transition starts from the effective state too: a pending
	// version is blocked from UNRESOLVED after settlement.
	f.r.set(t, f.fixture, hashOf("W4"), false)
	o := f.status(t, f.sysTests)
	if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, o.Revision, domain.ObligationBlocked)); err != nil {
		t.Fatalf("block a pending version: %v", err)
	}
	h = f.history(t, f.sysTests)
	if settle, block := h[len(h)-2], h[len(h)-1]; settle.Cause != domain.CauseResourceInvalidation || block.From != domain.ObligationUnresolved || block.To != domain.ObligationBlocked {
		t.Errorf("settle then block = %+v, %+v", settle, block)
	}
}

// K1 A7/A2: reads report the effective status; pending settlement is a
// fixed flag in access-filtered views; completion counts the pending
// version as unfinished; the current SATISFIES view drops its proof.
func TestK1ReadsUseEffectiveStatus(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	satisfyOthers := func() {
		for _, ref := range []domain.ObligationRef{f.user, f.sys} {
			o := f.status(t, ref)
			if o.Status != domain.ObligationSatisfied {
				if _, err := f.s.transition(t, f.st, f.system, intent(ref, o.Revision, domain.ObligationSatisfied)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	satisfyOthers()
	unfinished := func() bool {
		var u bool
		var err error
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			u, err = f.s.UnfinishedTaskObligations(tx, "task")
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	if unfinished() {
		t.Fatalf("setup: task has unfinished obligations")
	}
	f.r.set(t, f.fixture, hashOf("W2"), false)
	if !unfinished() {
		t.Errorf("a pending (effectively UNRESOLVED) version does not block completion")
	}
	var views []ObligationView
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		views, _ = f.s.VisibleObligations(tx, f.harness, "task")
		return nil
	})
	found := false
	for _, v := range views {
		if v.Target == f.sysTests {
			found = true
			if v.Status != domain.ObligationUnresolved || !v.Pending {
				t.Errorf("view = %+v, want effective UNRESOLVED, pending", v)
			}
		}
	}
	if !found {
		t.Fatalf("tests obligation not visible")
	}
	if cur, err := f.satisfies(t, f.system, true); err != nil || len(cur.Relations) != 0 {
		t.Errorf("current SATISFIES of a pending version = %+v, %v", cur, err)
	}
}
