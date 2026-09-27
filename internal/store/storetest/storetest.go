// Package storetest is the implementation-agnostic conformance suite for
// store.Store. Every store implementation runs it from its own tests:
//
//	func TestConformance(t *testing.T) {
//		storetest.Run(t, func(t *testing.T) store.Store { return memory.New() })
//	}
//
// The suite uses only the store and domain packages, so it pins down the
// contract rather than any one implementation.
package storetest

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Session IDs used throughout the suite.
const (
	sessA = "sess-a"
	sessB = "sess-b"
)

// Run runs every conformance test against stores made by newStore. Each
// subtest gets a fresh, empty store, which the suite closes when the subtest
// ends (Close is idempotent, so implementations may also close it).
func Run(t *testing.T, newStore func(t *testing.T) store.Store) {
	for _, tc := range atomicSuite {
		t.Run(tc.name, func(t *testing.T) { tc.fn(t, newStore) })
	}
	for _, tc := range suite {
		t.Run(tc.name, func(t *testing.T) {
			s := newStore(t)
			t.Cleanup(func() {
				if err := s.Close(); err != nil {
					t.Errorf("Close: %v", err)
				}
			})
			tc.fn(t, s)
		})
	}
}

type testCase struct {
	name string
	fn   func(t *testing.T, s store.Store)
}

