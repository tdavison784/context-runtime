package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestOpenAndMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	s, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestConformance(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		s, err := Open(context.Background(), filepath.Join(t.TempDir(), "state.db"))
		if err != nil {
			t.Fatal(err)
		}
		return s
	})
}

func TestDurableConformance(t *testing.T) {
	storetest.RunDurable(t, func(t *testing.T) storetest.Opener {
		path := filepath.Join(t.TempDir(), "state.db")
		return func(t *testing.T) store.Store {
			s, err := Open(context.Background(), path)
			if err != nil {
				t.Fatal(err)
			}
			return s
		}
	})
}
