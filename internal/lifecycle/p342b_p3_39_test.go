package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_39_GCRequestExecutesOnceAcrossSQLiteRestart closes the P3-42 table
// row "retry request executes once" (ADR8:1294). The cited test runs on the
// memory store only; this is the SQLite half with the real crash window:
// completion commits and enqueues the durable GC request, the process
// "crashes" (close) before any collection, and after reopen the request is
// drained exactly once — the archived item is touched once, a second drain
// does nothing, and a direct retry replays the frozen receipt.
func TestP3_39_GCRequestExecutesOnceAcrossSQLiteRestart(t *testing.T) {
	ctx := context.Background()
	path := sqlitetest.Path(t)
	db, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := New(db, testPolicy())
	seedCompletion(t, db, nil, "", false)
	scratch := storetest.NewItem("s", "scratch", 0, "scratch")
	scratch.Scope, scratch.Access, scratch.Generation = domain.ScopeTask, storetest.DirectiveBoundary("s"), domain.GenerationEphemeral
	seedItem(t, db, scratch)
	f := newFacets("scratch")
	done, err := completeTask(f, db, s, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r39", TaskID: "task"}, true)
	if err != nil {
		t.Fatal(err)
	}
	id := done.Result.Completion.GCRequestID
	if id == "" {
		t.Fatal("completion enqueued no GC request")
	}
	// The producer committed; the collector crashes before running.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	s, _ = New(reopened, testPolicy())
	pending := pendingGC(t, reopened)
	if len(pending) != 1 || pending[0].ID != id {
		t.Fatalf("durable GC request lost across restart: %+v", pending)
	}
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	n, err := s.CollectPending(ctx, "s", func(domain.GCRequest) (domain.Principal, bool) { return harness, true }, 1)
	if n != 1 || err != nil {
		t.Fatalf("drain after restart: n=%d err=%v", n, err)
	}
	result, found := gcResult(t, reopened, id)
	if !found || result.Outcome != domain.GCCollected {
		t.Fatalf("GC result after restart: %+v found=%v", result, found)
	}
	if err := reopened.View(ctx, "s", func(tx store.ReadTx) error {
		it, err := tx.Item("scratch")
		if err != nil || it.Residency != domain.ResidencyArchived || it.Version != 2 {
			t.Fatalf("scratch archived more than once: %+v %v", it, err)
		}
		events, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetItem, TargetID: "scratch"})
		if err != nil || len(events) != 1 {
			t.Fatalf("scratch archive audits: %+v %v", events, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Nothing left to run: a second drain executes nothing.
	if n, err := s.CollectPending(ctx, "s", func(domain.GCRequest) (domain.Principal, bool) { return harness, true }, 1); n != 0 || err != nil {
		t.Fatalf("second drain: n=%d err=%v", n, err)
	}
	// A direct retry replays the frozen receipt and archives nothing new.
	again, err := executeGC(f, reopened, s, harness, id)
	if err != nil || again.Result.Collect == nil || result.CollectReceiptID != again.Result.Collect.ID {
		t.Fatalf("retry after restart: %+v %v (stored %s)", again, err, result.CollectReceiptID)
	}
	if got := residency(t, reopened, "scratch"); got != domain.ResidencyArchived {
		t.Fatalf("retry re-archived or restored: %s", got)
	}
	if err := reopened.View(ctx, "s", func(tx store.ReadTx) error {
		it, _ := tx.Item("scratch")
		if it.Version != 2 {
			t.Fatalf("retry bumped the item again: %+v", it)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestP3_39_OverLimitCollectionWritesNothing closes the P3-42 table row
// "limit rollback" (ADR8:1297): the cited test only checked that the final
// receipt fits. This one forces the receipt over MaxReceiptBytes with real
// stores and proves the two fail-safe outcomes: a batch that can shrink to a
// single-decision skip receipt archives NOTHING (no item, no audit events)
// and stores exactly the SKIP_RESOURCE_LIMIT disclosure, and a bound so
// tight not even that fits fails closed with ErrResourceLimit leaving no
// receipt at all.
func TestP3_39_OverLimitCollectionWritesNothing(t *testing.T) {
	ctx := context.Background()
	const items = 24
	for name, open := range map[string]func(*testing.T) store.Store{
		"memory": func(t *testing.T) store.Store {
			s := memory.New()
			t.Cleanup(func() { s.Close() })
			return s
		},
		"sqlite": func(t *testing.T) store.Store { return sqlitetest.Open(t) },
	} {
		t.Run(name, func(t *testing.T) {
			for sub, limit := range map[string]int{"skip-receipt-fits": 2025, "nothing-fits": 1} {
				// 2025 sits in the deterministic window where the trimmed
				// single-decision SKIP receipt fits but the same receipt
				// with one archive effect (extra ArchivedRefs entry) does
				// not: the boundary is 2040 with this seed.
				t.Run(sub, func(t *testing.T) {
					// A fresh store per subtest so the no-receipt assertion
					// is not polluted by the sibling subtest's receipt.
					db := open(t)
					seedEphemeral(t, db, items, 0)
					pol := testPolicy()
					pol.MaxReceiptBytes = limit
					s, _ := New(db, pol)
					f := newFacets(p342bEphemeralIDs(items)...)
					harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
					out, err := collect(f, db, s, harness, domain.CollectIntent{RequestID: "c39-" + sub, Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual})
					if sub == "nothing-fits" {
						if !errors.Is(err, domain.ErrResourceLimit) {
							t.Fatalf("over-limit collect: %v, want ErrResourceLimit", err)
						}
						readSemantic(t, db, func(sem store.SemanticReader) error {
							if _, err := sem.CollectReceipt(collectReceiptID("s", "c39-"+sub)); !errors.Is(err, domain.ErrNotFound) {
								t.Fatalf("failed over-limit collect stored a receipt: %v", err)
							}
							return nil
						})
					} else {
						if err != nil {
							t.Fatalf("skippable over-limit collect: %v", err)
						}
						r := out.Result.Collect
						if r == nil || len(r.Decisions) != 1 || r.Decisions[0].Code != domain.GCSkipResourceLimit || len(r.ArchivedRefs) != 0 {
							t.Fatalf("skip receipt: %+v", r)
						}
					}
					// Either way: nothing was archived, no audit events exist,
					// and every item is untouched at version 1.
					if err := db.View(ctx, "s", func(tx store.ReadTx) error {
						for _, id := range p342bEphemeralIDs(items) {
							it, err := tx.Item(id)
							if err != nil || it.Residency != domain.ResidencyResident || it.Version != 1 {
								t.Fatalf("%s touched by an over-limit batch: %+v %v", id, it, err)
							}
							events, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetItem, TargetID: id})
							if err != nil || len(events) != 0 {
								t.Fatalf("%s has audit events: %+v %v", id, events, err)
							}
						}
						return nil
					}); err != nil {
						t.Fatal(err)
					}
				})
			}
		})
	}
}

func p342bEphemeralIDs(n int) []string {
	ids := make([]string, n)
	for i := range n {
		ids[i] = fmt.Sprintf("eph-%03d", i)
	}
	return ids
}
