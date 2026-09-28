package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_2_UnpinReplaySurvivesLaterChangesAndRestart closes the MISSING half
// of "retry after lifecycle changes and restart" (ADR 8 :1042).
// TestCommandReplayUsesOriginalGrantAndFrozenResult proves the replay
// contract only as a pure function over a stubbed reader, and the restart
// half (TestCompletionReplaysAcrossSQLiteRestart) exercises CompleteTask
// alone. Here a real Unpin runs through the service on real stores; an
// identical retry after further lifecycle changes — and, on SQLite, after
// closing and reopening the database — replays the frozen result without
// allocating a sequence, while the same request with changed arguments is an
// ErrEventIDConflict and a fresh request sees the item's current state.
func TestP3_2_UnpinReplaySurvivesLaterChangesAndRestart(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T, db store.Store) {
		t.Helper()
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			if err := tx.InsertItem(storetest.NewDirective("s", "dir", "d", tx.NextSeq(), "pinned instruction")); err != nil {
				return err
			}
			other := storetest.NewItem("s", "other", tx.NextSeq(), "plain fact")
			if err := tx.InsertItem(other); err != nil {
				return err
			}
			if err := storetest.UncheckedSetCurrentVersion(tx, "dir"); err != nil {
				return err
			}
			_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	lastSeq := func(t *testing.T, db store.Store) uint64 {
		t.Helper()
		var n uint64
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			n = tx.LastSeq()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	unpin := func(id, req string, version uint64) domain.UnpinIntent {
		return domain.UnpinIntent{RequestID: req, ItemID: id, ExpectedVersion: version}
	}
	user := storetest.NewPrincipal("s", domain.AuthorityUser)

	// Later lifecycle changes intervene, then the identical retry replays the
	// frozen unpin on both stores.
	eachStore(t, func(t *testing.T, db store.Store) {
		seed(t, db)
		s, _ := New(db, testPolicy())
		first, err := s.UnpinStandalone(ctx, user, unpin("dir", "u1", 1))
		if err != nil || first.After.Generation != domain.GenerationDurable || first.After.Version != 2 {
			t.Fatalf("unpin: %+v %v", first, err)
		}
		// Two further lifecycle changes, one on the same item, one elsewhere.
		if _, err := s.ArchiveStandalone(ctx, user, domain.ArchiveIntent{RequestID: "a1", ItemID: "dir", ExpectedVersion: 2}); err != nil {
			t.Fatalf("archive the unpinned source: %v", err)
		}
		if _, err := s.ArchiveStandalone(ctx, user, domain.ArchiveIntent{RequestID: "a2", ItemID: "other", ExpectedVersion: 1}); err != nil {
			t.Fatalf("archive another item: %v", err)
		}
		before := lastSeq(t, db)

		again, err := s.UnpinStandalone(ctx, user, unpin("dir", "u1", 1))
		if err != nil {
			t.Fatalf("replay after later changes: %v", err)
		}
		if again.AuditID != first.AuditID || again.Before.Generation != domain.GenerationPinned || again.After.Generation != domain.GenerationDurable ||
			again.Before.Version != 1 || again.After.Version != 2 {
			t.Fatalf("replay was not the frozen result:\nfirst %+v\nagain  %+v", first, again)
		}
		if after := lastSeq(t, db); after != before {
			t.Fatalf("replay allocated sequences: %d -> %d", before, after)
		}

		// The same request with changed arguments conflicts; a fresh request
		// meets the item's current state instead.
		if _, err := s.UnpinStandalone(ctx, user, unpin("dir", "u1", 2)); !errors.Is(err, domain.ErrEventIDConflict) {
			t.Fatalf("changed replay: %v", err)
		}
		if _, err := s.UnpinStandalone(ctx, user, unpin("dir", "u2", 1)); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("fresh request on moved-on state: %v", err)
		}
	})

	// The same unpin replays identically after SQLite is closed and reopened.
	t.Run("sqlite restart", func(t *testing.T) {
		path := sqlitetest.Path(t)
		db, err := sqlite.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		seed(t, db)
		s, _ := New(db, testPolicy())
		first, err := s.UnpinStandalone(ctx, user, unpin("dir", "u1", 1))
		if err != nil {
			t.Fatalf("unpin: %v", err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := sqlite.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reopened.Close() })
		s2, _ := New(reopened, testPolicy())

		again, err := s2.UnpinStandalone(ctx, user, unpin("dir", "u1", 1))
		if err != nil {
			t.Fatalf("replay after restart: %v", err)
		}
		if again.AuditID != first.AuditID || again.After.Generation != domain.GenerationDurable || again.After.Version != 2 || again.Before.Version != 1 {
			t.Fatalf("replay after restart was not the frozen result:\nfirst %+v\nagain  %+v", first, again)
		}
		if _, err := s2.UnpinStandalone(ctx, user, unpin("dir", "u1", 2)); !errors.Is(err, domain.ErrEventIDConflict) {
			t.Fatalf("changed replay after restart: %v", err)
		}
		if _, err := s2.UnpinStandalone(ctx, user, unpin("dir", "u3", 1)); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("fresh request after restart: %v", err)
		}
	})
}
