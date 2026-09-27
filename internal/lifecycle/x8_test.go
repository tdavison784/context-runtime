package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func eachStore(t *testing.T, run func(*testing.T, store.Store)) {
	t.Run("memory", func(t *testing.T) {
		mem := memory.New()
		t.Cleanup(func() { mem.Close() })
		run(t, mem)
	})
	t.Run("sqlite", func(t *testing.T) { run(t, sqlitetest.Open(t)) })
}

// X8: completion rejects reserving calls and open exchanges of the task only
// after authority and obligation checks, and commits nothing.
func TestCompletionRejectsInFlightWorkOnRealStores(t *testing.T) {
	ctx := context.Background()
	conv := domain.ConversationIDFor("task", "agent")
	for name, tc := range map[string]struct {
		obligation bool
		seed       func(store.Tx) error
		want       error
	}{
		"prepared call": {seed: func(tx store.Tx) error {
			if _, err := tx.PutConversation(storetest.NewConversation("s", conv), 0); err != nil {
				return err
			}
			return tx.InsertCall(storetest.NewCall("s", "call", conv, tx.NextSeq()))
		}, want: domain.ErrCallInFlight},
		"open exchange": {seed: func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			return sem.InsertLogicalExchange(storetest.NewExchange("s", "x", "task", "agent", 1, tx.NextSeq()))
		}, want: domain.ErrCallInFlight},
		"obligations first": {obligation: true, seed: func(tx store.Tx) error {
			if _, err := tx.PutConversation(storetest.NewConversation("s", conv), 0); err != nil {
				return err
			}
			return tx.InsertCall(storetest.NewCall("s", "call", conv, tx.NextSeq()))
		}, want: domain.ErrUnfinishedObligations},
	} {
		t.Run(name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				seedCompletion(t, db, []string{"g1"}, "", tc.obligation)
				if err := db.Update(ctx, "s", tc.seed); err != nil {
					t.Fatal(err)
				}
				s, _ := New(db, testPolicy())
				var before uint64
				_ = db.View(ctx, "s", func(tx store.ReadTx) error { before = tx.LastSeq(); return nil })
				if _, err := s.CompleteTaskStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}); !errors.Is(err, tc.want) {
					t.Fatalf("got %v, want %v", err, tc.want)
				}
				if err := db.View(ctx, "s", func(tx store.ReadTx) error {
					task, _ := tx.Task("task")
					if tx.LastSeq() != before || task.Status != domain.TaskActive {
						t.Fatal("rejected completion committed")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestCompletionReplaysAcrossSQLiteRestart(t *testing.T) {
	ctx := context.Background()
	path := sqlitetest.Path(t)
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedCompletion(t, db, []string{"g1"}, "", false)
	user := storetest.NewPrincipal("s", domain.AuthorityUser)
	intent := domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}
	s, _ := New(db, testPolicy())
	first, err := s.CompleteTaskStandalone(ctx, user, intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	s, _ = New(reopened, testPolicy())
	again, err := s.CompleteTaskStandalone(ctx, user, intent)
	if err != nil || again.AuditID != first.AuditID || again.GCRequestID != first.GCRequestID || len(again.ResolvedGoals) != 1 || again.ResolvedGoals[0] != first.ResolvedGoals[0] {
		t.Fatalf("replay after restart: %+v %v, want %+v", again, err, first)
	}
	if _, err := s.CompleteTaskStandalone(ctx, user, domain.CompleteTaskIntent{RequestID: "r2", TaskID: "task"}); !errors.Is(err, domain.ErrInvalidTransition) {
		t.Fatalf("new request after restart: %v", err)
	}
	if pending := pendingGC(t, reopened); len(pending) != 1 || pending[0].ID != first.GCRequestID {
		t.Fatalf("durable GC request lost across restart: %+v", pending)
	}
}
