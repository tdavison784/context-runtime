// Package sqlitetest opens fresh SQLite stores for tests without replaying
// every migration per store. The first call migrates one template database
// per test binary; each later store is a private copy of its bytes, so a
// test pays for migrations once (seconds under the race detector, where the
// transpiled SQLite engine re-parses the schema for every ALTER TABLE)
// instead of once per store. Open still verifies every migration checksum.
package sqlitetest

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

var (
	once     sync.Once
	template []byte
	failure  error
)

// migrated returns the bytes of a fully migrated, empty database.
func migrated() ([]byte, error) {
	once.Do(func() {
		dir, err := os.MkdirTemp("", "sqlitetest-")
		if err != nil {
			failure = err
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "template.db")
		s, err := sqlite.Open(context.Background(), path)
		if err != nil {
			failure = err
			return
		}
		// Closing the last connection checkpoints the WAL into the file.
		if failure = s.Close(); failure != nil {
			return
		}
		template, failure = os.ReadFile(path)
	})
	return template, failure
}

// Path returns the path of a fresh, fully migrated database file (mode
// 0600) in a directory the test removes when it ends.
func Path(t testing.TB) string {
	t.Helper()
	b, err := migrated()
	if err != nil {
		t.Fatalf("sqlitetest: migrate template: %v", err)
	}
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Open returns a store on a fresh copy of the migrated template, closed
// when the test ends.
func Open(t testing.TB) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(context.Background(), Path(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
