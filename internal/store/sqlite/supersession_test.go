package sqlite

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestSupersessionCycleCheckIsLocal checks that the cycle check for a new
// SUPERSEDES edge walks only the chain reachable from it (SPEC-2.1): a new
// version with no incoming SUPERSEDES edge visits nothing however many
// unrelated chains the session holds, and a real cycle is still found
// after visiting only its own chain.
func TestSupersessionCycleCheckIsLocal(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		for i := range 200 { // unrelated chains
			a, b := fmt.Sprintf("a%d", i), fmt.Sprintf("b%d", i)
			for _, id := range []string{a, b} {
				if err := tx.InsertItem(storetest.NewItem("s", id, tx.NextSeq(), id)); err != nil {
					return err
				}
			}
			if err := tx.InsertRelationship(storetest.NewRelationship("s", "e"+a, domain.RelSupersedes, b, a, tx.NextSeq())); err != nil {
				return err
			}
		}
		for _, id := range []string{"v1", "v2", "v3"} {
			if err := tx.InsertItem(storetest.NewItem("s", id, tx.NextSeq(), id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		inner := tx.(*store.Guard).TxBase.(*transaction)
		// Measured from outside the check (SPEC-3.2): the rows every query
		// of the insert read, not the walk's own count.
		inner.rowsRead = 0
		if err := tx.InsertRelationship(storetest.NewRelationship("s", "probe", domain.RelSupersedes, "v2", "v1", tx.NextSeq())); err != nil {
			return err
		}
		if inner.rowsRead > 2 {
			t.Errorf("inserting a new version's SUPERSEDES edge read %d rows, want at most 2", inner.rowsRead)
		}
		for _, e := range [][2]string{{"v3", "v2"}} {
			cycle, visited, err := inner.closesSupersessionCycle(e[0], e[1])
			if err != nil || cycle || visited != 0 {
				t.Errorf("%s -> %s: cycle=%v visited=%d err=%v; want no cycle and no walk", e[0], e[1], cycle, visited, err)
			}
			if err := tx.InsertRelationship(storetest.NewRelationship("s", e[0]+"-"+e[1], domain.RelSupersedes, e[0], e[1], tx.NextSeq())); err != nil {
				return err
			}
		}
		cycle, visited, err := inner.closesSupersessionCycle("v1", "v3")
		if err != nil || !cycle || visited > 3 {
			t.Errorf("v1 -> v3: cycle=%v visited=%d err=%v; want a cycle after at most 3 visits", cycle, visited, err)
		}
		if err := tx.InsertRelationship(storetest.NewRelationship("s", "v1-v3", domain.RelSupersedes, "v1", "v3", tx.NextSeq())); !errors.Is(err, domain.ErrSupersessionCycle) {
			t.Errorf("closing the cycle: %v, want ErrSupersessionCycle", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
