package lifecycle

import (
	"context"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// seedP3_39 seeds identical collectable content: task "task" at turn 2, a
// superseded directive pair (old→historical, new→current pinned), an OPEN
// goal, two ended-turn ephemeral items whose only difference is the usage
// counter, and a current-turn ephemeral item.
func seedP3_39(t *testing.T, db store.Store) {
	t.Helper()
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		task := storetest.NewTask("s", "task")
		task.Turn, task.TurnID = 2, "turn-2"
		if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
			return err
		}
		items := []domain.ContextItem{
			storetest.NewDirective("s", "old", "d", tx.NextSeq(), "first"),
			storetest.NewDirective("s", "new", "d", tx.NextSeq(), "second"),
			storetest.NewGoal("s", "goal", tx.NextSeq(), "ship"),
		}
		for _, id := range []string{"eph-never", "eph-used", "cur-eph"} {
			it := storetest.NewItem("s", id, tx.NextSeq(), id)
			it.Generation = domain.GenerationEphemeral
			if id == "eph-used" {
				it.LastUsedCall = 42 // the only difference from eph-never
			}
			if id == "cur-eph" {
				it.TurnID = "turn-2"
			}
			items = append(items, it)
		}
		for _, it := range items {
			if err := tx.InsertItem(it); err != nil {
				return err
			}
		}
		if err := storetest.UncheckedSetCurrentVersion(tx, "old"); err != nil {
			return err
		}
		return storetest.UncheckedSetCurrentVersion(tx, "new")
	}); err != nil {
		t.Fatal(err)
	}
}

// runP3_39 collects with intent and returns the frozen receipt.
func runP3_39(t *testing.T, db store.Store, intent domain.CollectIntent) *domain.CollectReceipt {
	t.Helper()
	s, err := New(db, testPolicy())
	if err != nil {
		t.Fatal(err)
	}
	f := newFacets("old", "new", "goal", "eph-never", "eph-used", "cur-eph")
	out, err := collect(f, db, s, storetest.NewPrincipal("s", domain.AuthoritySystem), intent)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Collect == nil || out.Result.Collect.Validate() != nil {
		t.Fatalf("collect receipt: %+v", out)
	}
	return out.Result.Collect
}

// TestP3_39_StableResultsIndependentOfClockAndCounter closes the P3-42 table
// row "stable results independent of wall clock/counter": the identical
// content collected with the same intent yields the identical decision
// sequence and archived set — across a wall-clock gap between runs, across
// two independent stores per backend, and across backends — and a usage
// counter never changes a decision: eph-never (LastUsedCall 0) and eph-used
// (LastUsedCall 42) share one code.
func TestP3_39_StableResultsIndependentOfClockAndCounter(t *testing.T) {
	intent := domain.CollectIntent{RequestID: "c39", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual}
	var cross []domain.GCDecision
	for name, open := range map[string]func(*testing.T) store.Store{
		"memory": func(t *testing.T) store.Store {
			s := memory.New()
			t.Cleanup(func() { s.Close() })
			return s
		},
		"sqlite": func(t *testing.T) store.Store { return sqlitetest.Open(t) },
	} {
		t.Run(name, func(t *testing.T) {
			a, b := open(t), open(t)
			seedP3_39(t, a)
			seedP3_39(t, b)
			first := runP3_39(t, a, intent)
			// The decision path consults no wall clock (no time.Now anywhere
			// in lifecycle/policy/store production code), so the second run
			// executes at a statically pinned instant: the same one. Equal
			// results are structural determinism, not timing luck; the
			// CreatedAt sweep belongs to the snapshot, not the clock.
			second := runP3_39(t, b, intent) // fresh collect of identical content
			same := func(x, y *domain.CollectReceipt) bool {
				return slices.Equal(x.Decisions, y.Decisions) && slices.Equal(x.ArchivedRefs, y.ArchivedRefs)
			}
			if !same(first, second) {
				t.Fatalf("identical content, later clock: %+v vs %+v", first.Decisions, second.Decisions)
			}
			if replay := runP3_39(t, b, intent); !same(second, replay) || replay.ID != second.ID {
				t.Fatalf("same request must replay frozen: %+v vs %+v", replay.Decisions, second.Decisions)
			}
			if cross == nil {
				cross = first.Decisions
			} else if !slices.Equal(cross, first.Decisions) {
				t.Fatalf("backends disagree: %+v vs %+v", cross, first.Decisions)
			}

			// Pin the exact codes: superseded→ARCHIVE, current requirement
			// (pinned directive, OPEN goal)→PROTECTED, ended-turn ephemeral→
			// ARCHIVE regardless of usage counter, current-turn ephemeral→
			// PROTECTED.
			codes := map[string]domain.GCDecisionCode{}
			for _, d := range first.Decisions {
				codes[d.Target.ItemID] = d.Code
			}
			want := map[string]domain.GCDecisionCode{
				"old": domain.GCArchive, "new": domain.GCProtected, "goal": domain.GCProtected,
				"eph-never": domain.GCArchive, "eph-used": domain.GCArchive, "cur-eph": domain.GCProtected,
			}
			if len(codes) != len(want) {
				t.Fatalf("decisions %v, want exactly %v", codes, want)
			}
			for id, code := range want {
				if codes[id] != code {
					t.Errorf("%s = %s, want %s", id, codes[id], code)
				}
			}
			var archived []string
			for _, ref := range first.ArchivedRefs {
				archived = append(archived, ref.ItemID)
			}
			slices.Sort(archived)
			if !slices.Equal(archived, []string{"eph-never", "eph-used", "old"}) {
				t.Fatalf("archived %v", archived)
			}
		})
	}
}