// suite lists every conformance test in execution order.
var suite = []testCase{
	{"EmptySession", testEmptySession},
	{"CloseIdempotent", testCloseIdempotent},
	{"ItemRoundTrip", testItemRoundTrip},

	// Transactions.
	{"NextSeqDense", testNextSeqDense},
	{"Allocated", testAllocated},
	{"SessionsSeqIndependent", testSessionsSeqIndependent},
	{"RollbackOnError", testRollbackOnError},
	{"RollbackOnStoreError", testRollbackOnStoreError},
	{"FailedWriteLeavesNoTrace", testFailedWriteLeavesNoTrace},
	{"ReadOwnWrites", testReadOwnWrites},
	{"ViewIsolation", testViewIsolation},
	{"ConcurrentUpdatesDense", testConcurrentUpdatesDense},
	{"SessionIsolation", testSessionIsolation},
	{"ForeignSessionRecords", testForeignSessionRecords},
	{"DeepCopies", testDeepCopies},
	{"PoisonRollsBack", testPoisonRollsBack},
	{"PoisonFirstErrorWins", testPoisonFirstErrorWins},
	{"PoisonBlocksEveryWrite", testPoisonBlocksEveryWrite},

	// Semantic state.
	{"ItemRichRoundTrip", testItemRichRoundTrip},
	{"ItemLosslessText", testItemLosslessText},
	{"ByteExactStringLists", testByteExactStringLists},
	{"ItemProvenance", testItemProvenance},
	{"BlobReferrerAccess", testBlobReferrerAccess},
	{"CanonicalCandidates", testCanonicalCandidates},
	{"CurrentWorking", testCurrentWorking},
	{"SourceItems", testSourceItems},
	{"SourceItemsMixedOwners", testSourceItemsMixedOwners},
	{"VisibleReferences", testVisibleReferences},
	{"ItemInsertRules", testItemInsertRules},
	{"ItemBlobIntegrity", testItemBlobIntegrity},
	{"ItemsFilterOrder", testItemsFilterOrder},
	{"UpdateItem", testUpdateItem},
	{"GoalLifecycle", testGoalLifecycle},
	{"Relationships", testRelationships},
	{"RelationshipFilters", testRelationshipFilters},
	{"SupersessionAcyclic", testSupersessionAcyclic},
	{"Events", testEvents},
	{"Blobs", testBlobs},
	{"DirectiveReplacement", testDirectiveReplacement},
	{"DirectiveBoundaries", testDirectiveBoundaries},
	{"CurrentVersions(DIRECTIVE)Order", testCurrentDirectivesOrder},
	{"CurrentNamespaces", testCurrentNamespaces},

	// Phase 3 semantic facet (membership, coverage, receipts).
	{"SemanticFacet", testSemanticFacet},
	{"SemanticCoverage", testSemanticCoverage},
	{"SemanticExchanges", testSemanticExchanges},
	{"SemanticMembershipFrontier", testSemanticMembershipFrontier},
	{"SemanticCheckpoints", testSemanticCheckpoints},
	{"SemanticReceipts", testSemanticReceipts},
	{"SemanticCreationDeclarations", testSemanticCreationDeclarations},
	{"SemanticSnapshotDeclarations", testSemanticSnapshotDeclarations},
	{"SemanticCurrentPointerCAS", testSemanticCurrentPointerCAS},
	{"SemanticGrantsFor", testSemanticGrantsFor},
	{"SemanticGrantDuplicateTargets", testSemanticGrantDuplicateTargets},
	{"SemanticLiveGrantsFor", testSemanticLiveGrantsFor},
	{"SemanticChanges", testSemanticChanges},
	{"SemanticResources", testSemanticResources},
	{"SemanticResourcePaths", testSemanticResourcePaths},
	{"SemanticResourceUpdatesAffectingPath", testSemanticResourceUpdatesAffectingPath},
	{"SemanticWorkspaceBindings", testSemanticWorkspaceBindings},
	{"SemanticObservations", testSemanticObservations},
	{"SemanticObservationEvidenceExecution", testSemanticObservationEvidenceExecution},
	{"SemanticRunOrdinalUnique", testSemanticRunOrdinalUnique},
	{"SemanticRunClosesOnce", testSemanticRunClosesOnce},
	{"SemanticLiveSubjectStates", testSemanticLiveSubjectStates},
	{"SemanticObligationTransitionByID", testSemanticObligationTransitionByID},
	{"SemanticLatestUpdateAffectingPath", testSemanticLatestUpdateAffectingPath},
	{"SemanticClosingObservation", testSemanticClosingObservation},
	{"SemanticLifecycleEventByID", testSemanticLifecycleEventByID},
	{"SemanticEarliestExchangeWithItem", testSemanticEarliestExchangeWithItem},
	{"SemanticLiveGrantsMatchGrantLiveAt", testSemanticLiveGrantsMatchGrantLiveAt},
	{"SemanticSubjectHighWater", testSemanticSubjectHighWater},
	{"SemanticCurrentWorkspaceBindings", testSemanticCurrentWorkspaceBindings},
	{"SemanticIndexedVersions", testSemanticIndexedVersions},
	{"SemanticWorkspaceBindingCursor", testSemanticWorkspaceBindingCursor},
	{"SemanticOwnerIDReuse", testSemanticOwnerIDReuse},
	{"SemanticObligationDeclarations", testSemanticObligationDeclarations},
	{"SemanticMatcherProof", testSemanticMatcherProof},
	{"SemanticProofReferences", testSemanticProofReferences},
	{"SemanticInvalidation", testSemanticInvalidation},
	{"SemanticAttestation", testSemanticAttestation},
	{"SemanticMaterialization", testSemanticMaterialization},
	{"SemanticRetrieval", testSemanticRetrieval},
	{"SemanticProjectionItemAccess", testSemanticProjectionItemAccess},
	{"SemanticGCRequests", testSemanticGCRequests},
	{"SemanticGCOutcomes", testSemanticGCOutcomes},
	{"SemanticGCBatchReceipts", testSemanticGCBatchReceipts},
	{"SemanticGCProgress", testSemanticGCProgress},
	{"SemanticGCCandidates", testSemanticGCCandidates},
	{"SemanticOpenGoalsByTaskOwner", testSemanticOpenGoalsByTaskOwner},
	{"SemanticLedgerSeqIsolation", testSemanticLedgerSeqIsolation},
	{"SemanticSatisfactionBacking", testSemanticSatisfactionBacking},
	{"SemanticStaleProof", testSemanticStaleProof},
	{"SemanticStaleProofWithoutState", testSemanticStaleProofWithoutState},
	{"SemanticStaleProofPrivateFail", testSemanticStaleProofPrivateFail},
	{"RawTransitionCannotSatisfy", testRawTransitionCannotSatisfy},

	// Ingestion records.
	{"IngestionRoundTrip", testIngestionRoundTrip},
	{"IngestionV3RoundTrip", testIngestionV3RoundTrip},
	{"ReceiptKeepsOriginalItems", testReceiptKeepsOriginalItems},
	{"IngestionInsertRules", testIngestionInsertRules},
	{"AnonymousIngestions", testAnonymousIngestions},
	{"DiagnosticsAccess", testDiagnosticsAccess},
	{"IngestionDeepCopies", testIngestionDeepCopies},
	{"ReceiptRecordsEveryLimit", testReceiptRecordsEveryLimit},
	{"UnresolvedReferences", testUnresolvedReferences},
	{"UnresolvedReferenceInsertRules", testUnresolvedReferenceInsertRules},

	// Obligations, grants, tasks, audit, and the call ledger.
	{"ObligationVersions", testObligationVersions},
	{"ObligationClaim", testObligationClaim},
	{"ObligationsBySource", testObligationsBySource},
	{"RetireObligationVersion", testRetireObligationVersion},
	{"ObligationTransitions", testObligationTransitions},
	{"MatcherTransition", testMatcherTransition},
	{"Grants", testGrants},
	{"Tasks", testTasks},
	{"LifecycleEvents", testLifecycleEvents},
	{"Conversations", testConversations},
	{"Calls", testCalls},
	{"CallReservation", testCallReservation},
	{"CallEvidence", testCallEvidence},
	{"CallAttemptBinding", testCallAttemptBinding},

	// Store-wide rules.
	{"SemanticWriteRule", testSemanticWriteRule},
	{"Sessions", testSessions},
	{"Cancellation", testCancellation},
	{"LedgerSeqIsolation", testLedgerSeqIsolation},
	{"CallAttempts", testCallAttempts},
}

