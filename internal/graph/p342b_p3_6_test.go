package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestP3_6_OverBudgetCoverageRejectsWithoutPartialWrites closes the P3-42
// table row "bounded limits reject without partial coverage" (ADR 8 :1076).
// The cited tests exercise planDerivedCoverage as a pure function; the
// MISSING half is the store after rejection. A LinkDerivedCoverage call whose
// source set exceeds the recorded bound must refuse with
// store.ErrLimitExceeded and leave NOTHING behind — no coverage record, no
// members, no CoveragesBySource index rows and no DERIVED_FROM edges — on
// both stores. The at-limit control on the same data proves the refusal came
// from the bound, not from the sources.
func TestP3_6_OverBudgetCoverageRejectsWithoutPartialWrites(t *testing.T) {
	const sess = "sess-p3-6-bound"
	actor := principal(sess, domain.AuthorityUser)
	sources := []string{"src-1", "src-2", "src-3", "src-4", "src-5"}

	seedSources := func(t *testing.T, s store.Store) {
		t.Helper()
		update(t, s, sess, func(tx store.Tx) error {
			for _, id := range sources {
				it := taskItem(sess, id, tx.NextSeq(), domain.AuthorityUser)
				it.Kind = domain.KindEvidence // user-supplied evidence qualifies as support (SEC-1.3)
				mustInsert(t, tx, it)
			}
			return nil
		})
	}

	// assertClean verifies the store holds no trace of a coverage set for
	// derivedID: no record under the deterministic ID, no member rows, no
	// per-source index entries and no DERIVED_FROM edges.
	assertClean := func(t *testing.T, s store.Store, derivedID, eventID string) {
		t.Helper()
		view(t, s, sess, func(tx store.ReadTx) error {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			wantID := deriveID("coverage", "context-runtime/graph/derived-coverage-id/v1", sess, derivedID, eventID, string(domain.CoverageEvidenceSupport))
			if _, err := r.Coverage(wantID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("rejected link still wrote its coverage record %s: %v", wantID, err)
			}
			if members, err := r.CoverageMembers(wantID, store.Page{Limit: 10}); err != nil || len(members.Records) != 0 {
				t.Errorf("rejected link left %d members (err %v), want none", len(members.Records), err)
			}
			for _, src := range sources {
				page, err := r.CoveragesBySource(src, domain.CoverageEvidenceSupport, store.Page{Limit: 10})
				if err != nil {
					return err
				}
				if len(page.Records) != 0 {
					t.Errorf("rejected link indexed coverage for source %s: %d records", src, len(page.Records))
				}
			}
			rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: derivedID})
			if err != nil {
				return err
			}
			if len(rels) != 0 {
				t.Errorf("rejected link left %d DERIVED_FROM edges, want none", len(rels))
			}
			return nil
		})
	}

	eachStore(t, func(t *testing.T, s store.Store) {
		seedSources(t, s)

		// Over-budget: five sources against a recorded bound of four. The
		// refusal must escape the update and the transaction must not commit
		// any part of the set.
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			d := taskItem(sess, "derived-over", tx.NextSeq(), domain.AuthorityUser)
			mustInsert(t, tx, d)
			_, err := LinkDerivedCoverage(tx, actor, d.ID, sources, domain.CoverageEvidenceSupport, "evt-over", 4)
			return err
		})
		if !errors.Is(err, store.ErrLimitExceeded) {
			t.Fatalf("over-budget LinkDerivedCoverage() error = %v, want store.ErrLimitExceeded", err)
		}
		assertClean(t, s, "derived-over", "evt-over")

		// Positive control: the identical source set at exactly the bound
		// commits the complete set — so the rejection above was the bound.
		update(t, s, sess, func(tx store.Tx) error {
			d := taskItem(sess, "derived-ok", tx.NextSeq(), domain.AuthorityUser)
			mustInsert(t, tx, d)
			rels, err := LinkDerivedCoverage(tx, actor, d.ID, sources, domain.CoverageEvidenceSupport, "evt-ok", len(sources))
			if err != nil {
				return err
			}
			if len(rels) != len(sources) {
				t.Fatalf("at-limit link wrote %d edges, want one per source", len(rels))
			}
			return nil
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			r, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			wantID := deriveID("coverage", "context-runtime/graph/derived-coverage-id/v1", sess, "derived-ok", "evt-ok", string(domain.CoverageEvidenceSupport))
			c, err := r.Coverage(wantID)
			if err != nil {
				return err
			}
			if c.MemberCount != uint64(len(sources)) {
				t.Errorf("at-limit coverage = %d members, want %d", c.MemberCount, len(sources))
			}
			for _, src := range sources {
				page, err := r.CoveragesBySource(src, domain.CoverageEvidenceSupport, store.Page{Limit: 10})
				if err != nil {
					return err
				}
				if len(page.Records) != 1 || page.Records[0].ID != c.ID {
					t.Errorf("at-limit CoveragesBySource(%s) = %d records, want the one set", src, len(page.Records))
				}
			}
			rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: "derived-ok"})
			if err != nil {
				return err
			}
			if len(rels) != len(sources) {
				t.Errorf("at-limit link wrote %d edges, want one per source", len(rels))
			}
			return nil
		})
	})
}
