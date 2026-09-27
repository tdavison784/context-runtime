package tools

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// W3 GC protects only the newest relevant checkpoint: the lookups say which
// checkpoint an item is, whether it is newest, and which checkpoints cover it.
func TestCheckpointLookupsFindNewestAndCoveringCheckpoints(t *testing.T) {
	st, s, i2, manifest := checkpointConversation(t, true)
	checkpoint := func(i domain.ToolInvocation, request, manifest string) domain.Checkpoint {
		var c domain.Checkpoint
		update(t, st, func(tx store.Tx) error {
			r, err := s.CreateCheckpoint(tx, dispatcher(i), Request[domain.CheckpointIntent]{i, summary(request, manifest, request)}, tx.NextSeq())
			if err != nil {
				return err
			}
			sem, _ := store.Semantic(tx)
			c, err = sem.Checkpoint(r.CheckpointID)
			return err
		})
		return c
	}
	k1 := checkpoint(i2, "k1", manifest)
	i3, manifest3 := nextRound(t, st, i2, "3", true, k1.ItemID)
	k2 := checkpoint(i3, "k2", manifest3)
	p := i2.Principal
	update(t, st, func(tx store.Tx) error {
		for item, newest := range map[string]bool{k1.ItemID: false, k2.ItemID: true} {
			c, isNewest, err := graph.CheckpointOfItem(tx, p, item, 1, 8)
			if err != nil || c.ItemID != item || isNewest != newest {
				t.Fatalf("CheckpointOfItem(%s): %+v newest=%v, %v", item, c, isNewest, err)
			}
		}
		for item, want := range map[string][]string{"output": {k2.ID, k1.ID}, "output-2": {k2.ID}, "output-3": nil, "F1": nil} {
			got, err := graph.CheckpointsCoveringItem(tx, p, item, 1, 16)
			if err != nil || len(got) != len(want) {
				t.Fatalf("CheckpointsCoveringItem(%s): %+v, %v", item, got, err)
			}
			for n := range got {
				if got[n].ID != want[n] {
					t.Fatalf("CheckpointsCoveringItem(%s) order: %+v", item, got)
				}
			}
		}
		other := p
		other.AgentID = "b"
		if _, _, err := graph.CheckpointOfItem(tx, other, k1.ItemID, 1, 8); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("private checkpoint disclosed: %v", err)
		}
		if _, _, err := graph.CheckpointOfItem(tx, p, "F1", 1, 8); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("non-checkpoint item: %v", err)
		}
		if _, err := graph.CheckpointsCoveringItem(tx, p, "output", 1, 2); !errors.Is(err, domain.ErrResourceLimit) {
			t.Fatalf("bounded lookup truncated instead of failing: %v", err)
		}
		return nil
	})
}
