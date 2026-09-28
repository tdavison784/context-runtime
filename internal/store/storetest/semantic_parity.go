package storetest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// testSemanticIndexedVersions checks DUR-1.13(a): the subject and task-owner
// indexes name exact obligation versions, so while two versions of one
// obligation are current each read returns each version once, never the
// latest twice.
func testSemanticIndexedVersions(t *testing.T, s store.Store) {
	proofWorld(t, s)
	update(t, s, sessA, func(tx store.Tx) error {
		return tx.InsertObligationVersion(BoundObligation(t, sessA, "o1", 2, tx.NextSeq(), "src"))
	})
	versions := func(pg store.ResultPage[domain.ObligationVersion]) []uint64 {
		var out []uint64
		for _, o := range pg.Records {
			out = append(out, o.Version)
		}
		return out
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		bound, err := r.CurrentBoundObligationsBySubject(BoundObligation(t, sessA, "o1", 1, 1, "src").TargetSubjectKey, store.Page{Limit: 5})
		noErr(t, err)
		assertEqual(t, "CurrentBoundObligationsBySubject versions", versions(bound), []uint64{1, 2})
		owned, err := r.ObligationsByTaskOwner("task", store.Page{Limit: 5})
		noErr(t, err)
		assertEqual(t, "ObligationsByTaskOwner versions", versions(owned), []uint64{1, 2})
		return nil
	})
}

// testSemanticWorkspaceBindingCursor checks DUR-1.13(b): a
// WorkspaceBindingsByContext page resumes from the documented (Seq, ID) of
// its last record, so two versions of one binding page as v1 then v2.
func testSemanticWorkspaceBindingCursor(t *testing.T, s store.Store) {
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertResourceBinding(NewResourceBinding(sessA, "repo", tx.NextSeq())))
		noErr(t, sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 1, tx.NextSeq())))
		return sem.InsertWorkspaceBinding(NewWorkspaceBinding(sessA, "wb", "repo", 2, tx.NextSeq()))
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		first, err := r.WorkspaceBindingsByContext("", "task", "", store.Page{Limit: 1})
		noErr(t, err)
		if len(first.Records) != 1 || first.Records[0].Version != 1 || !first.More {
			t.Fatalf("first page = %+v", first)
		}
		last := first.Records[0]
		if want := (store.Cursor{Seq: last.Seq, ID: last.ID}); first.Next != want {
			t.Errorf("Next = %+v, want the last record's (Seq, ID) %+v", first.Next, want)
		}
		second, err := r.WorkspaceBindingsByContext("", "task", "", store.Page{Limit: 1, After: store.Cursor{Seq: last.Seq, ID: last.ID}})
		noErr(t, err)
		if len(second.Records) != 1 || second.Records[0].Version != 2 {
			t.Errorf("page after (Seq, ID) of v1 = %+v, want v2", second.Records)
		}
		return nil
	})
}

// testSemanticOwnerIDReuse checks DUR-1.13(c): a registration ID is
// immutable; reusing it for another owner is ErrImmutable in both stores.
func testSemanticOwnerIDReuse(t *testing.T, s store.Store) {
	reg := func(id, owner string, seq uint64) domain.OwnerRegistration {
		return domain.OwnerRegistration{SemanticMeta: Meta(sessA, id, seq), Kind: domain.OwnerWorkflow, OwnerID: owner, SourceID: "evt", Actor: HarnessPrincipal(sessA)}
	}
	update(t, s, sessA, func(tx store.Tx) error {
		return semantic(t, tx).InsertOwnerRegistration(reg("owner-1", "wf-1", tx.NextSeq()))
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		return semantic(t, tx).InsertOwnerRegistration(reg("owner-1", "wf-2", tx.NextSeq()))
	})
	if !errors.Is(err, domain.ErrImmutable) {
		t.Errorf("reused registration ID: error = %v, want ErrImmutable", err)
	}
}
