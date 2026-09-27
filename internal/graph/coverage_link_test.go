package graph

import (
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestDerivedCoverageWritesOneSetAndLinearEdges runs on real stores, which
// refuse an edge whose CoverageID names coverage they do not hold (P3-6).
func TestDerivedCoverageWritesOneSetAndLinearEdges(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		update(t, s, "s", func(tx store.Tx) error {
			a := taskItem("s", "a", tx.NextSeq(), domain.AuthorityUser)
			b := taskItem("s", "b", tx.NextSeq(), domain.AuthorityUser)
			d := taskItem("s", "d", tx.NextSeq(), domain.AuthorityUser)
			a.Kind, b.Kind = domain.KindEvidence, domain.KindEvidence // user-supplied evidence qualifies as support (SEC-1.3)
			mustInsert(t, tx, a, b, d)
			rels, err := LinkDerivedCoverage(tx, principal("s", domain.AuthorityUser), d.ID, []string{b.ID, a.ID}, domain.CoverageEvidenceSupport, "event", 2)
			if err != nil {
				return err
			}
			if len(rels) != 2 {
				t.Fatalf("%d edges, want one per source", len(rels))
			}
			for _, rel := range rels {
				if rel.Coverage != nil || rel.CoverageID == "" || rel.CoverageID != rels[0].CoverageID {
					t.Fatal("legacy inline coverage escaped or coverage was copied per edge")
				}
			}
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			c, err := r.Coverage(rels[0].CoverageID)
			if err != nil {
				return err
			}
			if c.Purpose != domain.CoverageEvidenceSupport || c.MemberCount != 2 {
				t.Errorf("coverage = %+v, want EVIDENCE_SUPPORT with 2 members", c)
			}
			members, err := r.CoverageMembers(c.ID, store.Page{Limit: 10})
			if err != nil {
				return err
			}
			var ids []string
			for _, m := range members.Records {
				key, err := m.Key()
				if err != nil || m.ID != key || m.CoverageID != c.ID || m.Source == nil {
					t.Fatalf("member %+v: ID must equal its canonical key within its coverage", m)
				}
				ids = append(ids, m.Source.ItemID)
			}
			if members.More || !slices.Equal(ids, []string{a.ID, b.ID}) {
				t.Errorf("members = %v, want sorted unique [a b]", ids)
			}
			for _, src := range []string{a.ID, b.ID} {
				page, err := r.CoveragesBySource(src, domain.CoverageEvidenceSupport, store.Page{Limit: 10})
				if err != nil {
					return err
				}
				if len(page.Records) != 1 || page.Records[0].ID != c.ID {
					t.Errorf("CoveragesBySource(%s) = %d records, want the one shared set", src, len(page.Records))
				}
			}
			return nil
		})
	})
}
