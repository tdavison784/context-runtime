package sqlite_test

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestParityWithMemoryAcrossRestart writes the same Phase 3 history to the
// memory store and to SQLite, restarts SQLite, and requires both to read
// back the same logical state (FR-PER-001/003).
func TestParityWithMemoryAcrossRestart(t *testing.T) {
	var path string
	storetest.CheckParity(t,
		func(t *testing.T) store.Store { return memory.New() },
		func(t *testing.T) store.Store {
			path = sqlitetest.Path(t)
			s, err := sqlite.Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		func(t *testing.T, s store.Store) store.Store {
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := sqlite.Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			return reopened
		})
}
