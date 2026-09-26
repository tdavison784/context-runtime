package obligation

import (
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// snapshot is the committed state an injected failure must leave unchanged:
// the session sequence (every committed write consumes one) and every
// obligation version's status, revision, and proof caches.
type snapshot struct {
	lastSeq     uint64
	obligations string
}

func (f fixture) snap(t *testing.T) snapshot {
	t.Helper()
	var s snapshot
	_ = f.st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		s.lastSeq = tx.LastSeq()
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		all, _ := tx.Obligations("")
		for _, o := range all {
			for v := uint64(1); v <= o.Version; v++ {
				x, err := r.ExactObligation(domain.ObligationRef{SessionID: testSession, ObligationID: o.ObligationID, Version: v})
				if err != nil {
					return err
				}
				s.obligations += fmt.Sprintf("%s/%d:%s:%d:%s:%s:%v;", x.ObligationID, x.Version, x.Status, x.Revision, x.CurrentProofID, x.CurrentAssertionID, x.Current)
			}
		}
		return nil
	})
	return s
}

type scenario struct {
	name  string
	setup func(t *testing.T) (fixture, func(tx store.Tx) error)
	obs   bool // files OBSERVATION-namespace state
}

// observationsUnsupported is set by a backend suite whose store cannot file
// OBSERVATION-namespace state yet; scenarios that need it are skipped.
var observationsUnsupported bool

