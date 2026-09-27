package lifecycle

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_2_FailedAttemptThenValidRetry closes the P3-42 table row "failed
// attempt then valid retry (Phase 3 service level)": an uncommitted failed
// lifecycle attempt leaves no mutation receipt, so the same request ID
// retried with corrected arguments executes fresh; once committed, that
// request ID replays only exactly — conflicting arguments or a different
// method on the same family are refused with ErrEventIDConflict.
func TestP3_2_FailedAttemptThenValidRetry(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		goal := storetest.NewGoal("s", "goal", 0, "resolved goal")
		resolved := domain.GoalResolved
		goal.GoalStatus, goal.Residency = &resolved, domain.ResidencyArchived
		seedItem(t, db, goal)
		s, _ := New(db, testPolicy())
		p := storetest.NewPrincipal("s", domain.AuthorityUser)

		// The failed attempt (stale expected version) commits nothing.
		if _, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u1", ItemID: "goal", ExpectedVersion: 2}); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("failed attempt: %v", err)
		}
		readSemantic(t, db, func(sem store.SemanticReader) error {
			if _, err := sem.MutationReceipt(domain.MutationLifecycle, "u1"); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("failed attempt stored a receipt: %v", err)
			}
			return nil
		})
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			it, err := tx.Item("goal")
			if err != nil || it.Version != 1 || it.Residency != domain.ResidencyArchived {
				t.Fatalf("failed attempt changed the item: %+v %v", it, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// The same request ID retried with corrected arguments executes.
		out, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u1", ItemID: "goal", ExpectedVersion: 1})
		if err != nil || out.AfterVersion != 2 || out.After.Residency != domain.ResidencyResident {
			t.Fatalf("valid retry: %+v %v", out, err)
		}
		readSemantic(t, db, func(sem store.SemanticReader) error {
			r, err := sem.MutationReceipt(domain.MutationLifecycle, "u1")
			if err != nil || r.RequestID != "u1" || r.Principal != p || r.CanonicalMethod != string(domain.ActionUnarchive) {
				t.Fatalf("committed receipt: %+v %v", r, err)
			}
			return nil
		})

		// After commit the receipt is frozen: neither different arguments
		// nor a different method on the same family may reuse the request ID.
		if _, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u1", ItemID: "goal", ExpectedVersion: 9}); !errors.Is(err, domain.ErrEventIDConflict) {
			t.Fatalf("conflicting arguments: %v", err)
		}
		if _, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "u1", ItemID: "goal", ExpectedVersion: 2}); !errors.Is(err, domain.ErrEventIDConflict) {
			t.Fatalf("conflicting method: %v", err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			it, err := tx.Item("goal")
			if err != nil || it.Version != 2 || it.Residency != domain.ResidencyResident {
				t.Fatalf("conflicting retry re-executed: %+v %v", it, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestP3_2_ArchiveUnarchiveThenOldCollectRetryReplays closes the P3-42 table
// row "archive→unarchive→old Collect retry": after a committed Collect
// archives an item and the item is unarchived by a later request, retrying
// the old Collect request replays its frozen receipt exactly — same ID, same
// decisions, same archived refs — and does not re-execute against the now
// resident item.
func TestP3_2_ArchiveUnarchiveThenOldCollectRetryReplays(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		f := seedCollection(t, db)
		s, _ := New(db, testPolicy())
		harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
		intent := domain.CollectIntent{RequestID: "c1", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual}
		first, err := collect(f, db, s, harness, intent)
		if err != nil || first.Result.Collect == nil {
			t.Fatalf("first collect: %+v %v", first, err)
		}
		r := first.Result.Collect
		var archivedEph bool
		for _, ref := range r.ArchivedRefs {
			archivedEph = archivedEph || ref.ItemID == "eph"
		}
		if !archivedEph || len(r.ArchivedRefs) != 2 {
			t.Fatalf("first collect archived %v", r.ArchivedRefs)
		}

		// A later request unarchives the collected item.
		if _, err := s.UnarchiveStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.UnarchiveIntent{RequestID: "u", ItemID: "eph", ExpectedVersion: 2}); err != nil {
			t.Fatal(err)
		}

		// Retrying the old Collect request replays; it does not rescan.
		again, err := collect(f, db, s, harness, intent)
		if err != nil || again.Result.Collect == nil {
			t.Fatalf("collect retry: %+v %v", again, err)
		}
		replayed := again.Result.Collect
		if replayed.ID != r.ID || !slices.Equal(replayed.Decisions, r.Decisions) || !slices.Equal(replayed.ArchivedRefs, r.ArchivedRefs) {
			t.Fatalf("old collect retry diverged: %+v vs %+v", replayed, r)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			it, err := tx.Item("eph")
			if err != nil || it.Residency != domain.ResidencyResident || it.Version != 3 {
				t.Fatalf("replayed collect re-executed: %+v %v", it, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
