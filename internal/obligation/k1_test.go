package obligation

import (
	"errors"
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

// DUR-4.2 (superseded by K1): resource-bound proofs that also claim
// WORKSPACE on other resources never wedge a report. Resync, edit and gap
// reports on the shared resource all succeed and every obligation derives
// UNRESOLVED.
func TestK1MultiResourceProofsNeverWedgeReports_DUR42(t *testing.T) {
	f := newResourceFixture(t)
	others := []string{"repo3", "repo4", "repo5", "repo6", "repo7", "repo8", "repo9"}
	for i, res := range others {
		seedResource(t, f.st, res, f.reporter)
		in := domain.ReportResourceChangeIntent{RequestID: fmt.Sprintf("init-%s", res), ResourceID: res, ResultingAuthoritativeRevision: 1,
			WorkspaceFingerprint: hashOf(fmt.Sprintf("O%d", i)), Resynchronization: true}
		if _, err := f.s.report(t, f.st, f.reporter, in); err != nil {
			t.Fatal(err)
		}
	}
	refs := []domain.ObligationRef{f.user, f.sysTests}
	for i := range 6 {
		src := seedPinned(t, f.st, fmt.Sprintf("d42src%d", i), fmt.Sprintf("d42dir%d", i), domain.AuthorityUser, "Keep the suite green.")
		refs = append(refs, f.repo2Obligation(t, src.ID, f.harness))
	}
	for _, ref := range refs {
		o := f.status(t, ref)
		rs := f.state(t)
		in := intent(ref, o.Revision, domain.ObligationSatisfied)
		in.AssertionMode = domain.AssertionResourceBound
		in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo2", ResourceRevision: rs.AuthoritativeRevision, Fingerprint: rs.WorkspaceFingerprint}}
		for i, res := range others {
			in.Resources = append(in.Resources, domain.ResourceClaim{Kind: domain.DependencyWorkspace, ResourceID: res, ResourceRevision: 1, Fingerprint: hashOf(fmt.Sprintf("O%d", i))})
		}
		if _, err := f.s.transition(t, f.st, f.system, in); err != nil {
			t.Fatalf("multi-resource assertion %s: %v", ref.ObligationID, err)
		}
	}
	f.resync(t, f.auth+1, hashOf("W-resync"))
	f.edit(t, hashOf("W-edit"))
	gap := domain.ReportResourceChangeIntent{ExpectedRevision: f.rev, ExpectedAuthoritativeRevision: f.auth, ResultingAuthoritativeRevision: f.auth + 2, WorkspaceFingerprint: hashOf("W-gap")}
	if _, err := f.send(t, gap); err != nil {
		t.Fatalf("gap report: %v", err)
	}
	for _, ref := range refs {
		if st, _ := f.effective(t, ref); st != domain.ObligationUnresolved {
			t.Errorf("%s effective %s after its workspace moved", ref.ObligationID, st)
		}
	}
}

// DUR-4.3 (superseded by K1): live proofs on files that never change never
// block a new satisfaction on the same resource, and nothing is refused
// silently.
func TestK1StableLiveProofsNeverBlockSatisfaction_DUR43(t *testing.T) {
	f := newEvalFixture(t)
	f.resourceReport(t, "W1", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
	rev := f.r.auth
	for i := range 8 {
		// One declaration per source keeps within the per-source limit.
		src := seedPinned(t, f.st, fmt.Sprintf("d43src%d", i), fmt.Sprintf("d43dir%d", i), domain.AuthorityUser, "Read the doc.")
		target := fileTarget("repo1", "docs/a.md", domain.FileCurrentContent, "")
		in := domain.DeclareObligationIntent{RequestID: fmt.Sprintf("d43-%d", i), SourceItemID: src.ID, DeclarationSlot: "1", Description: "read it",
			ExpectedSourceVersion: 1, Target: &target, Matcher: &FileReadV1}
		if _, err := f.s.declare(t, f.st, f.harness, in); err != nil {
			t.Fatal(err)
		}
		key, _ := f.item(t, src.ID).CurrentKey()
		ref := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, 1), Version: 1}
		if err := f.assertPath(t, ref, rev, "H1"); err != nil {
			t.Fatalf("path assertion %d: %v", i, err)
		}
	}
	for i := range 5 {
		f.resourceReport(t, "W1", false, false, []string{fmt.Sprintf("other/%d.md", i)})
	}
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	if st, _ := f.effective(t, f.sysTests); st != domain.ObligationSatisfied {
		t.Errorf("granted PASS with 8 live path proofs on the resource: effective %s", st)
	}
}

