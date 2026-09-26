package ingest

import (
	"os"
	"testing"

	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
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
