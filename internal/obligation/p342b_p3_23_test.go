package obligation

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
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

// TestP3_23_ReportAliasesRefusedAndDirectoriesHitContainedProofs: a report's
// changed paths are canonical or refused, and a changed path that names no
// known file still hits every live proof it conservatively contains (P3-23 —
// the cited tests bound update history and unrelated edits, never aliases or
// unknown paths). Every alias spelling of docs/a.md — interior dot segment,
// doubled slash, leading dot, absolute form, parent hop, trailing slash, the
// empty path, and a duplicate entry — is one uniform invalid-record refusal
// that consumes no revision and leaves the live proof untouched. A changed
// DIRECTORY ("docs", which names no recorded file content) then invalidates
// the CURRENT_PATH proof of docs/a.md conservatively: containment, not
// equality, is the rule.
func TestP3_23_ReportAliasesRefusedAndDirectoriesHitContainedProofs(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		ref := f.fileObligation(t, "23")
		f.matcherGrant(t, "g-23pa", ref, FileReadV1, f.userP)
		f.resourceReport(t, "W-a", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
		if err := f.assertPath(t, ref, f.r.auth, "H1"); err != nil {
			t.Fatalf("live current-path proof not established: %v", err)
		}
		if st, _ := f.effective(t, ref); st != domain.ObligationSatisfied {
			t.Fatalf("setup: effective %s", st)
		}

		aliases := [][]string{
			{"docs/./a.md"},
			{"docs//a.md"},
			{"./docs/a.md"},
			{"/docs/a.md"},
			{"docs/sub/../a.md"},
			{"docs/a.md/"},
			{""},
			{"docs/a.md", "docs/a.md"},
		}
		for _, alias := range aliases {
			f.r.n++
			in := domain.ReportResourceChangeIntent{RequestID: fmt.Sprintf("rr-%d", f.r.n), ResourceID: "repo1", ExpectedRevision: f.r.rev,
				ExpectedAuthoritativeRevision: f.r.auth, ResultingAuthoritativeRevision: f.r.auth + 1, WorkspaceFingerprint: hashOf("W-alias"),
				ChangedPaths: alias}
			if _, err := f.s.report(t, f.st, f.harness, in); !errors.Is(err, domain.ErrInvalidRecord) {
				t.Fatalf("alias %q accepted: %v", alias, err)
			}
		}
		// The refusals consumed nothing: the same expected revision still
		// lands, and the proof never moved.
		f.resourceReport(t, "W-ok", false, false, []string{"docs/b.md"})
		if st, _ := f.effective(t, ref); st != domain.ObligationSatisfied {
			t.Fatalf("alias refusals disturbed the live proof: %s", st)
		}

		// The conservative direction: a changed directory naming no known
		// file still contains docs/a.md.
		f.resourceReport(t, "W-dir", false, false, []string{"docs"})
		f.wantInvalidated(t, ref, "repo1", "changed directory left the contained file's proof live")
	})
}

