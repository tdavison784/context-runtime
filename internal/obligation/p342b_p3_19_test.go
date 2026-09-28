package obligation

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

// TestP3_19_CurrentContentClaimsMatchAuthoritativeState: the CURRENT half of
// P3-19's file modes, on the assertion path (SPEC-1.11's cited test covers
// only FIXED_HASH). A CURRENT_CONTENT obligation is satisfied only by a
// claim naming the path's AUTHORITATIVE CURRENT content and revision — not
// the old content at the new revision, not the new revision with other
// bytes, not another file's current content, and not a fixed-content
// claim, which is meaningful only against a FIXED_HASH target. The proof it
// installs is bound to that current state: the next change to the path
// invalidates it, new authoritative content re-opens it, and the very same
// edit leaves a FIXED_HASH obligation's snapshot claim valid — the two
// modes differ exactly as published.
func TestP3_19_CurrentContentClaimsMatchAuthoritativeState(t *testing.T) {
	p342BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		ref := f.fileObligation(t, "11") // CURRENT_CONTENT file_read on docs/a.md

		claim := func(kind domain.ProofDependencyKind, path, content string, rev uint64) error {
			t.Helper()
			o := f.status(t, ref)
			l := domain.ResourceLocator{ResourceID: "repo1", BaseDir: ".", Path: path}
			in := intent(ref, o.Revision, domain.ObligationSatisfied)
			in.AssertionMode = domain.AssertionResourceBound
			in.Resources = []domain.ResourceClaim{{Kind: kind, ResourceID: "repo1", ResourceRevision: rev, Fingerprint: hashOf(content), Locator: &l}}
			_, err := f.s.transition(t, f.st, f.system, in)
			return err
		}
		refused := func(what string, err error) {
			t.Helper()
			if !errors.Is(err, domain.ErrUnknownApplicability) {
				t.Fatalf("%s: %v, want ErrUnknownApplicability", what, err)
			}
		}

		// Fail closed: with no reported content for the path, no claim applies.
		before := f.r.auth
		refused("claim before any path state", claim(domain.DependencyCurrentPath, "docs/a.md", "H1", before))

		// Both files have current authoritative content at revision rev1.
		f.resourceReport(t, "W1", true, false, nil,
			domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H1")},
			domain.ResourcePathContent{Path: "docs/b.md", ContentHash: hashOf("B1")})
		rev1 := f.r.auth
		if o := f.status(t, ref); o.Status != domain.ObligationUnresolved || o.Revision != 1 {
			t.Fatalf("setup: refused probes changed the obligation: %+v", o)
		}

		// Wrong content at the current revision.
		refused("other bytes at the current revision", claim(domain.DependencyCurrentPath, "docs/a.md", "OTHER", rev1))
		// The once-current bytes at the superseded revision.
		refused("current bytes at a stale revision", claim(domain.DependencyCurrentPath, "docs/a.md", "H1", before))
		// Another file's current content, at its current revision: a valid
		// claim of the wrong file never covers the target.
		refused("another file's current content", claim(domain.DependencyCurrentPath, "docs/b.md", "B1", rev1))
		// A fixed-content claim — even of exactly the current bytes — is only
		// meaningful against a FIXED_HASH target (SPEC-1.11); on a
		// CURRENT_CONTENT obligation it would smuggle an attestation in under
		// a RESOURCE_BOUND label.
		refused("fixed-content claim on a CURRENT_CONTENT target", claim(domain.DependencyFixedContent, "docs/a.md", "H1", 0))
		if o := f.status(t, ref); o.Status != domain.ObligationUnresolved || o.Revision != 1 {
			t.Fatalf("refused claims changed the obligation: %+v", o)
		}

		// The one claim that applies: this file's current bytes at the
		// authoritative revision. It installs a proof.
		if err := claim(domain.DependencyCurrentPath, "docs/a.md", "H1", rev1); err != nil {
			t.Fatalf("current-content claim: %v", err)
		}
		sat := f.status(t, ref)
		if sat.Status != domain.ObligationSatisfied || sat.CurrentProofID == "" {
			t.Fatalf("current-content claim installed no proof: %+v", sat)
		}

		// The proof is bound to the current state: the path's next change
		// invalidates it, and new authoritative content re-opens the target.
		f.resourceReport(t, "W2", false, false, []string{"docs/a.md"})
		f.wantInvalidated(t, ref, "repo1", "path edit kept a current-content proof")
		refused("old bytes after the edit", claim(domain.DependencyCurrentPath, "docs/a.md", "H1", rev1))
		f.resourceReport(t, "W2b", true, false, nil, domain.ResourcePathContent{Path: "docs/a.md", ContentHash: hashOf("H2")})
		if err := claim(domain.DependencyCurrentPath, "docs/a.md", "H2", f.r.auth); err != nil {
			t.Fatalf("claim of the new authoritative content: %v", err)
		}
		if o := f.status(t, ref); o.Status != domain.ObligationSatisfied || o.CurrentProofID == sat.CurrentProofID {
			t.Fatalf("re-opened target did not re-prove on new content: %+v", o)
		}

		// The mode contrast: a FIXED_HASH obligation over the same path is
		// satisfied by its required snapshot even now, with the path edited
		// and holding different current bytes.
		fixed := fileTarget("repo1", "docs/a.md", domain.FileFixedHash, hashOf("REQ"))
		in := domain.DeclareObligationIntent{RequestID: "d19-fix", SourceItemID: "pu", DeclarationSlot: "12", Description: "read it",
			ExpectedSourceVersion: 1, Target: &fixed, Matcher: &FileReadV1}
		if _, err := f.s.declare(t, f.st, f.harness, in); err != nil {
			t.Fatal(err)
		}
		key, _ := f.item(t, "pu").CurrentKey()
		n, _ := harnessSlot("12")
		fixedRef := domain.ObligationRef{SessionID: testSession, ObligationID: domain.DerivedObligationID(key, n), Version: 1}
		fixedClaim := func(kind domain.ProofDependencyKind, content string, rev uint64) error {
			t.Helper()
			o := f.status(t, fixedRef)
			l := domain.ResourceLocator{ResourceID: "repo1", BaseDir: ".", Path: "docs/a.md"}
			c := domain.ResourceClaim{Kind: kind, ResourceID: "repo1", ResourceRevision: rev, Fingerprint: hashOf(content), Locator: &l}
			ci := intent(fixedRef, o.Revision, domain.ObligationSatisfied)
			ci.AssertionMode = domain.AssertionResourceBound
			ci.Resources = []domain.ResourceClaim{c}
			_, err := f.s.transition(t, f.st, f.system, ci)
			return err
		}
		refused("current-content claim naming other-than-required bytes on FIXED_HASH", fixedClaim(domain.DependencyCurrentPath, "H2", f.r.auth))
		if err := fixedClaim(domain.DependencyFixedContent, "REQ", 0); err != nil {
			t.Fatalf("fixed-content claim of the required snapshot after the edit: %v", err)
		}
		if o := f.status(t, fixedRef); o.Status != domain.ObligationSatisfied || o.CurrentProofID == "" {
			t.Fatalf("FIXED_HASH snapshot claim after the edit: %+v", o)
		}
	})
}

