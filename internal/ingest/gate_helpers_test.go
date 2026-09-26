package ingest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

// pending marks a Phase 3 gate test whose contract or service dependency has
// not landed on this branch. It is skipped with a GATE-PENDING line naming
// the dependency, so the inventory is visible in -v output; set
// CR_GATE_PENDING=1 to run it anyway and see its current failure. A pending
// test is never gate evidence.
func pending(t *testing.T, dependency string) {
	t.Helper()
	if os.Getenv("CR_GATE_PENDING") == "" {
		t.Skipf("GATE-PENDING: needs %s", dependency)
	}
}

// isCurrent reports whether itemID is the current version of its key.
func (f *fixture) isCurrent(itemID string) bool {
	f.t.Helper()
	var cur bool
	f.view(func(tx store.ReadTx) error {
		var err error
		cur, err = graph.IsCurrent(tx, itemID)
		return err
	})
	return cur
}

// semanticStores runs fn under the test Phase 3 policy against a fresh
// memory store and a fresh SQLite store, with W3's real lifecycle service.
// A store that does not yet implement the Phase 3 semantic facet (before
// W2's backends land) is skipped with a GATE-PENDING line; the moment it
// does, the test runs with no change here.
func semanticStores(t *testing.T, fn func(t *testing.T, f *fixture)) {
	t.Helper()
	run := func(t *testing.T, s store.Store) {
		if !hasSemantic(s) {
			t.Skip("GATE-PENDING: needs " + depW2)
		}
		f := newFixture(t, s)
		pol := testPolicy()
		f.in.Semantic = &pol
		fn(t, f)
	}
	t.Run("memory", func(t *testing.T) {
		ms := memory.New()
		t.Cleanup(func() { ms.Close() })
		run(t, ms)
	})
	t.Run("sqlite", func(t *testing.T) {
		ss, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "gate.db"))
		if err != nil {
			t.Fatalf("sqlite.Open: %v", err)
		}
		t.Cleanup(func() { ss.Close() })
		run(t, ss)
	})
}