// TestP3_23_SettlementLimitAndFinalPageCrashRollBackEverything: a bounded
// settlement pass wedges nothing, and a crash after the final page rolls back
// the whole pass — recorded settlements, cursor, and stored statuses alike
// (P3-23 — the cited TestFailureInjectionAtomicity injects failures into
// single-proof scenarios with no paging, and K1 moved multi-proof settlement
// onto SettlePendingTx's cursor). Five file obligations of docs/a.md each
// hold a live CURRENT_PATH proof; one edit to the directory makes all five
// pending. A pass limited to two settles at most two and reports more; every
// obligation — settled or not — reads UNRESOLVED (derivation is pure read),
// so the limit changes recording, never status. A pass that crashes after
// its final page (the enclosing transaction refuses to commit) persists
// nothing: no obligation has a recorded settlement, every stored status is
// still SATISFIED with its proof, and the next clean pass settles all five
// from the same cursor — the crash rolled the cursor back too.
func TestP3_23_SettlementLimitAndFinalPageCrashRollBackEverything(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		var refs []domain.ObligationRef
		for i, slot := range []string{"30", "31", "32", "33", "34"} {
			ref := f.fileObligation(t, slot)
			refs = append(refs, ref)
			f.matcherGrant(t, fmt.Sprintf("g-23s%d", i), ref, FileReadV1, f.userP)
		}
		f.resourceReport(t, "W-a", false, false, []string{"docs/a.md"}, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
		for _, ref := range refs {
			if err := f.assertPath(t, ref, f.r.auth, "H1"); err != nil {
				t.Fatalf("proof for %s: %v", ref.ObligationID, err)
			}
			if st, _ := f.effective(t, ref); st != domain.ObligationSatisfied {
				t.Fatalf("setup: %s effective %s", ref.ObligationID, st)
			}
		}

		// One edit makes every proof pending.
		f.resourceReport(t, "W-dir", false, false, []string{"docs"})
		for _, ref := range refs {
			if st, _ := f.effective(t, ref); st != domain.ObligationUnresolved {
				t.Fatalf("edit left %s effective %s", ref.ObligationID, st)
			}
		}

		settled := func() int {
			n := 0
			for _, ref := range refs {
				if o := f.status(t, ref); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
					n++
				}
			}
			return n
		}
		runWorker := func(max int) (int, bool, error) {
			var n int
			var more bool
			err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
				var err error
				n, more, err = f.s.SettlePendingTx(tx, f.system, max)
				return err
			})
			return n, more, err
		}

		// The bounded pass: at most one settles, more remain, and no status
		// differs — derivation already reads every one UNRESOLVED.
		n, more, err := runWorker(1)
		if err != nil || n > 1 || !more {
			t.Fatalf("limited pass = %d settled, more=%v (%v), want <=1 and more", n, more, err)
		}
		for _, ref := range refs {
			if st, _ := f.effective(t, ref); st != domain.ObligationUnresolved {
				t.Fatalf("limited pass changed a status: %s -> %s", ref.ObligationID, st)
			}
		}

		// The crash after the final page: the whole pass rolls back. The
		// limited pass's committed settlements stand; the crashed one must
		// add nothing on top of them.
		afterLimit := settled()
		countSettlements := func() int {
			n := 0
			for _, ref := range refs {
				for _, tr := range f.history(t, ref) {
					if tr.Cause == domain.CauseResourceInvalidation {
						n++
					}
				}
			}
			return n
		}
		settlementsAfterLimit := countSettlements()
		storedAfterLimit := make(map[string]domain.ObligationVersion, len(refs))
		for _, ref := range refs {
			o := f.status(t, ref)
			storedAfterLimit[ref.ObligationID] = o
		}
		crash := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
			if _, _, err := f.s.SettlePendingTx(tx, f.system, 64); err != nil {
				return err
			}
			return errCrashAfterFinalPage
		})
		if crash == nil {
			t.Fatal("crash injection committed")
		}
		if got := settled(); got != afterLimit {
			t.Fatalf("crashed pass settled %d, want the limited pass's %d to stand unchanged", got, afterLimit)
		}
		if got := countSettlements(); got != settlementsAfterLimit {
			t.Fatalf("crashed pass persisted %d settlements, want %d", got, settlementsAfterLimit)
		}
		for _, ref := range refs {
			o, was := f.status(t, ref), storedAfterLimit[ref.ObligationID]
			if o.Status != was.Status || o.CurrentProofID != was.CurrentProofID || o.Revision != was.Revision {
				t.Fatalf("crashed pass changed stored state of %s: %+v (was %+v)", ref.ObligationID, o, was)
			}
		}

		// The cursor rolled back too: one clean pass settles everything.
		for more := true; more; {
			var n int
			n, more, err = runWorker(64)
			if err != nil {
				t.Fatalf("clean pass: %v", err)
			}
			_ = n
		}
		if got := settled(); got != len(refs) {
			t.Fatalf("clean pass after crash settled %d of %d", got, len(refs))
		}
		for _, ref := range refs {
			f.wantSettled(t, ref, "repo1", "settlement after crash did not record")
		}
	})
}

// errCrashAfterFinalPage stands in for the process dying after the worker's
// last write of a pass: the transaction must not commit.
var errCrashAfterFinalPage = errors.New("crash after final page")