var ctx = context.Background()

// update runs fn in an Update on sess and fails the test on error.
func update(t *testing.T, s store.Store, sess string, fn func(tx store.Tx) error) {
	t.Helper()
	if err := s.Update(ctx, sess, fn); err != nil {
		t.Fatalf("Update(%s): %v", sess, err)
	}
}

// view runs fn in a View on sess and fails the test on error.
func view(t *testing.T, s store.Store, sess string, fn func(tx store.ReadTx) error) {
	t.Helper()
	if err := s.View(ctx, sess, fn); err != nil {
		t.Fatalf("View(%s): %v", sess, err)
	}
}

// rejected runs fn in its own transaction and fails the test unless Update
// returns want. A write that fails after another write succeeded poisons its
// transaction (P3-1), so a rejection case never shares a transaction with
// setup writes it expects to commit.
func rejected(t *testing.T, s store.Store, sess string, want error, fn func(tx store.Tx) error) {
	t.Helper()
	if err := s.Update(ctx, sess, fn); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

// wantErr fails unless errors.Is(err, want).
func wantErr(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

// noErr fails on a non-nil error.
func noErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// errOf2 returns the error of a three-result call.
func errOf2[T, U any](_ T, _ U, err error) error { return err }

// errOf returns the error of a two-result call.
func errOf[T any](_ T, err error) error { return err }

// audited appends a lifecycle event with a fresh sequence number, so a
// transaction whose other writes carry none satisfies the semantic-write
// rule.
func audited(tx store.Tx) error {
	seq := tx.NextSeq()
	return tx.AppendLifecycleEvent(NewLifecycleEvent(tx.SessionID(), fmt.Sprintf("audit-%d", seq), seq, domain.TargetItem, "audit"))
}

// seqs allocates n sequence numbers.
func seqs(tx store.Tx, n int) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = tx.NextSeq()
	}
	return out
}

func testEmptySession(t *testing.T, s store.Store) {
	view(t, s, sessA, func(tx store.ReadTx) error {
		if got := tx.SessionID(); got != sessA {
			t.Errorf("SessionID = %q, want %q", got, sessA)
		}
		if got := tx.LastSeq(); got != 0 {
			t.Errorf("LastSeq = %d, want 0", got)
		}
		_, err := tx.Item("missing")
		wantErr(t, err, domain.ErrNotFound)
		items, err := tx.Items(store.ItemFilter{})
		noErr(t, err)
		if len(items) != 0 {
			t.Errorf("Items = %d records, want 0", len(items))
		}
		return nil
	})
}

func testCloseIdempotent(t *testing.T, s store.Store) {
	noErr(t, s.Close())
	noErr(t, s.Close())
}

func testItemRoundTrip(t *testing.T, s store.Store) {
	var want domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		want = NewItem(sessA, "i1", tx.NextSeq(), "hello")
		return tx.InsertItem(want)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.Item("i1")
		noErr(t, err)
		assertEqual(t, "Item", got, want)
		return nil
	})
}
