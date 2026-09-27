package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestEnqueueGCFailurePoisons checks DUR-1.11 (P3-1/39): a failed EnqueueGC
// poisons the producer's transaction like every other lifecycle entry
// point, so a producer that ignores the error cannot commit its write
// without the durable trigger.
func TestEnqueueGCFailurePoisons(t *testing.T) {
	for name, st := range map[string]func(*testing.T) store.Store{
		"memory": func(t *testing.T) store.Store {
			m := memory.New()
			t.Cleanup(func() { m.Close() })
			return m
		},
		"sqlite": func(t *testing.T) store.Store { return sqlitetest.Open(t) },
	} {
		t.Run(name, func(t *testing.T) {
			mem := st(t)
			s, _ := New(mem, testPolicy())
			p := storetest.NewPrincipal("s", domain.AuthoritySystem)
			ctx := context.Background()
			// H4: no session-scoped requests exist, so the conflicting
			// identity is the same trigger for another stored task.
			if err := mem.Update(ctx, "s", func(tx store.Tx) error {
				for _, id := range []string{"task", "other"} {
					if _, err := tx.PutTask(storetest.NewTask("s", id), 0, storetest.NewLifecycleEvent("s", "created-"+id, tx.NextSeq(), domain.TargetTask, id)); err != nil {
						return err
					}
				}
				_, err := s.EnqueueGC(tx, p, domain.GCSupersession, domain.CollectTask, "other", "event-1")
				return err
			}); err != nil {
				t.Fatal(err)
			}
			err := mem.Update(ctx, "s", func(tx store.Tx) error {
				if err := tx.InsertItem(storetest.NewItem("s", "produced", tx.NextSeq(), "producer write")); err != nil {
					return err
				}
				// Same trigger identity, different content: a conflict the
				// producer ignores.
				_, _ = s.EnqueueGC(tx, p, domain.GCSupersession, domain.CollectTask, "task", "event-1")
				return nil
			})
			if !errors.Is(err, domain.ErrEventIDConflict) {
				t.Errorf("Update = %v, want the ignored EnqueueGC conflict", err)
			}
			if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
				if _, err := tx.Item("produced"); !errors.Is(err, domain.ErrNotFound) {
					t.Errorf("producer write committed without its GC trigger: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
