package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_33_ArchiveUnarchivePreserveStatusAndProof closes the P3-42 table
// row "archive does not alter status/proof" (ADR8:1258). The cited test
// never checked the obligation side and ran on memory only; this one seeds
// a real satisfied obligation with a stored proof on its source item, then
// archive and unarchive that source on both stores: the obligation version
// (current, SATISFIED, revision, proof pointer) and the stored proof itself
// are untouched before, between and after both mutations, and the
// unarchive request replays frozen — including on SQLite.
func TestP3_33_ArchiveUnarchivePreserveStatusAndProof(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		seedItem(t, db, storetest.NewItem("s", "source", 0, "obligation source"))
		// A RESOLVED goal beside it pins the item-side half of the clause:
		// residency flips must never move goal status (FR-DOM-005).
		resolvedGoal := storetest.NewGoal("s", "rg", 0, "done")
		done := domain.GoalResolved
		resolvedGoal.GoalStatus = &done
		seedItem(t, db, resolvedGoal)
		proofID := assertSatisfied(t, db, "source")
		// The satisfying transition itself bumps the version's revision;
		// archive/unarchive must not move it from that seeded value.
		var seeded uint64
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			sem, err := store.ReadSemantic(tx)
			if err != nil {
				return err
			}
			o, err := sem.ExactObligation(domain.ObligationRef{SessionID: "s", ObligationID: "o", Version: 1})
			if err != nil {
				return err
			}
			seeded = o.Revision
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		check := func(stage string) {
			t.Helper()
			if err := db.View(ctx, "s", func(tx store.ReadTx) error {
				sem, err := store.ReadSemantic(tx)
				if err != nil {
					return err
				}
				o, err := sem.ExactObligation(domain.ObligationRef{SessionID: "s", ObligationID: "o", Version: 1})
				if err != nil || !o.Current || o.Status != domain.ObligationSatisfied || o.CurrentProofID != proofID || o.Revision != seeded || o.SourceItemID != "source" {
					t.Fatalf("%s changed the obligation: %+v %v", stage, o, err)
				}
				p, err := sem.ApplicabilityProof(proofID)
				if err != nil || p.TransitionID != "tr-o" || p.Target.ObligationID != "o" {
					t.Fatalf("%s changed the proof: %+v %v", stage, p, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		check("baseline")
		s, _ := New(db, testPolicy())
		user := storetest.NewPrincipal("s", domain.AuthorityUser)
		goalCheck := func(stage string) {
			t.Helper()
			if err := db.View(ctx, "s", func(tx store.ReadTx) error {
				it, err := tx.Item("rg")
				if err != nil || it.GoalStatus == nil || *it.GoalStatus != domain.GoalResolved {
					t.Fatalf("%s changed the goal status: %+v %v", stage, it, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		goalCheck("goal baseline")
		if _, err := s.ArchiveStandalone(ctx, user, domain.ArchiveIntent{RequestID: "a33s", ItemID: "source", ExpectedVersion: 1}); err != nil {
			t.Fatalf("archive: %v", err)
		}
		if _, err := s.ArchiveStandalone(ctx, user, domain.ArchiveIntent{RequestID: "a33g", ItemID: "rg", ExpectedVersion: 1}); err != nil {
			t.Fatalf("goal archive: %v", err)
		}
		check("after archive")
		goalCheck("after goal archive")
		first, err := s.UnarchiveStandalone(ctx, user, domain.UnarchiveIntent{RequestID: "u33", ItemID: "source", ExpectedVersion: 2})
		if err != nil || first.After.Residency != domain.ResidencyResident || first.AfterVersion != 3 {
			t.Fatalf("unarchive: %+v %v", first, err)
		}
		check("after unarchive")
		if _, err := s.UnarchiveStandalone(ctx, user, domain.UnarchiveIntent{RequestID: "u33g", ItemID: "rg", ExpectedVersion: 2}); err != nil {
			t.Fatalf("goal unarchive: %v", err)
		}
		goalCheck("after goal unarchive")
		// The unarchive request replays frozen — same audit, same versions,
		// no further item change — on both stores.
		again, err := s.UnarchiveStandalone(ctx, user, domain.UnarchiveIntent{RequestID: "u33", ItemID: "source", ExpectedVersion: 2})
		if err != nil || again.AuditID != first.AuditID || again.AfterVersion != first.AfterVersion {
			t.Fatalf("replay: %+v %v, want audit %s version %d", again, err, first.AuditID, first.AfterVersion)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			it, err := tx.Item("source")
			if err != nil || it.Version != 3 || it.Residency != domain.ResidencyResident {
				t.Fatalf("replay mutated the item: %+v %v", it, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		check("after replay")
	})
}
