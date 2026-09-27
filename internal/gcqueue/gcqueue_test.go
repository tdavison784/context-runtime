package gcqueue_test

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/gcqueue"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func pending(t *testing.T, mem store.Store) []domain.GCRequest {
	t.Helper()
	var out []domain.GCRequest
	if err := mem.View(context.Background(), "s", func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		page, err := sem.PendingGCRequests(store.Page{Limit: 16})
		out = page.Records
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func seedTask(t *testing.T, mem store.Store) {
	t.Helper()
	if err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
		_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// The enqueue primitive honours the caller's RECORDED policy (SPEC-2.11),
// produces nothing for a task-less trigger (H4), deduplicates by trigger
// identity and poisons the transaction on any failure.
func TestEnqueueUsesRecordedPolicyAndTaskScope(t *testing.T) {
	ctx := context.Background()
	origin := storetest.NewPrincipal("s", domain.AuthoritySystem)
	recorded := policy.DefaultPhase3Policy()
	recorded.Version = domain.Phase3PolicyVersion
	off := recorded.Clone()
	off.GCTriggers = []domain.GCTrigger{domain.GCManual, domain.GCTaskCompletion}

	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	seedTask(t, mem)
	enqueue := func(pol domain.Phase3Policy, trigger domain.GCTrigger, task, id string) (string, error) {
		var out string
		err := mem.Update(ctx, "s", func(tx store.Tx) error {
			var err error
			out, err = gcqueue.Enqueue(tx, pol, origin, trigger, task, id)
			return err
		})
		return out, err
	}
	if id, err := enqueue(off, domain.GCSupersession, "task", "item-2"); id != "" || err != nil || len(pending(t, mem)) != 0 {
		t.Fatalf("recorded policy disables SUPERSESSION: %q %v", id, err)
	}
	if id, err := enqueue(recorded, domain.GCSupersession, "", "item-2"); id != "" || err != nil || len(pending(t, mem)) != 0 {
		t.Fatalf("task-less trigger produced a request: %q %v", id, err)
	}
	id, err := enqueue(recorded, domain.GCSupersession, "task", "item-2")
	got := pending(t, mem)
	if err != nil || id == "" || len(got) != 1 || got[0].ID != id || got[0].Scope != domain.CollectTask || got[0].TaskID != "task" ||
		got[0].Origin != origin || got[0].PolicyVersion != recorded.Version || got[0].Trigger != domain.GCSupersession {
		t.Fatalf("enqueued: %q %v %+v", id, err, got)
	}
	if again, err := enqueue(recorded, domain.GCSupersession, "task", "item-2"); again != id || err != nil || len(pending(t, mem)) != 1 {
		t.Fatalf("duplicate trigger: %q %v", again, err)
	}
	// Task completion persists even when its trigger is disabled (P3-39).
	if id, err := enqueue(off, domain.GCTaskCompletion, "task", "task"); id == "" || err != nil {
		t.Fatalf("completion request: %q %v", id, err)
	}
	for name, tc := range map[string]struct {
		pol     domain.Phase3Policy
		trigger domain.GCTrigger
		task    string
		want    error
	}{
		"manual":         {recorded, domain.GCManual, "task", domain.ErrInvalidRecord},
		"conflict":       {recorded, domain.GCSupersession, "other-task", domain.ErrEventIDConflict},
		"invalid policy": {domain.Phase3Policy{}, domain.GCSupersession, "task", nil},
	} {
		err := mem.Update(ctx, "s", func(tx store.Tx) error {
			_, _ = gcqueue.Enqueue(tx, tc.pol, origin, tc.trigger, tc.task, "item-2")
			return nil // ignored: the transaction must still be poisoned
		})
		if err == nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Fatalf("%s: ignored failure committed or wrong error: %v", name, err)
		}
	}
}
