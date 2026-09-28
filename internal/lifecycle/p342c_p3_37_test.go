package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// P3-37 (ADR 8 line 1440). The cited TestUnarchiveRestoresResidencyOnlyAnd-
// Replays (archive_test.go:110) runs on memory.New() only. This is the
// SQLite half through eachStore, on memory and sqlitetest: unarchiving an
// archived RESOLVED goal with a stale ExpectedVersion is a plain
// ErrVersionConflict; the correct revision restores residency ONLY (the
// resolved status and content are untouched); archiving the archived item
// is refused; and replaying the first unarchive after a later archive is
// idempotent — same AfterVersion and AuditID, no new write, content and
// state byte-identical.
func TestP3_37_StaleRevisionAndIdempotentUnarchiveReplayOnBothStores(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		goal := storetest.NewGoal("s", "goal", 0, "resolved goal")
		resolved := domain.GoalResolved
		goal.GoalStatus, goal.Residency = &resolved, domain.ResidencyArchived
		seedItem(t, db, goal)
		p := storetest.NewPrincipal("s", domain.AuthorityUser)

		// Archive of the already-archived item is refused.
		if _, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "a", ItemID: "goal", ExpectedVersion: 1}); !errors.Is(err, graph.ErrLifecycleTargetMismatch) {
			t.Fatalf("archived an archived item: %v", err)
		}
		// The row's clause: a stale ExpectedVersion is a plain version
		// conflict, not a residency or authority error.
		if _, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u", ItemID: "goal", ExpectedVersion: 2}); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("stale revision: %v", err)
		}
		first, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u", ItemID: "goal", ExpectedVersion: 1})
		if err != nil || first.After.Residency != domain.ResidencyResident || first.After.GoalStatus == nil || *first.After.GoalStatus != domain.GoalResolved {
			t.Fatalf("unarchive: %+v %v", first, err)
		}
		if _, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "a2", ItemID: "goal", ExpectedVersion: 2}); err != nil {
			t.Fatal(err)
		}
		var before uint64
		if err := db.View(ctx, "s", func(tx store.ReadTx) error { before = tx.LastSeq(); return nil }); err != nil {
			t.Fatal(err)
		}
		again, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u", ItemID: "goal", ExpectedVersion: 1})
		if err != nil || again.AfterVersion != first.AfterVersion || again.AuditID != first.AuditID {
			t.Fatalf("replay after later archive: %+v %v", again, err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			if tx.LastSeq() != before {
				t.Fatalf("replay allocated a sequence: %d -> %d", before, tx.LastSeq())
			}
			it, err := tx.Item("goal")
			if err != nil || it.Version != 3 || it.Residency != domain.ResidencyArchived || it.ContentHash != goal.ContentHash || len(it.Parts) != 1 {
				t.Fatalf("content or state changed: %+v %v", it, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
