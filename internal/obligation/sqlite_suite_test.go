package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

var errProbeRollback = errors.New("probe rollback")

// w4FamiliesSupported probes whether the backend implements W4's proof,
// resource, and observation facet families (W2 publishes SQLite after memory).
func w4FamiliesSupported(t *testing.T, st store.Store) bool {
	t.Helper()
	supported := true
	_ = st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			supported = false
			return nil
		}
		for _, err := range []error{
			func() error { _, err := r.ResourceBinding("probe"); return err }(),
			func() error { _, err := r.ApplicabilityProof("probe"); return err }(),
			func() error { _, err := r.ObservationRun("probe"); return err }(),
		} {
			if errors.Is(err, domain.ErrUnsupportedSchema) {
				supported = false
			}
		}
		return nil
	})
	return supported
}

// observationNamespaceSupported probes whether the backend can file an
// OBSERVATION-namespace current pointer (P3-3). SQLite migration 0005's
// CHECK(namespace IN ('DIRECTIVE','AGENT_KEY')) rejects it until W2's forward
// migration lands; the probe rolls back either way.
func observationNamespaceSupported(t *testing.T, st store.Store) bool {
	t.Helper()
	sub := mustSubjectKey(testsTarget(nil))
	err := st.Update(t.Context(), testSession, func(tx store.Tx) error {
		seq := tx.NextSeq()
		run := domain.ObservationRun{SubjectKey: sub, TaskID: "task", Access: taskBoundary()}
		obs := domain.ObservationRecord{SemanticMeta: domain.SemanticMeta{ID: "obs-probe", SessionID: testSession}, Family: domain.ObservationTests, Outcome: domain.OutcomePass, Completeness: domain.ObservationComplete}
		it := stateItem(obs, run, seq)
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := tx.SetCurrentVersion(it.ID); err != nil {
			return err
		}
		return errProbeRollback
	})
	return errors.Is(err, errProbeRollback)
}

// TestSQLiteSuite reruns the service, trace, failure-injection, and
// concurrency tests against W2's SQLite backend. Families the backend has not
// published yet fall back to the temporary facet (semstore_test.go).
func TestSQLiteSuite(t *testing.T) {
	if testing.Short() {
		t.Skip("SQLite suite skipped in -short mode")
	}
	prev := backendFactory
	backendFactory = sqliteBackend
	t.Cleanup(func() { backendFactory = prev })
	families := w4FamiliesSupported(t, sqliteBackend(t))
	observations := families && observationNamespaceSupported(t, sqliteBackend(t))
	observationsUnsupported = !observations
	t.Cleanup(func() { observationsUnsupported = false })
	for _, tc := range []struct {
		name string
		fn   func(*testing.T)
		obs  bool // files OBSERVATION-namespace state
	}{
		{"ReceiptReplayAndConflict", TestReceiptReplayAndConflict, false},
		{"BindWorkspace", TestBindWorkspace, false},
		{"ResolveWorkspace", TestResolveWorkspace, false},
		{"DeclarePinnedBound", TestDeclarePinnedBound, false},
		{"DeclarePinnedCases", TestDeclarePinnedCases, false},
		{"DeclarePinnedRejects", TestDeclarePinnedRejects, false},
		{"DeclarePinnedReplacementVersions", TestDeclarePinnedReplacementVersions, false},
		{"DeclareHarness", TestDeclareHarness, false},
		{"SetMaterialization", TestSetMaterialization, false},
		{"TransitionMatrix", TestTransitionMatrix, false},
		{"TransitionAuthorityT06", TestTransitionAuthorityT06, false},
		{"TransitionEvidence", TestTransitionEvidence, false},
		{"TransitionCASAndReplay", TestTransitionCASAndReplay, false},
		{"TransitionRetiredVersion", TestTransitionRetiredVersion, false},
		{"TransitionResourceBound", TestTransitionResourceBound, false},
		{"TransitionIgnoredErrorPoisons", TestTransitionIgnoredErrorPoisons, false},
		{"RegisterResource", TestRegisterResource, false},
		{"ResourceBaselineAndOrdering", TestResourceBaselineAndOrdering, false},
		{"ResourceGapBecomesUnknown", TestResourceGapBecomesUnknown, false},
		{"ResourceInvalidationT07", TestResourceInvalidationT07, false},
		{"ResourceInvalidationScope", TestResourceInvalidationScope, false},
		{"ResourceInvalidationPagingAndLimit", TestResourceInvalidationPagingAndLimit, false},
		{"RegisterRun", TestRegisterRun, false},
		{"ReportObservation", TestReportObservation, false},
		{"ObservationStateChain", TestObservationStateChain, true},
		{"ObservationStateGating", TestObservationStateGating, true},
		{"MatcherGrantT06", TestMatcherGrantT06, true},
		{"MatcherInvalidationAndReproofT07", TestMatcherInvalidationAndReproofT07, true},
		{"ProofRejection", TestProofRejection, true},
		{"ProofRefresh", TestProofRefresh, true},
		{"PrivateEvidenceNotPublished", TestPrivateEvidenceNotPublished, true},
		{"FileReadEndToEnd", TestFileReadEndToEnd, true},
		{"ReevaluateAfterGrant", TestReevaluateAfterGrant, true},
		{"ReevaluateStaleEvidenceAndUnblock", TestReevaluateStaleEvidenceAndUnblock, true},
		{"UnfinishedTaskObligations", TestUnfinishedTaskObligations, false},
		{"SatisfiesView", TestSatisfiesView, true},
		{"SatisfiesNoEdgeForAttestation", TestSatisfiesNoEdgeForAttestation, false},
		{"VisibleObligations", TestVisibleObligations, false},
		{"SemanticChangeRecords", TestSemanticChangeRecords, true},
		{"FailureInjectionAtomicity", TestFailureInjectionAtomicity, false},
		{"TraceT06", TestTraceT06, true},
		{"TraceT07", TestTraceT07, true},
		{"TraceT02Obligation", TestTraceT02Obligation, true},
		{"TraceT07PublicAPI", TestTraceT07PublicAPI, true},
		{"RunAndObservationReceipts", TestRunAndObservationReceipts, true},
		{"RegisterResourceReceipt", TestRegisterResourceReceipt, false},
		{"ConcurrentINV16", TestConcurrentINV16, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if !families && tc.name != "ReceiptReplayAndConflict" {
				t.Skip("blocked on W2: SQLite proof/resource/observation facet not yet published")
			}
			if tc.obs && !observations {
				t.Skip("blocked on W2: SQLite migration 0005 CHECK rejects the OBSERVATION namespace")
			}
			tc.fn(t)
		})
	}
}
