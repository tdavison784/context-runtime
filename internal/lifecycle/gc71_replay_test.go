package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestGC71_FinishedRequestReplaysStoredReceiptForAnyAuthorizedCollector
// (GC-7.1): a finished GC request's replay is not the final-batch runner's
// private property. Any authorized collector — same session, SYSTEM or
// HARNESS, and the request's own task for a task-scoped request, exactly the
// continuation gate a pending batch passes (SEC-4.5) — replays the STORED
// final-batch receipt, PROJECTED for the calling principal (SPEC-6.2 /
// P3-38): the candidate, decision AND archived-result pairs that caller
// cannot read are dropped, because the runner could archive an item the
// caller has no business seeing named. The replay re-plans nothing from a
// fresh snapshot, never collides on the final batch's CollectReceipt
// identity, and writes nothing: no new receipt, no progress or result
// write, no sequence allocation. The collector that ran the final batch
// keeps its recorded outcome, and an unauthorized caller gets the same
// ErrInvalidAuthorityPromotion a pending continuation gets.
func TestGC71_FinishedRequestReplaysStoredReceiptForAnyAuthorizedCollector(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxGCDecisions = 1 // three candidates -> three batches of one
		s, _ := New(db, pol)
		// eph-002 — the final batch's only candidate, decided ARCHIVE — is
		// agent-limited: the runner reads and archives it; the second
		// collector, a sibling agent of the same task, cannot.
		seedEphemeralLimited(t, db, "agent", "eph-002")
		runner := storetest.NewPrincipal("s", domain.AuthorityHarness) // AgentID "agent"
		mate := runner
		mate.AgentID = "agent-2"
		id := enqueueScratch(t, db, s)
		step := func(p domain.Principal) (MutationOutcome, error) {
			var out MutationOutcome
			err := db.Update(ctx, "s", func(tx store.Tx) error {
				var e error
				out, e = s.ExecuteGCRequest(tx, p, id, 0)
				return e
			})
			return out, err
		}
		for range 8 {
			if _, ok := gcResult(t, db, id); ok {
				break
			}
			if _, err := step(runner); err != nil {
				t.Fatal(err)
			}
		}
		res, found := gcResult(t, db, id)
		if !found || res.Outcome != domain.GCCollected {
			t.Fatalf("request did not finish: %+v found=%v", res, found)
		}
		// The stored final-batch receipt: one ARCHIVE pair the mate cannot read.
		var stored domain.CollectReceipt
		readSemantic(t, db, func(sem store.SemanticReader) error {
			var err error
			stored, err = sem.CollectReceipt(res.CollectReceiptID)
			return err
		})
		if err := stored.Validate(); err != nil {
			t.Fatalf("stored final receipt does not validate: %v (%+v)", err, stored)
		}
		if len(stored.CandidateRefs) != 1 || stored.CandidateRefs[0].ItemID != "eph-002" ||
			stored.Decisions[0].Code != domain.GCArchive || len(stored.ArchivedRefs) != 1 {
			t.Fatalf("final batch receipt %+v: want eph-002 ARCHIVE with its archived result", stored)
		}

		// gc71Snapshot is every observable the replay could touch: the
		// request's progress and result rows, every committed batch's
		// decisions, the final receipt and its runner's mutation receipt,
		// the three items and their audit events, and the session's
		// sequence floor.
		type gc71Snapshot struct {
			progress   domain.GCProgress
			result     domain.GCResult
			decisions  map[string]domain.GCDecisionCode
			receipt    domain.CollectReceipt
			finalOwner domain.Principal
			items      map[string]domain.ContextItem
			events     map[string]int
			lastSeq    uint64
		}
		snapshot := func() gc71Snapshot {
			var g gc71Snapshot
			g.decisions = gcBatchDecisions(t, db, id)
			g.items = map[string]domain.ContextItem{}
			g.events = map[string]int{}
			readSemantic(t, db, func(sem store.SemanticReader) error {
				p, err := sem.GCProgress(id)
				if err != nil {
					return err
				}
				g.progress = p
				if g.result, err = sem.GCResult(id); err != nil {
					return err
				}
				if g.receipt, err = sem.CollectReceipt(res.CollectReceiptID); err != nil {
					return err
				}
				mr, err := sem.MutationReceipt(domain.MutationCollection, stored.RequestID)
				if err != nil {
					return err
				}
				g.finalOwner = mr.Principal
				return nil
			})
			if err := db.View(ctx, "s", func(tx store.ReadTx) error {
				for _, item := range []string{"eph-000", "eph-001", "eph-002"} {
					it, err := tx.Item(item)
					if err != nil {
						return err
					}
					g.items[item] = it
					ev, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetItem, TargetID: item})
					if err != nil {
						return err
					}
					g.events[item] = len(ev)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Update(ctx, "s", func(tx store.Tx) error {
				g.lastSeq = tx.LastSeq()
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			return g
		}
		before := snapshot()

		// The second authorized collector replays the stored final-batch
		// receipt PROJECTED for it: the archived pair it cannot read is
		// gone — candidate, decision and archived result alike.
		got, err := step(mate)
		if err != nil {
			t.Fatalf("second collector's replay of the finished request: %v", err)
		}
		if got.Result.Collect == nil {
			t.Fatalf("second collector's replay returned no receipt: %+v", got)
		}
		if err := got.Result.Collect.Validate(); err != nil {
			t.Fatalf("projected replay does not validate: %v (%+v)", err, got.Result.Collect)
		}
		if len(got.Result.Collect.CandidateRefs) != 0 || len(got.Result.Collect.Decisions) != 0 || len(got.Result.Collect.ArchivedRefs) != 0 {
			t.Fatalf("projected replay kept the unreadable pair: %+v", got.Result.Collect)
		}
		for _, ref := range got.Result.Collect.CandidateRefs {
			if ref.ItemID == "eph-002" {
				t.Errorf("projected replay names eph-002 as a candidate (SPEC-6.2)")
			}
		}
		for _, ref := range got.Result.Collect.ArchivedRefs {
			if ref.ItemID == "eph-002" {
				t.Errorf("projected replay names eph-002 as archived (P3-38)")
			}
		}
		// The replay wrote nothing, so there is no mutation receipt of the
		// second collector's own to reference.
		if got.MutationReceiptID != "" {
			t.Fatalf("second collector's replay reports receipt %q: a writeless replay records nothing", got.MutationReceiptID)
		}
		// Nothing was written: every stored row, every item and the session's
		// sequence floor are unchanged — no re-plan, no new receipt, no
		// CollectReceipt-ID collision.
		if after := snapshot(); !reflect.DeepEqual(before, after) {
			t.Fatalf("replay by the second collector wrote state:\nbefore %+v\nafter  %+v", before, after)
		}
		// And it is deterministic: replaying again returns the same copy.
		again, err := step(mate)
		if err != nil || !reflect.DeepEqual(got, again) {
			t.Fatalf("second replay: %+v, %v (want identical to the first: %+v)", again, err, got)
		}

		// The collector that ran the final batch still replays its recorded
		// outcome: the full stored receipt (it could read every candidate).
		own, err := step(runner)
		if err != nil {
			t.Fatalf("final-batch runner's replay: %v", err)
		}
		if own.Result.Collect == nil || !reflect.DeepEqual(*own.Result.Collect, stored) {
			t.Fatalf("runner's replay %+v: want the stored final receipt %+v", own.Result.Collect, stored)
		}
		if own.MutationReceiptID == "" {
			t.Fatal("runner's replay lost its recorded mutation receipt")
		}
		if after := snapshot(); !reflect.DeepEqual(before, after) {
			t.Fatalf("runner's replay wrote state:\nbefore %+v\nafter  %+v", before, after)
		}

		// An unauthorized caller gets the same refusal a pending continuation
		// gets (GC-7.1: the replay is gated like the continuation, and the
		// refusal is uniform across pending and finished requests).
		if _, err := step(storetest.NewPrincipal("s", domain.AuthorityUser)); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("user replay: %v", err)
		}
		foreign := runner
		foreign.TaskID = "other-task"
		if _, err := step(foreign); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("foreign-task replay: %v", err)
		}
		if after := snapshot(); !reflect.DeepEqual(before, after) {
			t.Fatalf("refused replays wrote state:\nbefore %+v\nafter  %+v", before, after)
		}
	})
}
