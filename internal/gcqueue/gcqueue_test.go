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

// failedRequest stores a GC request as the re-arm source and marks it FAILED.
func failedRequest(t *testing.T, mem store.Store, r domain.GCRequest) domain.GCRequest {
	t.Helper()
	if err := mem.Update(context.Background(), r.SessionID, func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		r.Seq = tx.NextSeq()
		if err := sem.InsertGCRequest(r); err != nil {
			return err
		}
		return sem.InsertGCResult(domain.GCResult{SemanticMeta: storetest.Meta(r.SessionID, "gcres_"+r.ID, tx.NextSeq()),
			GCRequestID: r.ID, Outcome: domain.GCFailed, Reason: domain.GCFailureAttemptsExhausted})
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

// DUR-3.3 / SEC-4.4 / DUR-4.8: re-arm enqueues under the failed request's
// own scope and trigger — MANUAL and J7 session scope included — with an
// identity derived from the failed request alone, so any authorized actor
// re-arming is idempotent; a trigger the policy disables persists nothing.
func TestEnqueueRearmIsActorFreeAndManualCapable(t *testing.T) {
	ctx := context.Background()
	recorded := policy.DefaultPhase3Policy()
	recorded.Version = domain.Phase3PolicyVersion
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	seedTask(t, mem)

	manual := failedRequest(t, mem, domain.GCRequest{SemanticMeta: storetest.Meta("s", "gcq_manual1", 1),
		CollectIntent: domain.CollectIntent{RequestID: "manual-root", Scope: domain.CollectSession, Trigger: domain.GCManual},
		Origin:        storetest.NewPrincipal("s", domain.AuthoritySystem), PolicyVersion: recorded.Version})
	rearm := func(actor domain.Principal, failed domain.GCRequest, pol domain.Phase3Policy) (string, error) {
		var out string
		err := mem.Update(ctx, "s", func(tx store.Tx) error {
			var err error
			out, err = gcqueue.EnqueueRearm(tx, pol, actor, failed)
			return err
		})
		return out, err
	}
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	id, err := rearm(harness, manual, recorded)
	got := pending(t, mem)
	if err != nil || id == "" || len(got) != 1 || got[0].ID != id || got[0].Scope != domain.CollectSession ||
		got[0].Trigger != domain.GCManual || got[0].RequestID == manual.RequestID || got[0].PolicyVersion != recorded.Version {
		t.Fatalf("re-arm of a MANUAL session request: %q %v %+v", id, err, got)
	}
	// Any other authorized actor replays the same request; the identity
	// never derives from the actor, so no duplicate request appears.
	again, err := rearm(storetest.NewPrincipal("s", domain.AuthoritySystem), manual, recorded)
	if err != nil || again != id || len(pending(t, mem)) != 1 {
		t.Fatalf("re-arm is actor-bound: %q %v", again, err)
	}
	// A trigger this policy disables persists nothing ("", nil).
	off := recorded.Clone()
	off.GCTriggers = []domain.GCTrigger{domain.GCManual, domain.GCTaskCompletion}
	superseded := failedRequest(t, mem, domain.GCRequest{SemanticMeta: storetest.Meta("s", "gcq_sup1", 1),
		CollectIntent: domain.CollectIntent{RequestID: "gc_sup", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCSupersession},
		Origin:        storetest.NewPrincipal("s", domain.AuthoritySystem), PolicyVersion: recorded.Version})
	if id, err := rearm(harness, superseded, off); id != "" || err != nil || len(pending(t, mem)) != 1 {
		t.Fatalf("disabled trigger re-armed: %q %v", id, err)
	}
	// An existing request at the derived identity with different intent
	// content is a conflict, and the failure poisons the transaction.
	tampered := manual
	tampered.CollectIntent.TaskID = "task"
	tampered.CollectIntent.Scope = domain.CollectTask
	if _, err := rearm(harness, tampered, recorded); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Fatalf("tampered re-arm content: %v", err)
	}
}
