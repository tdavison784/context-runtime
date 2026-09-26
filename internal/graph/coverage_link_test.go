package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"testing"
)

type coverageLinkTx struct {
	store.Tx
	backend *coverageLinkBackend
}
type coverageLinkBackend struct {
	store.SemanticTx
	inserts  int
	coverage domain.CoverageRecord
	members  []domain.CoverageMember
}

func (t *coverageLinkTx) SemanticTransaction() (store.SemanticTx, error) { return t.backend, nil }
func (b *coverageLinkBackend) InsertCoverage(c domain.CoverageRecord, members []domain.CoverageMember) error {
	b.inserts++
	b.coverage, b.members = c.Clone(), members
	return c.Validate()
}

func TestDerivedCoverageWritesOneSetAndLinearEdges(t *testing.T) {
	s := memory.New()
	defer s.Close()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		a := taskItem("s", "a", tx.NextSeq(), domain.AuthorityUser)
		b := taskItem("s", "b", tx.NextSeq(), domain.AuthorityUser)
		d := taskItem("s", "d", tx.NextSeq(), domain.AuthorityUser)
		mustInsert(t, tx, a, b, d)
		backend := &coverageLinkBackend{}
		wrapped := &coverageLinkTx{Tx: tx, backend: backend}
		rels, err := LinkDerivedCoverage(wrapped, principal("s", domain.AuthorityUser), d.ID, []string{b.ID, a.ID}, domain.CoverageEvidenceSupport, "event", 2)
		if err != nil {
			return err
		}
		if len(rels) != 2 || backend.inserts != 1 || len(backend.members) != 2 {
			t.Fatal("coverage was copied per edge")
		}
		for _, rel := range rels {
			if rel.Coverage != nil || rel.CoverageID != backend.coverage.ID {
				t.Fatal("legacy inline coverage escaped")
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
