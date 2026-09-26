package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
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
	{"Phase2StateAcrossRestart", testPhase2StateAcrossRestart},
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

// testPhase2StateAcrossRestart checks that item provenance, namespaced
// current-version pointers, obligation claims, and retirements survive a
// restart (D8, D13, D18, M6).
func testPhase2StateAcrossRestart(t *testing.T, open Opener) {
	s := openDurable(t, open)
	var transcript, pin domain.ContextItem
	var retired domain.ObligationVersion
	update(t, s, sessA, func(tx store.Tx) error {
		transcript = NewTranscript(sessA, "tr", tx.NextSeq(), "# Pinned\n- x\n")
		noErr(t, tx.InsertItem(transcript))
		pin = NewDirective(sessA, "pin", "agent.status", tx.NextSeq(), "x")
		pin.CreatedTurn = 1
		pin.SourceRanges = []domain.SourceRange{{TranscriptID: "tr", Range: domain.ByteRange{Start: 10, End: 13}, Slices: []domain.ByteRange{{Start: 12, End: 13}}}}
		noErr(t, tx.InsertItem(pin))
		noErr(t, tx.InsertItem(NewAgentKeyItem(sessA, "key", "agent.status", tx.NextSeq(), "state")))
		noErr(t, tx.SetCurrentVersion("pin"))
		noErr(t, tx.SetCurrentVersion("key"))
		o := NewObligation(sessA, "o", 1, tx.NextSeq(), "pin")
		o.Claim, o.Matcher = "tests_pass", nil
		noErr(t, tx.InsertObligationVersion(o))
		var err error
		retired, err = tx.RetireObligationVersion("o", 1, 1, NewLifecycleEvent(sessA, "retire", tx.NextSeq(), domain.TargetObligation, "o"))
		return err
	})
	s = reopen(t, s, open)
	view(t, s, sessA, func(tx store.ReadTx) error {
		for _, want := range []domain.ContextItem{transcript, pin} {
			got, err := tx.Item(want.ID)
			noErr(t, err)
			assertEqual(t, "item "+want.ID+" after restart", got, want)
		}
		for ns, want := range map[domain.DirectiveNamespace]string{domain.NamespaceDirective: "pin", domain.NamespaceAgentKey: "key"} {
			got, err := tx.CurrentVersion(namespaceKey(sessA, ns, "agent.status"))
			noErr(t, err)
			if got != want {
				t.Errorf("CurrentVersion(%s) after restart = %q, want %q", ns, got, want)
			}
		}
		got, err := tx.ObligationsBySource("pin", 1)
		noErr(t, err)
		assertEqual(t, "retired obligation after restart", got, []domain.ObligationVersion{retired})
		return nil
	})
}
