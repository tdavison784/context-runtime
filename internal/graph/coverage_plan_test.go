package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"testing"
)

func TestCoveragePlanIsCompleteBoundedAndPurposeSpecific(t *testing.T) {
	s := memory.New()
	defer s.Close()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		actor := principal("s", domain.AuthorityUser)
		a := taskItem("s", "a", tx.NextSeq(), domain.AuthorityUser)
		b := taskItem("s", "b", tx.NextSeq(), domain.AuthorityUser)
		d := taskItem("s", "d", tx.NextSeq(), domain.AuthorityUser)
		a.Kind, b.Kind = domain.KindEvidence, domain.KindEvidence // user-supplied evidence qualifies as support (SEC-1.3)
		mustInsert(t, tx, a, b, d)
		p, err := planDerivedCoverage(tx, actor, d.ID, []string{b.ID, a.ID}, domain.CoverageProvenance, "event", 2)
		if err != nil {
			return err
		}
		if len(p.members) != 2 || p.coverage.MemberCount != 2 || p.members[0].CoverageID != p.members[1].CoverageID {
			t.Fatal("coverage is not normalized")
		}
		q, err := planDerivedCoverage(tx, actor, d.ID, []string{a.ID, b.ID}, domain.CoverageEvidenceSupport, "event", 2)
		if err != nil || p.coverage.ID == q.coverage.ID || p.coverage.Signature == q.coverage.Signature {
			t.Fatal("coverage purposes alias", err)
		}
		if _, err := planDerivedCoverage(tx, actor, d.ID, []string{a.ID, b.ID}, domain.CoverageProvenance, "event", 1); err == nil {
			t.Fatal("coverage truncated instead of rejecting")
		}
		if _, err := planDerivedCoverage(tx, actor, d.ID, []string{a.ID, a.ID}, domain.CoverageProvenance, "event", 2); err == nil {
			t.Fatal("duplicate members accepted")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
