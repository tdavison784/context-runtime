package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// seedCheckpointItem commits task "task" at turn 2 and an otherwise
// collectible (ended-turn ephemeral) CHECKPOINT item "ck" of agent "agent".
func seedCheckpointItem(t *testing.T, mem store.Store) {
	t.Helper()
	if err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
		task := storetest.NewTask("s", "task")
		task.Turn, task.TurnID = 2, "turn-2"
		if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
			return err
		}
		it := storetest.NewItem("s", "ck", tx.NextSeq(), "summary")
		it.Kind, it.Role, it.Generation = domain.KindSummary, domain.RoleCheckpoint, domain.GenerationEphemeral
		it.Scope, it.Access = domain.ScopeTask, storetest.DirectiveBoundary("s")
		return tx.InsertItem(it)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGCProtectsOnlyTheNewestRelevantCheckpoint(t *testing.T) {
	defer func(orig func(store.ReadTx, domain.Principal, string, int, int) (domain.Checkpoint, bool, error)) {
		checkpointOfItem = orig
	}(checkpointOfItem)
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	for name, tc := range map[string]struct {
		newest bool
		err    error
		want   domain.GCDecisionCode
		fails  error
	}{
		"newest protected":           {newest: true, want: domain.GCProtected},
		"older collectible":          {newest: false, want: domain.GCArchive},
		"invisible counts as newest": {err: domain.ErrNotFound, want: domain.GCProtected},
		// H3: an overflowing lookup keeps the checkpoint (never archived)
		// without aborting the rest of the collection.
		"bounded lookup is ineligible": {err: domain.ErrResourceLimit, want: domain.GCIneligible},
	} {
		t.Run(name, func(t *testing.T) {
			mem := memory.New()
			t.Cleanup(func() { mem.Close() })
			seedCheckpointItem(t, mem)
			var viewer domain.Principal
			checkpointOfItem = func(tx store.ReadTx, v domain.Principal, id string, page, work int) (domain.Checkpoint, bool, error) {
				viewer = v
				return domain.Checkpoint{ItemID: id}, tc.newest, tc.err
			}
			s, _ := New(mem, testPolicy())
			out, err := collect(newFacets(), mem, s, harness, domain.CollectIntent{RequestID: "c", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual})
			if tc.fails != nil {
				if !errors.Is(err, tc.fails) {
					t.Fatalf("got %v, want %v", err, tc.fails)
				}
				return
			}
			if err != nil || len(out.Result.Collect.Decisions) != 1 || out.Result.Collect.Decisions[0].Code != tc.want {
				t.Fatalf("decision: %+v %v", out, err)
			}
			if viewer.Authority != domain.AuthorityAgent || viewer.TaskID != "task" || viewer.AgentID != "agent" || viewer.SessionID != "s" {
				t.Fatalf("lookup viewer is not the conversation's agent: %+v", viewer)
			}
		})
	}
}

func TestGCCheckpointWithoutCompanionAbortsOnRealGraph(t *testing.T) {
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	seedCheckpointItem(t, mem)
	s, _ := New(mem, testPolicy())
	_, err := collect(newFacets(), mem, s, storetest.NewPrincipal("s", domain.AuthorityHarness), domain.CollectIntent{RequestID: "c", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual})
	if !errors.Is(err, domain.ErrIntegrity) {
		t.Fatalf("checkpoint item without its record: %v", err)
	}
}
