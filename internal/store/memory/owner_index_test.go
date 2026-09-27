package memory

import (
	"context"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestTaskOwnerIndexHoldsCurrentVersionsOnly checks H2 for the memory
// ObligationsByTaskOwner index: replacing an obligation version (insert
// v(n+1), retire v(n) in one transaction) leaves exactly the current
// version indexed, so the read never walks retired history. Before
// orderedIndex removed exact entries, the same-ID add cancelled the
// retirement's removal and every retired version stayed indexed.
func TestTaskOwnerIndexHoldsCurrentVersionsOnly(t *testing.T) {
	s := New()
	ctx := context.Background()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		return tx.InsertObligationVersion(storetest.NewObligation("s", "o", 1, tx.NextSeq(), "src"))
	}); err != nil {
		t.Fatal(err)
	}
	for v := uint64(1); v <= 40; v++ {
		if err := s.Update(ctx, "s", func(tx store.Tx) error {
			if err := tx.InsertObligationVersion(storetest.NewObligation("s", "o", v+1, tx.NextSeq(), "src")); err != nil {
				return err
			}
			_, err := tx.RetireObligationVersion("o", v, 1, storetest.NewLifecycleEvent("s", fmt.Sprintf("retire-%d", v), tx.NextSeq(), domain.TargetObligation, "o"))
			return err
		}); err != nil {
			t.Fatalf("replace v%d: %v", v, err)
		}
	}
	if n := len(s.sessions["s"].st.sem.proof.owners["task"]); n != 1 {
		t.Errorf("task-owner index holds %d entries after 40 replacements, want 1 (the current version)", n)
	}
}
