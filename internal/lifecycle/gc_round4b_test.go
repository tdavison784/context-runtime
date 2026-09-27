package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// enqueueWithID persists a durable GC request with the given trigger identity.
func enqueueWithID(t *testing.T, db store.Store, s *Service, trigger domain.GCTrigger, triggerID string) string {
	t.Helper()
	var id string
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		var err error
		id, err = s.EnqueueGC(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), trigger, domain.CollectTask, "task", triggerID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// failRequest marks GC request id FAILED as the consumer would.
func failRequest(t *testing.T, db store.Store, id string) {
	t.Helper()
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		return sem.InsertGCResult(domain.GCResult{SemanticMeta: domain.SemanticMeta{ID: gcResultID("s", id), SessionID: "s",
			SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()}, GCRequestID: id, Outcome: domain.GCFailed, Reason: domain.GCFailureIntegrity})
	}); err != nil {
		t.Fatal(err)
	}
}

// storeManualRequest seeds a J7 manual session request directly, as the
// manual Collect path would have, and returns its record ID.
func storeManualRequest(t *testing.T, db store.Store, s *Service, requestID string) string {
	t.Helper()
	var id string
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		r := domain.GCRequest{SemanticMeta: domain.SemanticMeta{ID: "gcq_manual_" + requestID, SessionID: "s",
			SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
			CollectIntent: domain.CollectIntent{RequestID: requestID, Scope: domain.CollectSession, Trigger: domain.GCManual},
			Origin:        storetest.NewPrincipal("s", domain.AuthoritySystem), PolicyVersion: s.policy.Version}
		id = r.ID
		return sem.InsertGCRequest(r)
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

// SEC-4.4 / SPEC-4.6: re-arm is bound to the request's task before any
// outcome check, and a foreign principal's error is indistinguishable from
// an absent request — no existence oracle over predictable gcq_ IDs.
func TestRearmBindsToTheRequestTaskAndIsNoOracle_SEC44(t *testing.T) {
	ctx := context.Background()
	attacker := sec26Principal("attacker", domain.AuthorityHarness)
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		seedEphemeral(t, db, 0, 0)
		pending := enqueueScratch(t, db, s)
		failed := enqueueWithID(t, db, s, domain.GCSupersession, "scratch-2")
		failRequest(t, db, failed)
		try := func(id string) error {
			return db.Update(ctx, "s", func(tx store.Tx) error {
				_, err := s.RearmGCRequest(tx, attacker, id)
				return err
			})
		}
		absent := try("gcq_absent")
		pend := try(pending)
		fail := try(failed)
		for _, err := range []error{absent, pend, fail} {
			if !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("foreign re-arm is not not-found: %v", err)
			}
		}
		if absent.Error() != pend.Error() || pend.Error() != fail.Error() {
			t.Fatalf("foreign re-arm discloses request state: absent=%v pending=%v failed=%v", absent, pend, fail)
		}
		// An in-task HARNESS and SYSTEM both re-arm the same failed request
		// to the same identity (idempotent across actors).
		mate := storetest.NewPrincipal("s", domain.AuthorityHarness)
		var first, second string
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			var err error
			first, err = s.RearmGCRequest(tx, mate, failed)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			var err error
			second, err = s.RearmGCRequest(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), failed)
			return err
		}); err != nil || second != first {
			t.Fatalf("re-arm is actor-bound: %q %q %v", first, second, err)
		}
		if pendingList := pendingGC(t, db); len(pendingList) != 2 { // the re-armed one plus the still-pending scratch request
			t.Fatalf("re-arm duplicated the request: %+v", pendingList)
		}
	})
}

