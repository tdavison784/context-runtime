package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// P3-13 (ADR 8 :1123/:1125/:1126): transition intents enforce the exact
// state machine, and their evidence and applicability references are backed
// by persisted records. The cited tests are pure domain validations
// (span-vs-item exclusivity, empty evidence); the missing probes are the
// service refusing, whole, an intent whose references name nothing real —
// and leaving status, history, and caches in exact parity after a refused
// CAS.

// TestP3_13_ObservationEvidenceAbsentForeignOrPrivateRefused closes the
// MISSING half of "absent/future/private evidence" (ADR 8 :1123).
// TestObservationIntentEvidenceReferenceIsExclusive checks only that an
// intent names at most one evidence reference; nothing asserts what the
// named reference must be. Through the real report path on both stores: a
// well-formed nonempty evidence ID that names no item, a TOOL occurrence
// produced by another execution, and one produced by the run's own
// execution but owned by another agent are each refused with the fixed
// ErrInvalidRecord, and the run keeps no observation, closing or otherwise.
// The valid control report on the same fixture shape succeeds.
func TestP3_13_ObservationEvidenceAbsentForeignOrPrivateRefused(t *testing.T) {
	p3_14BothStores(t, func(t *testing.T) {
		f := newFixture(t)
		// A task-owned run with its proper evidence: the control shape every
		// probe varies one field of.
		probe := func(name, exec, evidence string) (domain.ObservationRun, error) {
			t.Helper()
			run, err := f.registerRun(t, f.harness, runIntent("run-p313-"+name, exec, testsTarget(nil)))
			if err != nil {
				t.Fatalf("register %s: %v", name, err)
			}
			_, err = f.observe(t, f.harness, obsIntent("obs-p313-"+name, run, evidence, domain.OutcomePass, hashOf("W1")))
			return run, err
		}
		refused := func(t *testing.T, name string, run domain.ObservationRun, err error) {
			t.Helper()
			if !errors.Is(err, domain.ErrInvalidRecord) {
				t.Fatalf("%s evidence: %v, want ErrInvalidRecord", name, err)
			}
			if err := f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
				r, err := store.ReadSemantic(tx)
				if err != nil {
					return err
				}
				pg, err := r.ObservationsByRun(run.ID, store.Page{Limit: 8})
				if err != nil {
					return err
				}
				if len(pg.Records) != 0 || pg.More {
					t.Errorf("%s evidence left observations: %+v", name, pg.Records)
				}
				if _, err := r.ClosingObservation(run.ID); !errors.Is(err, domain.ErrNotFound) {
					t.Errorf("%s evidence closed the run: %v", name, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}

		// Absent: a well-formed nonempty ID naming no stored occurrence.
		run, err := probe("absent", "exec-absent", "ev-p313-never-stored")
		refused(t, "absent", run, err)
		// Foreign execution: the occurrence exists and is task-visible, but
		// another execution produced it, so it is not this run's result.
		foreign := seedEvidenceAs(t, f.st, "ev-p313-foreign", taskBoundary(), "exec-p313-somewhere-else")
		run, err = probe("foreign", "exec-foreign", foreign.ID)
		refused(t, "foreign", run, err)
		// Private: produced by the run's own execution, but owned by another
		// agent — evidence private to b can never evidence the task's run.
		private := seedEvidenceAs(t, f.st, "ev-p313-private",
			domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: testSession, TaskID: "task", AgentID: "b"}, "exec-private")
		run, err = probe("private", "exec-private", private.ID)
		refused(t, "private", run, err)

		// Control: the same shape with the run's own properly owned evidence
		// reports and closes the run.
		run, err = f.registerRun(t, f.harness, runIntent("run-p313-valid", "exec-valid", testsTarget(nil)))
		if err != nil {
			t.Fatalf("register valid: %v", err)
		}
		if _, err := f.observe(t, f.harness, obsIntent("obs-p313-valid", run, evidenceFor(t, f.st, run).ID, domain.OutcomePass, hashOf("W1"))); err != nil {
			t.Fatalf("valid control: %v", err)
		}
		if err := f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			if _, err := r.ClosingObservation(run.ID); err != nil {
				t.Errorf("valid control did not close its run: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestP3_13_BogusFingerprintCannotSatisfy closes the MISSING half of
// "nonempty bogus fingerprint ID" (ADR 8 :1125).
// TestObservationCannotFabricateEvidenceOrPass proves an empty evidence ID
// or a PASS with failures fails domain validation — a NONEMPTY fingerprint
// naming the wrong content validates fine, so only the service can catch it.
// A resource-bound satisfaction whose claim carries a well-formed
// fingerprint that is not the workspace's current one is refused whole with
// ErrUnknownApplicability and leaves no status change, proof, assertion or
// history; the same intent with the real fingerprint satisfies.
func TestP3_13_BogusFingerprintCannotSatisfy(t *testing.T) {
	p3_14BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		satisfy := func(req, fp string) domain.TransitionIntent {
			in := intent(f.sysTests, 1, domain.ObligationSatisfied)
			in.RequestID = req
			in.AssertionMode = domain.AssertionResourceBound
			in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo1", ResourceRevision: f.r.auth, Fingerprint: fp}}
			return in
		}
		if _, err := f.s.transition(t, f.st, f.system, satisfy("tr-p313-bogus", hashOf("not-the-workspace"))); !errors.Is(err, domain.ErrUnknownApplicability) {
			t.Fatalf("bogus fingerprint: %v, want ErrUnknownApplicability", err)
		}
		o := f.status(t, f.sysTests)
		if o.Status != domain.ObligationUnresolved || o.Revision != 1 || o.CurrentProofID != "" || o.CurrentAssertionID != "" {
			t.Errorf("bogus fingerprint changed the version: %+v", o)
		}
		if h := f.history(t, f.sysTests); len(h) != 0 {
			t.Errorf("bogus fingerprint left transitions: %+v", h)
		}

		// Control: the workspace's actual fingerprint satisfies and installs
		// the resource-bound proof.
		res, err := f.s.transition(t, f.st, f.system, satisfy("tr-p313-real", hashOf("W1")))
		if err != nil {
			t.Fatalf("real fingerprint: %v", err)
		}
		o = f.status(t, f.sysTests)
		if o.Status != domain.ObligationSatisfied || o.Revision != 2 || o.CurrentProofID == "" || res.Obligation.ProofID != o.CurrentProofID {
			t.Errorf("real fingerprint: version = %+v result = %+v", o, res.Obligation)
		}
	})
}

// TestP3_13_FailedCASLeavesStatusHistoryAndCacheInParity closes the MISSING
// half of "status/history/cache rollback parity" (ADR 8 :1126).
// TestTransitionCASAndReplay checks the error code and the replay; the
// missing probe is the parity itself: after a satisfaction, a revalidation
// whose expected revision names the version's PAST revision fails with
// ErrVersionConflict and leaves the stored status, revision, assertion and
// proof caches, the transition history, and the audit trail exactly as the
// successful transition left them. The same intent at the current revision
// revalidates.
func TestP3_13_FailedCASLeavesStatusHistoryAndCacheInParity(t *testing.T) {
	p3_14BothStores(t, func(t *testing.T) {
		f := newEvalFixture(t)
		if _, err := f.s.transition(t, f.st, f.system, intent(f.sysTests, 1, domain.ObligationSatisfied)); err != nil {
			t.Fatalf("setup satisfaction: %v", err)
		}
		before := f.status(t, f.sysTests)
		hist := f.history(t, f.sysTests)
		if before.Status != domain.ObligationSatisfied || before.Revision != 2 || len(hist) != 1 {
			t.Fatalf("setup: version = %+v history = %+v", before, hist)
		}

		stale := domain.TransitionIntent{RequestID: "tr-p313-stale", Target: f.sysTests, ExpectedRevision: 1, To: domain.ObligationUnresolved}
		if _, err := f.s.transition(t, f.st, f.system, stale); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("stale revalidation: %v, want ErrVersionConflict", err)
		}
		after := f.status(t, f.sysTests)
		if after.Status != before.Status || after.Revision != before.Revision ||
			after.CurrentAssertionID != before.CurrentAssertionID || after.CurrentProofID != before.CurrentProofID ||
			!equalIDs(after.EvidenceIDs, before.EvidenceIDs) {
			t.Errorf("failed CAS changed the version:\nbefore %+v\nafter  %+v", before, after)
		}
		if h := f.history(t, f.sysTests); len(h) != len(hist) || h[0].ID != hist[0].ID {
			t.Errorf("failed CAS changed history: %+v", h)
		}

		// Control: the same intent at the current revision revalidates.
		cur := domain.TransitionIntent{RequestID: "tr-p313-cur", Target: f.sysTests, ExpectedRevision: before.Revision, To: domain.ObligationUnresolved}
		if _, err := f.s.transition(t, f.st, f.system, cur); err != nil {
			t.Fatalf("current revalidation: %v", err)
		}
		if o := f.status(t, f.sysTests); o.Status != domain.ObligationUnresolved || o.Revision != before.Revision+1 {
			t.Errorf("current revalidation: %+v", o)
		}
	})
}

// equalIDs compares two optional string slices without distinguishing nil
// from empty: what the caches hold is presence, not allocation.
func equalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
