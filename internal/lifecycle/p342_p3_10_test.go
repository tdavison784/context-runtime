package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_10_ExpiredOriginUnchangedByPromotion: no generation operation
// refreshes TTL or touches the origin. An item whose one-turn TTL has been
// passed by its originating task's turn still promotes WORKING→DURABLE —
// the closed table does not consult lifetime — and the promotion leaves the
// TTL, created turn, scope and owning task byte-identical, so the origin is
// exactly as expired after the promotion as before it.
func TestP3_10_ExpiredOriginUnchangedByPromotion(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		ctx := context.Background()
		s, _ := New(db, testPolicy())
		one := 1
		it := storetest.NewItem("s", "aging", 0, "turn-scoped fact")
		it.Generation = domain.GenerationWorking
		it.TTLTurns, it.CreatedTurn = &one, 1
		seedItem(t, db, it)
		// The originating task has moved four turns past the one-turn TTL.
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			task := storetest.NewTask("s", "task")
			task.Turn, task.TurnID = 5, "turn-5"
			_, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		ttlExpired := func(tx store.ReadTx) bool {
			task, err := tx.Task("task")
			if err != nil {
				t.Fatal(err)
			}
			return !domain.TTLLive(1, task.Turn, 1)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			if !ttlExpired(tx) {
				t.Fatal("setup: the item's origin is not expired")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		res, err := s.PromoteStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser),
			domain.PromoteIntent{ItemMutationIntent: domain.ItemMutationIntent{RequestID: "r", ItemID: "aging", ExpectedVersion: 1}, Generation: domain.GenerationDurable})
		if err != nil || res.After.Generation != domain.GenerationDurable {
			t.Fatalf("promotion of an expired-origin item: %+v %v", res, err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			got, err := tx.Item("aging")
			if err != nil {
				return err
			}
			if got.TTLTurns == nil || *got.TTLTurns != 1 || got.CreatedTurn != 1 {
				t.Fatalf("promotion refreshed the origin: TTL %v created turn %d", got.TTLTurns, got.CreatedTurn)
			}
			if got.Scope != it.Scope || got.TaskID != it.TaskID || got.Authority != it.Authority || got.Access != it.Access {
				t.Fatalf("promotion changed scope/task/authority/access: %+v", got)
			}
			if !ttlExpired(tx) {
				t.Fatal("promotion revived the expired origin")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestP3_10_PromoteReplayAndCASConflict: an identical Promote retry replays
// its frozen receipt without allocating a sequence or re-reading state, a
// stale ExpectedVersion against the promoted item is a CAS conflict that
// changes nothing, and a distinct later request is judged against the new
// revision.
func TestP3_10_PromoteReplayAndCASConflict(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		ctx := context.Background()
		s, _ := New(db, testPolicy())
		seedItem(t, db, storetest.NewItem("s", "item", 0, "content"))
		user := storetest.NewPrincipal("s", domain.AuthorityUser)
		intent := domain.PromoteIntent{ItemMutationIntent: domain.ItemMutationIntent{RequestID: "r", ItemID: "item", ExpectedVersion: 1}, Generation: domain.GenerationDurable}
		out, err := s.PromoteStandalone(ctx, user, intent)
		if err != nil || out.After.Generation != domain.GenerationDurable || out.After.Version != 2 {
			t.Fatalf("promotion: %+v %v", out, err)
		}
		var last uint64
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			last = tx.LastSeq()
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// The identical retry replays the frozen receipt, allocating nothing.
		replay, err := s.PromoteStandalone(ctx, user, intent)
		if err != nil || replay.AuditID != out.AuditID || replay.After != out.After {
			t.Fatalf("replay: %+v %v (want audit %s)", replay, err, out.AuditID)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			if tx.LastSeq() != last {
				t.Fatal("replay allocated a sequence")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		// A distinct request against the promoted revision's stale
		// ExpectedVersion is a CAS conflict and commits nothing.
		stale := domain.PromoteIntent{ItemMutationIntent: domain.ItemMutationIntent{RequestID: "r2", ItemID: "item", ExpectedVersion: 1}, Generation: domain.GenerationPinned}
		if _, err := s.PromoteStandalone(ctx, user, stale); !errors.Is(err, domain.ErrVersionConflict) {
			t.Fatalf("stale CAS: %v", err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			got, err := tx.Item("item")
			if err != nil || got.Version != 2 || got.Generation != domain.GenerationDurable || got.Retention != domain.RetentionHigh {
				t.Fatalf("failed CAS left effects: %+v %v", got, err)
			}
			if tx.LastSeq() != last {
				t.Fatal("failed CAS allocated a sequence")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