// SEC-4.4 / SPEC-4.6 / DUR-4.8: a FAILED MANUAL (J7 session-scope) request
// re-arms and collects, and a trigger this policy disables reports
// ErrGCTriggerDisabled instead of a silent empty ID.
func TestRearmSupportsManualAndReportsDisabledTriggers_SEC44(t *testing.T) {
	ctx := context.Background()
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	pick := func(domain.GCRequest) (domain.Principal, bool) { return harness, true }
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		seedEphemeral(t, db, 0, 0)
		manual := storeManualRequest(t, db, s, "m1")
		failRequest(t, db, manual)
		var rearmID string
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			var err error
			rearmID, err = s.RearmGCRequest(tx, harness, manual)
			return err
		}); err != nil || rearmID == "" {
			t.Fatalf("MANUAL re-arm: %q %v", rearmID, err)
		}
		if n, err := s.CollectPending(ctx, "s", pick, 4); n != 1 || err != nil {
			t.Fatalf("re-armed MANUAL request: n=%d err=%v", n, err)
		}
		if res, found := gcResult(t, db, rearmID); !found || res.Outcome != domain.GCCollected {
			t.Fatalf("re-armed MANUAL not collected: %+v found=%v", res, found)
		}

		off := testPolicy()
		off.GCTriggers = []domain.GCTrigger{domain.GCManual, domain.GCTaskCompletion}
		s2, _ := New(db, off)
		disabled := enqueueScratch(t, db, s) // SUPERSESSION
		failRequest(t, db, disabled)
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			_, err := s2.RearmGCRequest(tx, harness, disabled)
			return err
		}); !errors.Is(err, ErrGCTriggerDisabled) {
			t.Fatalf("disabled trigger re-arm: %v", err)
		}
	})
}

// SEC-4.8: a caller-named manual Collect derives its durable request in the
// manual encoder domain. Under the runtime trigger domain, a SYSTEM
// collector's manual Collect naming the task's own ID precomputes the
// task-completion record: the completion then fails ErrEventIDConflict and
// its GC never runs.
func TestManualCollectDoesNotWedgeTaskCompletion_SEC48(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		s, _ := New(db, testPolicy())
		seedCompletion(t, db, nil, "", false)
		p := storetest.NewPrincipal("s", domain.AuthoritySystem)
		var manualID string
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			out, err := s.Collect(tx, p, domain.CollectIntent{RequestID: "task", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCTaskCompletion}, tx.NextSeq())
			if err == nil {
				manualID = out.Result.Collect.GCRequestID
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		done, err := s.CompleteTaskStandalone(ctx, p, domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"})
		if err != nil || done.GCRequestID == "" {
			t.Fatalf("manual Collect wedged task completion: %v", err)
		}
		if manualID == done.GCRequestID {
			t.Fatalf("manual and completion requests share identity %q", manualID)
		}
		runGC(t, db, s, done.GCRequestID, 8)
		if res, ok := gcResult(t, db, done.GCRequestID); !ok || res.Outcome != domain.GCCollected {
			t.Fatalf("completion request not collected: %+v %v", res, ok)
		}
		if res, ok := gcResult(t, db, manualID); !ok || res.Outcome != domain.GCCollected {
			t.Fatalf("manual request not collected: %+v %v", res, ok)
		}
	})
}

// SEC-4.5 (J5 wedge): a pending request's continuation binds to authority
// class and task, not the exact principal that ran batch 1. A same-task
// HARNESS with another AgentID finishes the request; binding to the first
// collector left it pending forever (uncollectable, never FAILED, one
// CollectPending attempt every cycle).
func TestContinuationIsNotBoundToTheFirstCollector_SEC45(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxGCDecisions = 1
		s, _ := New(db, pol)
		seedEphemeral(t, db, 3, 0)
		id := enqueueScratch(t, db, s)
		first := storetest.NewPrincipal("s", domain.AuthorityHarness)
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			_, err := s.ExecuteGCRequest(tx, first, id, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		readSemantic(t, db, func(sem store.SemanticReader) error {
			p, err := sem.GCProgress(id)
			if p.Batches != 1 {
				t.Fatalf("setup: %d batches", p.Batches)
			}
			return err
		})
		mate := first
		mate.AgentID = "agent-2"
		for range 10 {
			if _, ok := gcResult(t, db, id); ok {
				break
			}
			if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
				_, err := s.ExecuteGCRequest(tx, mate, id, 0)
				return err
			}); err != nil {
				t.Fatalf("same-task mate cannot continue: %v", err)
			}
		}
		res, found := gcResult(t, db, id)
		if !found || res.Outcome != domain.GCCollected {
			t.Fatalf("request wedged on its first collector: %+v found=%v", res, found)
		}
	})
}