// settle runs one worker pass as SYSTEM.
func (f fixture) settle(t *testing.T, max int) (int, bool) {
	t.Helper()
	var n int
	var more bool
	err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
		var err error
		n, more, err = f.s.SettlePendingTx(tx, f.system, max)
		return err
	})
	if err != nil {
		t.Fatalf("settlement pass: %v", err)
	}
	return n, more
}

// K1 A4: the asynchronous worker records the same exact-keyed settlement
// as the inline path, in bounded resumable passes, as the session SYSTEM
// runtime actor with the earliest affecting update as cause; it is
// idempotent, skips re-satisfied versions and is SYSTEM-only.
func TestK1SettlementWorker(t *testing.T) {
	f := newResourceFixture(t)
	refs := []domain.ObligationRef{f.user, f.sysTests}
	for i := range 3 {
		src := seedPinned(t, f.st, fmt.Sprintf("k1w%d", i), fmt.Sprintf("k1wdir%d", i), domain.AuthorityUser, "Keep the suite green.")
		refs = append(refs, f.repo2Obligation(t, src.ID, f.harness))
	}
	for _, ref := range refs {
		f.assertBound(t, ref, f.system)
	}
	f.edit(t, hashOf("W2"))
	earliest := recordID("ru_", "resource-update", "repo2", fmt.Sprintf("rep-%d", f.n)) // send names requests rep-<n>
	f.edit(t, hashOf("W3"))
	err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
		_, _, err := f.s.SettlePendingTx(tx, f.harness, 10)
		return err
	})
	if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Errorf("HARNESS worker pass: %v, want ErrInvalidAuthorityPromotion", err)
	}
	total, passes := 0, 0
	for more := true; more; passes++ {
		if passes > 10 {
			t.Fatal("worker never finishes")
		}
		var n int
		n, more = f.settle(t, 2)
		if n > 2 {
			t.Errorf("pass settled %d, bound 2", n)
		}
		total += n
	}
	if total != len(refs) {
		t.Errorf("worker settled %d, want %d", total, len(refs))
	}
	runtime := domain.Principal{SessionID: testSession, Authority: domain.AuthoritySystem}
	for _, ref := range refs {
		if o := f.status(t, ref); o.Status != domain.ObligationUnresolved {
			t.Errorf("%s stored %s after settlement", ref.ObligationID, o.Status)
		}
		if _, pending := f.effective(t, ref); pending {
			t.Errorf("%s still pending after settlement", ref.ObligationID)
		}
		h := (&evalFixture{fixture: f.fixture}).history(t, ref)
		last := h[len(h)-1]
		if last.Cause != domain.CauseResourceInvalidation || last.CauseRecordID != earliest || last.Actor != runtime || last.OriginAuthorizationRef == nil {
			t.Errorf("%s settlement = %+v", ref.ObligationID, last)
		}
	}
	// Idempotent, and a re-satisfied version is left alone.
	f.assertBound(t, refs[0], f.system)
	if n, _ := f.settle(t, 10); n != 0 {
		t.Errorf("second pass settled %d", n)
	}
	if o := f.status(t, refs[0]); o.Status != domain.ObligationSatisfied {
		t.Errorf("worker touched a re-satisfied version: %+v", o)
	}
}

// K1 A3 / ruling M2: before graph retires a version, SettleBeforeRetireTx
// records the same exact-keyed settlement for a pending one, and does
// nothing for a valid version or on a repeat call.
func TestK1SettleBeforeRetire(t *testing.T) {
	f := newEvalFixture(t)
	f.matcherGrant(t, "g", f.sysTests, TestsPassV1, f.system)
	f.report(t, f.newRun(t), domain.OutcomePass, hashOf("W1"), nil)
	call := func() {
		t.Helper()
		if err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
			return f.s.SettleBeforeRetireTx(tx, f.sysTests)
		}); err != nil {
			t.Fatalf("SettleBeforeRetireTx: %v", err)
		}
	}
	before := len(f.history(t, f.sysTests))
	call()
	if n := len(f.history(t, f.sysTests)); n != before {
		t.Errorf("valid version: %d new transitions", n-before)
	}
	f.r.set(t, f.fixture, hashOf("W2"), false)
	cause := f.repo1Update()
	before = len(f.history(t, f.sysTests))
	call()
	h := f.history(t, f.sysTests)
	if len(h) != before+1 {
		t.Fatalf("pending version: %d new transitions, want 1", len(h)-before)
	}
	runtime := domain.Principal{SessionID: testSession, Authority: domain.AuthoritySystem}
	if last := h[len(h)-1]; last.Cause != domain.CauseResourceInvalidation || last.CauseRecordID != cause || last.Actor != runtime {
		t.Errorf("settlement = %+v", last)
	}
	call()
	if n := len(f.history(t, f.sysTests)); n != len(h) {
		t.Errorf("repeat call wrote %d transitions", n-len(h))
	}
}
