package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func sec26Principal(task string, a domain.Authority) domain.Principal {
	p := storetest.NewPrincipal("s", a)
	p.TaskID = task
	return p
}

func sec26SeedTask(t *testing.T, st store.Store, task string, n int) {
	t.Helper()
	if err := st.Update(context.Background(), "s", func(tx store.Tx) error {
		if _, err := tx.PutTask(storetest.NewTask("s", task), 0, storetest.NewLifecycleEvent("s", "created-"+task, tx.NextSeq(), domain.TargetTask, task)); err != nil {
			return err
		}
		for i := range n {
			it := storetest.NewItem("s", fmt.Sprintf("%s-item-%d", task, i), tx.NextSeq(), fmt.Sprintf("message %d", i))
			it.TaskID, it.Scope = task, domain.ScopeTask
			it.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: task}
			if err := tx.InsertItem(it); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestGCRequestIDsCannotBeNamedByCallers_SEC26 (H5, SEC-2.6): GC request and
// collection IDs are a reserved runtime namespace. A manual Collect naming
// one, including another task's predictable completion ID, is refused, and
// the victim task's completion GC request stays executable.
func TestGCRequestIDsCannotBeNamedByCallers_SEC26(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, st store.Store) {
		s, err := New(st, policy.DefaultPhase3Policy())
		if err != nil {
			t.Fatal(err)
		}
		sec26SeedTask(t, st, "victim", 1)
		sec26SeedTask(t, st, "attacker", 0)
		predicted := "gc_" + domain.NewCanonicalEncoder("context-runtime/gc-trigger/v1").String("s").String(string(domain.GCTaskCompletion)).String("victim").Hash()
		attacker := sec26Principal("attacker", domain.AuthorityHarness)
		for _, id := range []string{predicted, "gc_x", "gcq_x"} {
			err := st.Update(ctx, "s", func(tx store.Tx) error {
				_, err := s.Collect(tx, attacker, domain.CollectIntent{RequestID: id, Scope: domain.CollectTask, TaskID: "attacker", Trigger: domain.GCManual}, 0)
				return err
			})
			if !errors.Is(err, domain.ErrInvalidRecord) {
				t.Fatalf("manual Collect named reserved GC request ID %q: err = %v", id, err)
			}
		}
		out, err := s.CompleteTaskStandalone(ctx, sec26Principal("victim", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "c-victim", TaskID: "victim"})
		if err != nil {
			t.Fatalf("complete: %v", err)
		}
		if err := st.Update(ctx, "s", func(tx store.Tx) error {
			_, err := s.ExecuteGCRequest(tx, sec26Principal("victim", domain.AuthorityHarness), out.GCRequestID, 0)
			return err
		}); err != nil {
			t.Fatalf("victim's completion GC request is unexecutable: %v", err)
		}
	})
}