// TestP3_23_UnrelatedResourceEditsLeaveOtherResourceProofsUntouched: a
// resource change invalidates only proofs bound to THAT resource — an edit
// of one resource never disturbs an obligation, proof, or report of another
// (P3-23 — the cited TestSEC18DeadSubjectStatesDoNotWedgeReports works one
// resource's dead states only, so nothing of the cross-resource bound is
// asserted). The sharpest form: two resources carrying the SAME canonical
// path, each with its own binding, obligation, and live CURRENT_PATH proof.
// An edit of repo1's docs/a.md invalidates repo1's proof, records its
// settlement, and leaves repo2's obligation byte-identical — status, proof,
// revision, and transition history unmoved — while repo2's next report
// still lands (no wedge). Reversed, an edit of repo2's docs/a.md
// invalidates repo2's proof and leaves repo1's re-established proof
// untouched. Affecting keys are (resource, path): the resource axis is the
// only thing separating these two proofs.
func TestP3_23_UnrelatedResourceEditsLeaveOtherResourceProofsUntouched(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)

		// repo1's obligation, bound through the task-context binding ws1.
		ref1 := f.fileObligation(t, "40")
		f.matcherGrant(t, "g-23u1", ref1, FileReadV1, f.userP)

		// A second resource carrying the same canonical path. A
		// source-context binding shadows the task-context one for pu, so
		// repo2's obligation resolves its own workspace without ambiguity.
		seedResource(t, f.st, "repo2", f.harness)
		ws2r := bindIntent("ws2r", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceSource, ID: "pu"})
		ws2r.ResourceID = "repo2"
		if _, err := f.s.bindWS(t, f.st, f.harness, ws2r); err != nil {
			t.Fatalf("bind ws2r: %v", err)
		}
		var r2 repo1
		report2 := func(request, fp string, resync, all bool, changed []string, contents ...domain.ResourcePathContent) {
			t.Helper()
			r2.n++
			in := domain.ReportResourceChangeIntent{RequestID: request, ResourceID: "repo2", ExpectedRevision: r2.rev,
				ExpectedAuthoritativeRevision: r2.auth, ResultingAuthoritativeRevision: r2.auth + 1, WorkspaceFingerprint: hashOf(fp),
				Resynchronization: resync, AllPaths: all, ChangedPaths: changed, PathContents: contents}
			if _, err := f.s.report(t, f.st, f.harness, in); err != nil {
				t.Fatalf("repo2 report %s: %v", fp, err)
			}
			r2.rev++
			r2.auth++
		}
		report2("r2-a", "R2-a", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("X1")})

		target2 := fileTarget("repo2", "docs/a.md", domain.FileCurrentContent, "")
		in2 := domain.DeclareObligationIntent{RequestID: "d-23u2", SourceItemID: "pu", DeclarationSlot: "41", Description: "read it",
			ExpectedSourceVersion: 1, Target: &target2, Matcher: &FileReadV1}
		if _, err := f.s.declare(t, f.st, f.harness, in2); err != nil {
			t.Fatal(err)
		}
		key, _ := f.item(t, "pu").CurrentKey()
		n41, _ := harnessSlot("41")
		ref2 := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, n41), Version: 1}
		if o := f.status(t, ref2); o.BindingState != domain.BindingBound {
			t.Fatalf("repo2 obligation = %+v", o)
		}
		f.matcherGrant(t, "g-23u2", ref2, FileReadV1, f.userP)
		claim2 := func(content string) error {
			t.Helper()
			o := f.status(t, ref2)
			l := domain.ResourceLocator{ResourceID: "repo2", BaseDir: ".", Path: "docs/a.md"}
			ci := intent(ref2, o.Revision, domain.ObligationSatisfied)
			ci.AssertionMode = domain.AssertionResourceBound
			ci.Resources = []domain.ResourceClaim{{Kind: domain.DependencyCurrentPath, ResourceID: "repo2", ResourceRevision: r2.auth, Fingerprint: hashOf(content), Locator: &l}}
			_, err := f.s.transition(t, f.st, f.system, ci)
			return err
		}

		// Both proofs live at the same path of two resources.
		f.resourceReport(t, "W-a", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
		if err := f.assertPath(t, ref1, f.r.auth, "H1"); err != nil {
			t.Fatalf("repo1 proof: %v", err)
		}
		if err := claim2("X1"); err != nil {
			t.Fatalf("repo2 proof: %v", err)
		}
		untouched := func(ref domain.ObligationRef, was domain.ObligationVersion, wasHistory int, msg string) {
			t.Helper()
			if o := f.status(t, ref); o.Status != was.Status || o.CurrentProofID != was.CurrentProofID || o.Revision != was.Revision {
				t.Fatalf("%s: %+v (was %+v)", msg, o, was)
			}
			if got := len(f.history(t, ref)); got != wasHistory {
				t.Fatalf("%s: %d new transitions recorded", msg, got-wasHistory)
			}
		}
		snap1, snap2 := f.status(t, ref1), f.status(t, ref2)
		hist1, hist2 := len(f.history(t, ref1)), len(f.history(t, ref2))

		// repo1's edit: repo1's proof settles, repo2 is byte-identical, and
		// repo2's reporting is not wedged.
		f.resourceReport(t, "W-b", false, false, []string{"docs/a.md"})
		f.wantInvalidated(t, ref1, "repo1", "repo1 edit left repo1's proof live")
		untouched(ref2, snap2, hist2, "repo1 edit disturbed repo2's obligation")
		report2("r2-b", "R2-b", false, false, []string{"docs/other.md"})
		untouched(ref2, snap2, hist2, "repo2 report after repo1 edit disturbed repo2")
		if st, _ := f.effective(t, ref2); st != domain.ObligationSatisfied {
			t.Fatalf("repo2 effective %s after repo1 edit", st)
		}

		// repo1 re-establishes at its new content.
		f.resourceReport(t, "W-c", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H2")})
		if err := f.assertPath(t, ref1, f.r.auth, "H2"); err != nil {
			t.Fatalf("repo1 re-proof: %v", err)
		}
		snap1, hist1 = f.status(t, ref1), len(f.history(t, ref1))

		// The reverse: repo2's edit invalidates repo2's proof only.
		report2("r2-c", "R2-c", false, false, []string{"docs/a.md"})
		f.wantInvalidated(t, ref2, "repo2", "repo2 edit left repo2's proof live")
		untouched(ref1, snap1, hist1, "repo2 edit disturbed repo1's obligation")
		if st, _ := f.effective(t, ref1); st != domain.ObligationSatisfied {
			t.Fatalf("repo1 effective %s after repo2 edit", st)
		}
	})
}

