package graph

import (
	"errors"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

// TestP3_6_CoverageReconstructionAfterRestart closes the P3-42 table row
// "complete reconstruction after restart": the normalized companion
// coverage record plus its members persist once, and the whole set —
// record, every member in its canonical order, every edge's CoverageID and
// the signature — reconstructs identically from a reopened store (both
// backends; the restart itself is the SQLite reopen).
func TestP3_6_CoverageReconstructionAfterRestart(t *testing.T) {
	wantSources := []string{"src-a", "src-b", "src-c", "src-d", "src-e"}
	// seedCoverage files five evidence sources and one derived item, then
	// writes one shared EVIDENCE_SUPPORT coverage set over them.
	seedCoverage := func(t *testing.T, s store.Store) (coverageID, derivedID string) {
		t.Helper()
		update(t, s, "s", func(tx store.Tx) error {
			var sources []string
			for _, id := range wantSources {
				it := taskItem("s", id, tx.NextSeq(), domain.AuthorityUser)
				it.Kind = domain.KindEvidence // user-supplied evidence qualifies as support (SEC-1.3)
				mustInsert(t, tx, it)
				sources = append(sources, it.ID)
			}
			d := taskItem("s", "derived", tx.NextSeq(), domain.AuthorityUser)
			mustInsert(t, tx, d)
			rels, err := LinkDerivedCoverage(tx, principal("s", domain.AuthorityUser), d.ID, sources, domain.CoverageEvidenceSupport, "event", 8)
			if err != nil {
				return err
			}
			coverageID = rels[0].CoverageID
			derivedID = d.ID
			return nil
		})
		return coverageID, derivedID
	}
	// verify reconstructs the complete set from the store it is handed and
	// returns the members' canonical order for restart comparison.
	verify := func(t *testing.T, s store.Store, coverageID, derivedID string) []string {
		t.Helper()
		var order []string
		view(t, s, "s", func(tx store.ReadTx) error {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			c, err := r.Coverage(coverageID)
			if err != nil {
				return err
			}
			if c.Purpose != domain.CoverageEvidenceSupport || c.MemberCount != 5 {
				t.Errorf("coverage = %+v, want EVIDENCE_SUPPORT over 5 members", c)
			}
			members, err := r.CoverageMembers(c.ID, store.Page{Limit: 6})
			if err != nil {
				return err
			}
			var ids []string
			for _, m := range members.Records {
				key, keyErr := m.Key()
				if keyErr != nil || m.ID != key || m.CoverageID != c.ID || m.Source == nil {
					t.Fatalf("member %+v: canonical identity lost", m)
				}
				order = append(order, m.ID)
				ids = append(ids, m.Source.ItemID)
			}
			if members.More || len(ids) != len(wantSources) {
				t.Fatalf("members = %v more=%v, want the complete set of %d", ids, members.More, len(wantSources))
			}
			slices.Sort(ids)
			if !slices.Equal(ids, wantSources) {
				t.Errorf("members = %v, want exactly %v", ids, wantSources)
			}
			if !slices.IsSorted(order) {
				t.Errorf("members are not in canonical key order: %v", order)
			}
			if sig, err := domain.CoverageSignature(c, members.Records); err != nil || sig != c.Signature {
				t.Errorf("recomputed signature %q (err %v) != stored %q", sig, err, c.Signature)
			}
			rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: derivedID})
			if err != nil {
				return err
			}
			if len(rels) != len(wantSources) {
				t.Fatalf("%d edges, want one per source", len(rels))
			}
			for _, rel := range rels {
				if rel.CoverageID != c.ID {
					t.Errorf("edge %+v lost its coverage reference", rel)
				}
			}
			for _, src := range wantSources {
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
		return order
	}
	eachStore(t, func(t *testing.T, s store.Store) {
		coverageID, derivedID := seedCoverage(t, s)
		verify(t, s, coverageID, derivedID)
	})
	// The restart proper: the same SQLite database closed and reopened must
	// reconstruct the identical complete set — same members, same canonical
	// order, same signature — without any rewrite.
	path := sqlitetest.Path(t)
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	coverageID, derivedID := seedCoverage(t, db)
	before := verify(t, db, coverageID, derivedID)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	after := verify(t, reopened, coverageID, derivedID)
	if !slices.Equal(before, after) {
		t.Fatalf("canonical member order changed across restart: %v -> %v", before, after)
	}
	if err := reopened.View(ctx, "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		if _, err := r.Coverage("coverage-missing"); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("unknown coverage: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
