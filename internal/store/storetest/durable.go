package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/store"
)

// Opener opens a store over one persistent backing state. Every call opens
// a new store over the same state, so a test can close a store and reopen
// it to check what survives a restart.
type Opener func(t *testing.T) store.Store

// RunDurable runs the restart conformance tests for durable stores. For each
// subtest newBacking returns an Opener over fresh, empty persistent state.
// The suite closes every store it opens; a test closes one store before
// reopening the backing state.
func RunDurable(t *testing.T, newBacking func(t *testing.T) Opener) {
	for _, tc := range durableSuite {
		t.Run(tc.name, func(t *testing.T) {
			tc.fn(t, newBacking(t))
		})
	}
}

type durableCase struct {
	name string
	fn   func(t *testing.T, open Opener)
}

// durableSuite lists every restart conformance test in execution order.
var durableSuite = []durableCase{
	{"LosslessTextAcrossRestart", testLosslessTextAcrossRestart},
	{"IngestionAcrossRestart", testIngestionAcrossRestart},
}

// openDurable opens the backing state and closes the store when the test
// ends (Close is idempotent).
func openDurable(t *testing.T, open Opener) store.Store {
	t.Helper()
	s := open(t)
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s
}

// reopen closes s and opens the backing state again.
func reopen(t *testing.T, s store.Store, open Opener) store.Store {
	t.Helper()
	noErr(t, s.Close())
	return openDurable(t, open)
}