// TestP3_23_ReportersReceiptCarriesNoHiddenIDsOrCounts: the resource
// reporter's receipt names only their own update — never the fan-out's
// targets or counts (P3-23; the adjacent TestXREV11StalePathClaim never
// inspects the receipt, so nothing of what the reporter learns is
// asserted). Four live proofs hang off repo1 — three CURRENT_PATH proofs of
// docs/a.md and a tests-pass observation proof of the workspace
// fingerprint. One changed-path report invalidates all four, and the
// reporter sees exactly one RESOURCE_UPDATE reference: the result carries
// no other arm (no obligation or item payload), no proof, obligation,
// declaration, or settlement ID, and no count of anything affected — one
// reference for four invalidated proofs. The stored receipt's Result is
// byte-identical to the returned one, the exact retry replays it, and the
// invalidation itself is nowhere in the receipt: it is only visible
// downstream, as derivation and settlement record it apart from the report.
func TestP3_23_ReportersReceiptCarriesNoHiddenIDsOrCounts(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)

		// Authoritative content first, so every proof lands on the same
		// revision.
		f.resourceReport(t, "W-a", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")})
		f.matcherGrant(t, "g-23q", f.sysTests, TestsPassV1, f.system)
		f.observeTests(t, f.target, domain.OutcomePass, hashOf("W-a"), nil)
		var refs []domain.ObligationRef
		for i, slot := range []string{"50", "51", "52"} {
			ref := f.fileObligation(t, slot)
			refs = append(refs, ref)
			f.matcherGrant(t, fmt.Sprintf("g-23q%d", i), ref, FileReadV1, f.userP)
		}
		for _, ref := range refs {
			if err := f.assertPath(t, ref, f.r.auth, "H1"); err != nil {
				t.Fatalf("path proof for %s: %v", ref.ObligationID, err)
			}
		}
		dependents := append(refs, f.sysTests)
		hidden := map[string]bool{}
		for _, ref := range dependents {
			o := f.status(t, ref)
			if o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
				t.Fatalf("setup: %s = %+v", ref.ObligationID, o)
			}
			hidden[o.CurrentProofID] = true
			hidden[ref.ObligationID] = true
			hidden[o.DeclarationID] = true
		}
		if len(hidden) != 3*len(dependents) {
			t.Fatalf("setup: dependent IDs collide: %v", hidden)
		}

		// The report under inspection: one edit that invalidates all four.
		seeded := f.lastSeqIs(t)
		in := domain.ReportResourceChangeIntent{RequestID: "rr-quiet", ResourceID: "repo1", ExpectedRevision: f.r.rev,
			ExpectedAuthoritativeRevision: f.r.auth, ResultingAuthoritativeRevision: f.r.auth + 1, WorkspaceFingerprint: hashOf("W-b"),
			ChangedPaths: []string{"docs/a.md"}}
		res, err := f.s.report(t, f.st, f.harness, in)
		if err != nil {
			t.Fatalf("report: %v", err)
		}
		f.r.rev++
		f.r.auth++

		// The result has exactly one arm: the update's own reference.
		if res.Item != nil || res.Obligation != nil || res.Completion != nil || res.Collect != nil || res.Tool != nil {
			t.Fatalf("report result carries a payload arm: %+v", res)
		}
		if res.Records == nil || res.Records.Kind != "RESOURCE_UPDATE" || len(res.Records.IDs) != 1 {
			t.Fatalf("report result = %+v, want exactly one RESOURCE_UPDATE reference", res.Records)
		}
		got := res.Records.IDs[0]
		if !strings.HasPrefix(got, "ru_") {
			t.Fatalf("report names %q, want the resource update", got)
		}
		if hidden[got] {
			t.Fatalf("report names a dependent's ID: %q", got)
		}
		// The one reference is this request's update — the reporter's own
		// record, nothing of the fan-out.
		var upd domain.ResourceUpdate
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			upd, _ = r.ResourceUpdate(got)
			return nil
		})
		if upd.ID != got || upd.RequestID != "rr-quiet" || upd.ResourceID != "repo1" {
			t.Fatalf("report names %+v, want this request's update", upd)
		}

		// The stored receipt's Result is identical, and its arguments are the
		// reporter's own intent — no IDs or counts smuggled in either place.
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			rec, err := r.MutationReceipt(domain.MutationResourceReport, "rr-quiet")
			if err != nil {
				t.Errorf("receipt: %v", err)
				return nil
			}
			if !reflect.DeepEqual(rec.Result, res) {
				t.Errorf("receipt result = %+v, want the returned one", rec.Result)
			}
			for id := range hidden {
				if strings.Contains(string(rec.CanonicalArguments), id) {
					t.Errorf("receipt arguments leak %q", id)
				}
			}
			return nil
		})

		// The exact retry replays the same single reference and writes
		// nothing.
		after := f.lastSeqIs(t)
		if after == seeded {
			t.Fatalf("setup: the report wrote nothing: seq %d", seeded)
		}
		// A deferred sequence: an exact replay allocates none (DUR-2.14).
		var again domain.MutationResult
		err = f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
			var err error
			again, err = f.s.ReportResourceChangeTx(tx, f.harness, in, 0)
			return err
		})
		if err != nil || again.Records == nil || len(again.Records.IDs) != 1 || again.Records.IDs[0] != got {
			t.Fatalf("retry = %+v %v, want the same single reference", again.Records, err)
		}
		if ls := f.lastSeqIs(t); ls != after {
			t.Fatalf("replay wrote state: seq %d -> %d", after, ls)
		}

		// The fan-out the reporter never saw: all four dependents are
		// effectively invalidated, and settlement records it apart from the
		// report.
		for _, ref := range dependents {
			if st, _ := f.effective(t, ref); st != domain.ObligationUnresolved {
				t.Fatalf("%s still effective %s", ref.ObligationID, st)
			}
		}
		f.wantInvalidated(t, f.sysTests, "repo1", "quiet report left the observation proof live")
		for _, ref := range refs {
			f.wantSettled(t, ref, "repo1", "quiet report left a path proof live")
		}
	})
}
