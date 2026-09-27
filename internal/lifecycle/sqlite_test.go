package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// The memory-backed suites cover each rule; these run the executors end to
// end on the durable SQLite backend, including reopen-free replay.
func TestLifecycleEndToEndOnSQLite(t *testing.T) {
	ctx := context.Background()
	user, harness := storetest.NewPrincipal("s", domain.AuthorityUser), storetest.NewPrincipal("s", domain.AuthorityHarness)

	t.Run("completion enqueues GC collected once", func(t *testing.T) {
		db := sqlitetest.Open(t)
		s, _ := New(db, testPolicy())
		seedCompletion(t, db, []string{"g1"}, "", false)
		scratch := storetest.NewItem("s", "scratch", 0, "scratch")
		scratch.Scope, scratch.Access, scratch.Generation = domain.ScopeTask, storetest.DirectiveBoundary("s"), domain.GenerationEphemeral
		seedItem(t, db, scratch)
		intent := domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}
		first, err := s.CompleteTaskStandalone(ctx, user, intent)
		if err != nil || len(first.ResolvedGoals) != 1 {
			t.Fatalf("completion: %+v %v", first, err)
		}
		if again, err := s.CompleteTaskStandalone(ctx, user, intent); err != nil || again.AuditID != first.AuditID {
			t.Fatalf("replay: %+v %v", again, err)
		}
		pick := func(domain.GCRequest) (domain.Principal, bool) { return harness, true }
		for pass, want := range []int{1, 0} {
			if n, err := s.CollectPending(ctx, "s", pick, 4); n != want || err != nil {
				t.Fatalf("pass %d: %d %v", pass, n, err)
			}
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			it, err := tx.Item("scratch")
			if err != nil || it.Residency != domain.ResidencyArchived {
				t.Fatalf("scratch: %+v %v", it, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("grant archive revoke", func(t *testing.T) {
		db := sqlitetest.Open(t)
		s, _ := New(db, testPolicy())
		sys := storetest.NewItem("s", "sys", 0, "system fact")
		sys.Authority = domain.AuthoritySystem
		seedItem(t, db, sys)
		system := storetest.NewPrincipal("s", domain.AuthoritySystem)
		if _, err := s.IssueGrantStandalone(ctx, system, archiveGrant("g", "grant", "sys", harness)); err != nil {
			t.Fatal(err)
		}
		r, err := s.ArchiveStandalone(ctx, harness, domain.ArchiveIntent{RequestID: "a", ItemID: "sys", ExpectedVersion: 1})
		if err != nil || r.After.Residency != domain.ResidencyArchived {
			t.Fatalf("archive: %+v %v", r, err)
		}
		if _, err := s.RevokeGrantStandalone(ctx, system, domain.RevokeGrantIntent{RequestID: "rv", GrantID: "grant"}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.UnarchiveStandalone(ctx, harness, domain.UnarchiveIntent{RequestID: "u", ItemID: "sys", ExpectedVersion: 2}); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("revoked-grant actor unarchived: %v", err)
		}
		if again, err := s.ArchiveStandalone(ctx, harness, domain.ArchiveIntent{RequestID: "a", ItemID: "sys", ExpectedVersion: 1}); err != nil || again.AuditID != r.AuditID {
			t.Fatalf("archive replay after revocation: %+v %v", again, err)
		}
	})

	t.Run("replace directive", func(t *testing.T) {
		db := sqlitetest.Open(t)
		s, _ := New(db, testPolicy())
		seedDirective(t, db, domain.AuthorityUser, false)
		out, err := s.ReplaceDirectiveStandalone(ctx, user, replaceIntent("r", 1, "ship the release"))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			for id, want := range map[string]bool{out.Result.Records.IDs[0]: true, "prior": false} {
				if current, err := graph.IsCurrent(tx, id); err != nil || current != want {
					t.Fatalf("%s current=%v %v", id, current, err)
				}
			}
			o, err := tx.Obligation("o")
			if err != nil || o.Current {
				t.Fatalf("obligation not retired: %+v %v", o, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if again, err := s.ReplaceDirectiveStandalone(ctx, user, replaceIntent("r", 1, "ship the release")); err != nil || again.MutationReceiptID != out.MutationReceiptID {
			t.Fatalf("replay: %+v %v", again, err)
		}
	})
}
