package storetest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// testSemanticLedgerSeqIsolation extends testLedgerSeqIsolation to the
// Phase 3 records (P3-1, SPEC-1.4): no semantic record, including a CAS
// pointer row, may share a TargetCall event's sequence number, so a
// semantic write never hides behind ledger bookkeeping.
func testSemanticLedgerSeqIsolation(t *testing.T, s store.Store) {
	var u1 domain.ResourceUpdate
	var run1 domain.ObservationRun
	var o domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		putTask(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		u1 = NewResourceUpdate(sessA, "u1", "repo", tx.NextSeq(), 0, fpA, "src/a.go")
		noErr(t, sem.InsertResourceUpdate(u1))
		noErr(t, sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 1, tx.NextSeq())))
		run1 = NewObservationRun(t, sessA, "run1", "repo", "wb", tx.NextSeq())
		noErr(t, sem.InsertObservationRun(run1))
		noErr(t, tx.InsertItem(ToolEvidence(sessA, "ev1", tx.NextSeq())))
		noErr(t, sem.InsertObservation(NewObservation(run1, "obs1", "ev1", tx.NextSeq(), fpA)))
		noErr(t, tx.InsertItem(SemanticDirective(sessA, "src", "dep", tx.NextSeq(), "All tests must pass")))
		o = BoundObligation(t, sessA, "o1", 1, tx.NextSeq(), "src")
		return tx.InsertObligationVersion(o)
	})
	cases := []struct {
		name  string
		write func(sem store.SemanticTx, seq uint64) error
	}{
		{"resource update", func(sem store.SemanticTx, seq uint64) error {
			return sem.InsertResourceUpdate(NewResourceUpdate(sessA, "u2", "repo", seq, 1, fpB, "src/a.go"))
		}},
		{"resource path state", func(sem store.SemanticTx, seq uint64) error {
			_, err := sem.PutResourcePathState(domain.ResourcePathState{SemanticMeta: Meta(sessA, "ps-a", seq),
				Locator: domain.ResourceLocator{ResourceID: "repo", BaseDir: ".", Path: "src/a.go"}, ContentHash: domain.HashBytes([]byte("a")),
				ResourceUpdateID: u1.ID, ResourceRevision: u1.ResultingAuthoritativeRevision, Revision: 1, Freshness: domain.ResourceKnown}, 0)
			return err
		}},
		{"observation run", func(sem store.SemanticTx, seq uint64) error {
			return sem.InsertObservationRun(NewObservationRun(t, sessA, "run2", "repo", "wb", seq))
		}},
		{"subject state", func(sem store.SemanticTx, seq uint64) error {
			_, err := sem.PutSubjectState(domain.SubjectState{SemanticMeta: Meta(sessA, "ss", seq), SubjectKey: run1.SubjectKey, TaskID: "task",
				CurrentItemID: "ev1", ObservationID: "obs1", Access: run1.Access, AcceptedOrdinal: run1.Ordinal, Revision: 1,
				Applicability: domain.ApplicabilityCurrent}, 0, "obs1")
			return err
		}},
		{"obligation declaration", func(sem store.SemanticTx, seq uint64) error {
			return sem.InsertObligationDeclaration(DeclarationOf(o, seq))
		}},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			seq := tx.NextSeq()
			noErr(t, tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "call-shared-"+tc.name, seq, domain.TargetCall, "c")))
			noErr(t, tc.write(semantic(t, tx), seq))
			return nil
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("%s sharing a TargetCall seq: error = %v, want ErrInvalidRecord", tc.name, err)
			continue // the write committed; its control would conflict
		}
		// With its own sequence number the same write is accepted.
		err = s.Update(ctx, sessA, func(tx store.Tx) error {
			noErr(t, tx.AppendLifecycleEvent(NewLifecycleEvent(sessA, "call-own-"+tc.name, tx.NextSeq(), domain.TargetCall, "c")))
			noErr(t, tc.write(semantic(t, tx), tx.NextSeq()))
			return errRollback
		})
		wantErr(t, err, errRollback)
	}
}
