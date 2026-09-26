package ingest

import (
	"fmt"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestConcurrentSupersession (SPEC-1.10, SDD section 10, FR-DIR-002): n
// concurrent events that each replace [p] with different text serialize
// into one supersession chain on both stores: exactly one current pin, one
// SUPERSEDES edge per replacement, every old version retired exactly once.
func TestConcurrentSupersession(t *testing.T) {
	eachStore(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		f.mustIngest(user, userEvent("p0", "## Pinned\n- [p] version 0\n", true))
		const n = 8
		var wg sync.WaitGroup
		for i := range n {
			wg.Go(func() {
				if _, err := f.in.Ingest(ctx, f.s, user, userEvent(fmt.Sprintf("p%d", i+1), fmt.Sprintf("## Pinned\n- [p] version %d\n", i+1), true)); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if pins := currentIDs(t, f.s, domain.KindConstraint); len(pins) != 1 {
			t.Fatalf("current pins = %v, want exactly one", pins)
		}
		f.view(func(tx store.ReadTx) error {
			edges, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes})
			if err != nil {
				return err
			}
			retired := map[string]int{}
			for _, e := range edges {
				retired[e.ToID]++
			}
			if len(edges) != n || len(retired) != n {
				t.Errorf("SUPERSEDES edges = %d over %d targets, want %d each", len(edges), len(retired), n)
			}
			return nil
		})
	})
}
