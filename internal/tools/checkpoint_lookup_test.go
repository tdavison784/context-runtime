package tools

import (
	"errors"
	"strconv"
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

// SPEC-2.7 (H2): the covering-checkpoint lookup's cost is independent of the
// conversation's length: one fixed work limit answers for 3 rounds and 14.
func TestCheckpointCoveringLookupCostIsIndependentOfConversationLength(t *testing.T) {
	for _, rounds := range []int{3, 14} {
		st, i := toolFixture(t)
		s := testService(t)
		remember(t, st, s, i, keyed("r1", "db", "postgres"))
		cur := i
		for n := 2; n <= rounds; n++ {
			cur, _ = nextRound(t, st, cur, strconv.Itoa(n), true)
			if n < rounds {
				runTool(t, st, func(tx store.Tx) (domain.ToolResult, error) {
					return s.UpdateState(tx, dispatcher(cur), Request[domain.KeyedWriteIntent]{cur, domain.KeyedWriteIntent{RequestID: "s" + strconv.Itoa(n), Key: "progress", Kind: domain.KindTaskState, Parts: keyed("", "", strconv.Itoa(n)).Parts}}, 0)
				})
			}
		}
		var manifest string
		update(t, st, func(tx store.Tx) error {
			sem, _ := store.Semantic(tx)
			m, err := sem.AdmissionsByExchange(prevExchange(t, tx, cur), store.Page{Limit: 4})
			if err == nil && len(m.Records) == 1 {
				manifest = m.Records[0].ID
			}
			return err
		})
		k := runTool(t, st, func(tx store.Tx) (domain.ToolResult, error) {
			return s.CreateCheckpoint(tx, dispatcher(cur), Request[domain.CheckpointIntent]{cur, summary("k", manifest, "summary")}, 0)
		})
		update(t, st, func(tx store.Tx) error {
			got, err := graph.CheckpointsCoveringItem(tx, cur.Principal, "output", 1, 6)
			if err != nil || len(got) != 1 || got[0].ID != k.CheckpointID {
				t.Fatalf("%d rounds: %+v, %v", rounds, got, err)
			}
			return nil
		})
	}
}

// prevExchange returns the closed round just before the issuing one.
func prevExchange(t *testing.T, tx store.Tx, issuing domain.ToolInvocation) string {
	t.Helper()
	sem, _ := store.Semantic(tx)
	x, err := sem.LogicalExchange(issuing.ExchangeID)
	if err != nil {
		t.Fatal(err)
	}
	all, _ := sem.ExchangesByConversation(x.ConversationID, store.Page{Limit: 64})
	for _, e := range all.Records {
		if e.Ordinal == x.Ordinal-1 {
			return e.ID
		}
	}
	t.Fatal("no previous round")
	return ""
}