// TestP3_19_RestartReplaysRequestsFromDurableState: the restart half of
// P3-19's request replay (the cited TestRunAndObservationReceipts replays
// on one open store and never closes it). A SQLite store is driven through
// one epoch — workspace, pinned declaration, a registered run, a reported
// observation — then closed and reopened on the same file. Every identical
// request replays its recorded receipt with no new state, a changed typed
// field under the same request is still a conflict, the records and their
// effect survived verbatim, and the reopened service still accepts a
// genuinely new request. (Memory has no restart: Close destroys the state,
// so there is nothing durable to replay — the same-open-store half is what
// TestRunAndObservationReceipts already covers.)
func TestP3_19_RestartReplaysRequestsFromDurableState(t *testing.T) {
	if testing.Short() {
		t.Skip("SQLite backend skipped in -short mode")
	}
	path := sqlitetest.Path(t)
	epoch := func() (*fixture, func()) {
		s, err := sqlite.Open(context.Background(), path)
		if err != nil {
			t.Fatalf("open epoch: %v", err)
		}
		f := &fixture{s: newTestService(t), st: &testStore{Store: s}, harness: actorOf(domain.AuthorityHarness),
			system: actorOf(domain.AuthoritySystem), userP: actorOf(domain.AuthorityUser)}
		return f, func() { _ = s.Close() }
	}
	lastSeq := func(f *fixture) uint64 {
		t.Helper()
		var n uint64
		_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error { n = tx.LastSeq(); return nil })
		return n
	}

	// Epoch one: full workspace, one declared obligation, one run, one PASS.
	f1, close1 := epoch()
	setupWorkspace(t, f1.s, f1.st, f1.harness)
	userRef, err := pinAndDeclare(t, f1.s, f1.st, "pu", "u", domain.AuthorityUser, "All tests must pass.", "")
	if err != nil {
		t.Fatal(err)
	}
	f1.user = *userRef
	runIn := runIntent("r1", "exec-1", testsTarget(nil))
	var runRes domain.MutationResult
	mustUpdate(t, f1.st, func(tx store.Tx) error {
		var err error
		runRes, err = f1.s.RegisterRunTx(tx, f1.harness, runIn, tx.NextSeq())
		return err
	})
	var run domain.ObservationRun
	_ = f1.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		run, _ = r.ObservationRun(runRes.Records.IDs[0])
		return nil
	})
	if run.ID == "" || run.ExecutionID != "exec-1" {
		t.Fatalf("epoch-one run = %+v", run)
	}
	report := func(f *fixture, in domain.ObservationIntent) (domain.MutationResult, error) {
		var out domain.MutationResult
		err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
			var err error
			out, err = f.s.ReportObservationTx(tx, f.harness, in, tx.NextSeq())
			return err
		})
		return out, err
	}
	// reportDeferred submits with a deferred sequence, so an exact replay
	// consumes none (DUR-2.14).
	reportDeferred := func(f *fixture, in domain.ObservationIntent) (domain.MutationResult, error) {
		var out domain.MutationResult
		err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
			var err error
			out, err = f.s.ReportObservationTx(tx, f.harness, in, 0)
			return err
		})
		return out, err
	}
	obs := obsIntent("o1", run, evidenceFor(t, f1.st, run).ID, domain.OutcomePass, hashOf("W1"))
	first, err := report(f1, obs)
	if err != nil || first.Records.Kind != resultObservation {
		t.Fatalf("epoch-one report = %+v %v", first, err)
	}
	before := lastSeq(f1)
	sat := f1.status(t, f1.user)
	close1()

	// Epoch two: same file, fresh service, no re-setup.
	f2, close2 := epoch()
	defer close2()
	if after := lastSeq(f2); after != before {
		t.Fatalf("reopen changed the sequence: %d -> %d", before, after)
	}
	if o := f2.status(t, f1.user); o.Status != sat.Status || o.Revision != sat.Revision || o.CurrentProofID != sat.CurrentProofID || o.Version != sat.Version {
		t.Fatalf("declared obligation did not survive restart: %+v (was %+v)", o, sat)
	}

	// The identical run registration replays its receipt and registers
	// nothing new.
	// A deferred sequence: an exact replay allocates none (DUR-2.14).
	var runAgain domain.MutationResult
	mustUpdate(t, f2.st, func(tx store.Tx) error {
		var err error
		runAgain, err = f2.s.RegisterRunTx(tx, f2.harness, runIn, 0)
		return err
	})
	if runAgain.Records.Kind != resultObservationRun || runAgain.Records.IDs[0] != runRes.Records.IDs[0] {
		t.Fatalf("run replay after restart = %+v (want run %s)", runAgain.Records, runRes.Records.IDs[0])
	}
	// The identical observation report replays its receipt; a changed typed
	// field under the same request is still a conflict.
	again, err := reportDeferred(f2, obs)
	if err != nil || again.Records.Kind != resultObservation || again.Records.IDs[0] != first.Records.IDs[0] {
		t.Fatalf("observation replay after restart = %+v %v (want %s)", again.Records, err, first.Records.IDs[0])
	}
	changed := obs
	changed.Passed, changed.Failed, changed.Outcome = 2, 1, domain.OutcomeFail
	if _, err := reportDeferred(f2, changed); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Fatalf("changed typed field after restart: %v, want ErrEventIDConflict", err)
	}
	if after := lastSeq(f2); after != before {
		t.Fatalf("replays after restart wrote state: %d -> %d", before, after)
	}
	// The records themselves survived verbatim.
	_ = f2.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, _ := store.ReadSemantic(tx)
		run, err := r.ObservationRun(runRes.Records.IDs[0])
		if err != nil || run.ExecutionID != "exec-1" {
			t.Errorf("run record after restart = %+v %v", run, err)
		}
		o, err := r.Observation(first.Records.IDs[0])
		if err != nil || o.Outcome != domain.OutcomePass || o.Passed != 3 || o.Total != 3 {
			t.Errorf("observation record after restart = %+v %v", o, err)
		}
		return nil
	})

	// The reopened service still accepts a genuinely new request.
	runIn2 := runIntent("r2", "exec-2", testsTarget(nil))
	var run2 domain.MutationResult
	mustUpdate(t, f2.st, func(tx store.Tx) error {
		var err error
		run2, err = f2.s.RegisterRunTx(tx, f2.harness, runIn2, 0)
		return err
	})
	if run2.Records.Kind != resultObservationRun || run2.Records.IDs[0] == runRes.Records.IDs[0] {
		t.Fatalf("new request after restart = %+v", run2.Records)
	}
	if after := lastSeq(f2); after <= before {
		t.Fatalf("new request after restart wrote nothing: %d -> %d", before, after)
	}
}