func injectionScenarios() []scenario {
	satisfiedEval := func(t *testing.T) *evalFixture {
		f := newEvalFixture(t)
		f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
		f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
		return f
	}
	registered := func(t *testing.T, f *evalFixture) domain.ObservationRun {
		runN++
		run, err := f.registerRun(t, f.harness, runIntent(fmt.Sprintf("run-%d", runN), fmt.Sprintf("exec-%d", runN), f.target))
		if err != nil {
			t.Fatal(err)
		}
		return run
	}
	observe := func(f *evalFixture, run domain.ObservationRun, fp string) func(tx store.Tx) error {
		return func(tx store.Tx) error {
			sem, err := begin(tx, f.harness, tx.NextSeq())
			if err != nil {
				return err
			}
			_, err = f.s.reportObservation(tx, sem, f.harness, obsIntent("obs-inject", run, f.evidence.ID, domain.OutcomePass, fp), tx.LastSeq())
			return err
		}
	}
	return []scenario{
		{"resource report invalidating proof and state", func(t *testing.T) (fixture, func(tx store.Tx) error) {
			f := satisfiedEval(t)
			in := domain.ReportResourceChangeIntent{RequestID: "inject", ResourceID: "repo1", ExpectedRevision: f.r.rev, ExpectedAuthoritativeRevision: f.r.auth, ResultingAuthoritativeRevision: f.r.auth + 1, WorkspaceFingerprint: hashOf("W2"), AllPaths: true}
			return f.fixture, func(tx store.Tx) error {
				_, err := f.s.ReportResourceChangeTx(tx, f.harness, in, tx.NextSeq())
				return err
			}
		}, true},
		{"observation deriving state and satisfying", func(t *testing.T) (fixture, func(tx store.Tx) error) {
			f := newEvalFixture(t)
			f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
			return f.fixture, observe(f, registered(t, f), hashOf("W1"))
		}, true},
		{"proof refresh pair", func(t *testing.T) (fixture, func(tx store.Tx) error) {
			f := satisfiedEval(t)
			return f.fixture, observe(f, registered(t, f), hashOf("W1"))
		}, true},
		{"proof rejection", func(t *testing.T) (fixture, func(tx store.Tx) error) {
			f := satisfiedEval(t)
			run := registered(t, f)
			return f.fixture, func(tx store.Tx) error {
				sem, err := begin(tx, f.harness, tx.NextSeq())
				if err != nil {
					return err
				}
				_, err = f.s.reportObservation(tx, sem, f.harness, obsIntent("obs-fail", run, f.evidence.ID, domain.OutcomeFail, hashOf("W1")), tx.LastSeq())
				return err
			}
		}, true},
		{"resource report invalidating an assertion proof", func(t *testing.T) (fixture, func(tx store.Tx) error) {
			f := newEvalFixture(t)
			in := intent(f.user, 1, domain.ObligationSatisfied)
			in.AssertionMode = domain.AssertionResourceBound
			in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo1", ResourceRevision: 1, Fingerprint: hashOf("W1")}}
			if _, err := f.s.transition(t, f.st, f.system, in); err != nil {
				t.Fatal(err)
			}
			rep := domain.ReportResourceChangeIntent{RequestID: "inject", ResourceID: "repo1", ExpectedRevision: f.r.rev, ExpectedAuthoritativeRevision: f.r.auth, ResultingAuthoritativeRevision: f.r.auth + 1, WorkspaceFingerprint: hashOf("W2"), AllPaths: true}
			return f.fixture, func(tx store.Tx) error {
				_, err := f.s.ReportResourceChangeTx(tx, f.harness, rep, tx.NextSeq())
				return err
			}
		}, false},
		{"resource-bound assertion", func(t *testing.T) (fixture, func(tx store.Tx) error) {
			f := newEvalFixture(t)
			in := intent(f.user, 1, domain.ObligationSatisfied)
			in.AssertionMode = domain.AssertionResourceBound
			in.EvidenceIDs = []string{f.evidence.ID}
			in.Resources = []domain.ResourceClaim{{Kind: domain.DependencyWorkspace, ResourceID: "repo1", ResourceRevision: 1, Fingerprint: hashOf("W1")}}
			return f.fixture, func(tx store.Tx) error {
				_, err := f.s.ApplyTransitionTx(tx, f.system, in, tx.NextSeq())
				return err
			}
		}, false},
		{"HARNESS declaration", func(t *testing.T) (fixture, func(tx store.Tx) error) {
			f := newFixture(t)
			src := seedPinned(t, f.st, "p-h", "h", domain.AuthorityUser, "Deploy.")
			return f, func(tx store.Tx) error {
				_, err := f.s.DeclareObligationTx(tx, f.harness, harnessDecl("d-inject", src.ID, 1, "1"), tx.NextSeq())
				return err
			}
		}, false},
		{"reevaluation satisfying", func(t *testing.T) (fixture, func(tx store.Tx) error) {
			f := newEvalFixture(t)
			f.observeTests(t, f.target, domain.OutcomePass, hashOf("W1"), nil)
			f.matcherGrant(t, "g-sys", f.sysTests, TestsPassV1, f.system)
			return f.fixture, func(tx store.Tx) error {
				_, err := f.s.ReevaluateTx(tx, f.harness, domain.ReevaluateIntent{RequestID: "re-inject", Target: f.sysTests, ExpectedRevision: 1}, tx.NextSeq())
				return err
			}
		}, true},
	}
}

// TestFailureInjectionAtomicity fails each constituent facet write of every
// multi-record operation in turn, with the caller ignoring the error, and
// requires that nothing commits (P3-1, gate: failure injection).
func TestFailureInjectionAtomicity(t *testing.T) {
	for _, sc := range injectionScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			if sc.obs && observationsUnsupported {
				t.Skip("backend cannot file OBSERVATION-namespace state yet")
			}
			for k := 1; ; k++ {
				if k > 64 {
					t.Fatal("operation never completed")
				}
				f, op := sc.setup(t)
				before := f.snap(t)
				f.st.failAt.Store(int64(k))
				err := f.st.Update(t.Context(), testSession, func(tx store.Tx) error {
					_ = op(tx) // deliberately ignored: poisoning must still roll back
					return nil
				})
				after := f.snap(t)
				if err == nil {
					if k == 1 {
						t.Fatal("operation made no facet writes")
					}
					if after == before {
						t.Fatal("successful operation changed nothing")
					}
					t.Logf("rolled back at each of %d constituent writes", k-1)
					return
				}
				if after != before {
					t.Fatalf("failure at write %d committed a partial effect: %+v -> %+v", k, before, after)
				}
			}
		})
	}
}
