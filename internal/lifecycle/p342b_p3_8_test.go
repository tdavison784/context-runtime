package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_8_SystemInstructionStaysProtectedAfterUnpin closes the MISSING
// SYSTEM half of "Unpin preserves unresolved/blocked obligations and SYSTEM
// instruction status" (ADR 8 :1084). The obligation half is
// TestP3_33_UnpinWithUnresolvedObligation; the cited test uses a
// USER-authority item with no obligation. Items carry no mandatory flag —
// "stays mandatory as system policy" is observable only through what the
// runtime does with the item. On both stores: a SYSTEM pinned instruction can
// only be unpinned by SYSTEM (a USER attempt is an authority promotion, never
// a quieter status change), the unpin preserves the item's SYSTEM authority
// while moving PINNED -> DURABLE, and the unpinned instruction still
// discloses as a protected requirement when archived — where an identical
// USER instruction unpinned the same way does not.
func TestP3_8_SystemInstructionStaysProtectedAfterUnpin(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			sys := storetest.NewDirective("s", "dirsys", "d1", tx.NextSeq(), "system instruction")
			sys.Authority = domain.AuthoritySystem
			usr := storetest.NewDirective("s", "diruser", "d2", tx.NextSeq(), "user instruction")
			if err := tx.InsertItem(sys); err != nil {
				return err
			}
			if err := tx.InsertItem(usr); err != nil {
				return err
			}
			if err := storetest.UncheckedSetCurrentVersion(tx, "dirsys"); err != nil {
				return err
			}
			if err := storetest.UncheckedSetCurrentVersion(tx, "diruser"); err != nil {
				return err
			}
			_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		s, _ := New(db, testPolicy())
		user := storetest.NewPrincipal("s", domain.AuthorityUser)
		system := storetest.NewPrincipal("s", domain.AuthoritySystem)

		// A USER cannot unpin SYSTEM's instruction: authority, not access, is
		// what refuses.
		if _, err := s.UnpinStandalone(ctx, user, domain.UnpinIntent{RequestID: "u-user", ItemID: "dirsys", ExpectedVersion: 1}); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("USER unpinned SYSTEM instruction: %v", err)
		}

		// SYSTEM unpins it; the instruction keeps its authority and moves to
		// DURABLE.
		out, err := s.UnpinStandalone(ctx, system, domain.UnpinIntent{RequestID: "u-sys", ItemID: "dirsys", ExpectedVersion: 1})
		if err != nil || out.After.Generation != domain.GenerationDurable || out.After.Authority != domain.AuthoritySystem {
			t.Fatalf("SYSTEM unpin: %+v %v", out, err)
		}
		// The twin USER instruction unpins the same way for contrast.
		if _, err := s.UnpinStandalone(ctx, user, domain.UnpinIntent{RequestID: "u-usr", ItemID: "diruser", ExpectedVersion: 1}); err != nil {
			t.Fatalf("USER unpin of own instruction: %v", err)
		}

		// Both are now DURABLE instructions; archiving each discloses whether
		// it removes a protected requirement. Only the SYSTEM one does.
		sysArch, err := s.ArchiveStandalone(ctx, system, domain.ArchiveIntent{RequestID: "a-sys", ItemID: "dirsys", ExpectedVersion: 2})
		if err != nil {
			t.Fatalf("archive SYSTEM instruction: %v", err)
		}
		if !sysArch.ExplicitProtectedRemoval || sysArch.After.Authority != domain.AuthoritySystem || sysArch.After.Residency != domain.ResidencyArchived {
			t.Fatalf("SYSTEM instruction after unpin archived without protected-removal disclosure: %+v", sysArch)
		}
		usrArch, err := s.ArchiveStandalone(ctx, system, domain.ArchiveIntent{RequestID: "a-usr", ItemID: "diruser", ExpectedVersion: 2})
		if err != nil {
			t.Fatalf("archive USER instruction: %v", err)
		}
		if usrArch.ExplicitProtectedRemoval {
			t.Fatalf("unpinned USER instruction disclosed as protected requirement: %+v", usrArch)
		}
	})
}
