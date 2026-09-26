package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// The package's own tests cannot import sqlitetest (it imports this
// package), so they share the same template approach here: migrate once
// per test binary, then give each store a private copy of the bytes.
var (
	templateOnce  sync.Once
	templateBytes []byte
	templateErr   error
)

// freshPath returns a fresh, fully migrated database file for t.
func freshPath(t testing.TB) string {
	t.Helper()
	templateOnce.Do(func() {
		dir, err := os.MkdirTemp("", "sqlite-template-")
		if err != nil {
			templateErr = err
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "template.db")
		s, err := Open(context.Background(), path)
		if err != nil {
			templateErr = err
			return
		}
		if templateErr = s.Close(); templateErr != nil {
			return
		}
		templateBytes, templateErr = os.ReadFile(path)
	})
	if templateErr != nil {
		t.Fatalf("migrate template: %v", templateErr)
	}
	path := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(path, templateBytes, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
