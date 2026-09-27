package obligation

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

var errProbeRollback = errors.New("probe rollback")

// familySupported probes whether the backend implements a facet family,
// using a read that is ErrUnsupportedSchema until W2 publishes it.
func familySupported(t *testing.T, st store.Store, probe func(store.SemanticReader) error) bool {
	t.Helper()
	supported := false
	_ = st.View(t.Context(), testSession, func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err == nil {
			supported = !errors.Is(probe(r), domain.ErrUnsupportedSchema)
		}
		return nil
	})
	return supported
}

// resourceOnly lists subtests that need only the resource/workspace family.
var resourceOnly = map[string]bool{
	"ReceiptReplayAndConflict": true, "BindWorkspace": true, "ResolveWorkspace": true,
	"RegisterResource": true, "RegisterResourceReceipt": true,
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
		if err := storetest.UncheckedSetCurrentVersion(tx, it.ID); err != nil {
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
	resources := familySupported(t, sqliteBackend(t), func(r store.SemanticReader) error {
		_, err := r.ResourceBinding("probe")
		return err
	})
	proofs := familySupported(t, sqliteBackend(t), func(r store.SemanticReader) error {
		_, err := r.ExactObligation(domain.ObligationRef{SessionID: testSession, ObligationID: "probe", Version: 1})
		return err
	})
	families := resources && proofs
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
		{"DeclareForReplacement", TestDeclareForReplacement, false},
		{"DeclareForReplacementFailsClosed", TestDeclareForReplacementFailsClosed, false},
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
		{"TurnScopedEvidence", TestTurnScopedEvidence, true},
		{"G1StalePassAfterNewerFail", TestG1StalePassAfterNewerFail, true},
		{"G1StalePassAfterRejection", TestG1StalePassAfterRejection, true},
		{"G1OneTerminalObservationPerRun", TestG1OneTerminalObservationPerRun, true},
		{"XREV11StalePathClaim", TestXREV11StalePathClaim, false},
		{"SPEC19ReevaluateAfterRevalidation", TestSPEC19ReevaluateAfterRevalidation, true},
		{"SPEC110FailRejectsResourceBoundAssertion", TestSPEC110FailRejectsResourceBoundAssertion, true},
		{"SPEC111ClaimsMustCoverTarget", TestSPEC111ClaimsMustCoverTarget, false},
		{"SPEC111FixedHashTarget", TestSPEC111FixedHashTarget, false},
		{"SPEC118DirectoryChangeIntersectsFiles", TestSPEC118DirectoryChangeIntersectsFiles, false},
		{"SEC19ReevaluateIgnoresHiddenObservation", TestSEC19ReevaluateIgnoresHiddenObservation, true},
		{"DUR12ReevaluateScalesWithLiveState", TestDUR12ReevaluateScalesWithLiveState, true},
		{"DUR112OneBudgetPerTransaction", TestDUR112OneBudgetPerTransaction, false},
		{"SPEC112EvidenceBoundToExecution", TestSPEC112EvidenceBoundToExecution, true},
		{"SEC17PathCurrencyIgnoresEarlierHistory", TestSEC17PathCurrencyIgnoresEarlierHistory, false},
		{"SEC18DeadSubjectStatesDoNotWedgeReports", TestSEC18DeadSubjectStatesDoNotWedgeReports, true},
		{"DUR15DeclarationLimitPerSource", TestDUR15DeclarationLimitPerSource, false},
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
		{"DeferredSeqAllocatedAfterReplay_DUR214", TestDeferredSeqAllocatedAfterReplay_DUR214, false},
		{"ObservationStateSupersessionEnqueuesGC_SPEC23", TestObservationStateSupersessionEnqueuesGC_SPEC23, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			switch {
			case resourceOnly[tc.name] && !resources && tc.name != "ReceiptReplayAndConflict":
				t.Skip("blocked on W2: SQLite resource/workspace facet not yet published")
			case !resourceOnly[tc.name] && !families:
				t.Skip("blocked on W2: SQLite obligation/proof facet not yet published")
			}
			if tc.obs && !observations {
				t.Skip("blocked on W2: SQLite migration 0005 CHECK rejects the OBSERVATION namespace")
			}
			tc.fn(t)
		})
	}
}
