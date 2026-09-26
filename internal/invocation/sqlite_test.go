package invocation

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

// TestSQLiteRestartBetweenSentAndRecover covers T10 step 3 against a real
// file: the process dies after SENT is durable, the database is reopened,
// and recovery finds the call UNKNOWN rather than resending it.
func TestSQLiteRestartBetweenSentAndRecover(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ledger.db")
	s, err := sqlite.Open(ctx, path)
	must(t, err)
	l := newLedger(s)
	c, err := l.Prepare(ctx, request(agentA, "C1", 1, 0, 1))
	must(t, err)
	_, err = l.MarkSent(ctx, harness, c.CallID, "prov-req-1")
	must(t, err)
	must(t, s.Close())

	s, err = sqlite.Open(ctx, path)
	must(t, err)
	t.Cleanup(func() { s.Close() })
	l = newLedger(s)
	unknown, err := l.Recover(ctx, harness)
	must(t, err)
	if len(unknown) != 1 || unknown[0].CallID != c.CallID || unknown[0].State != domain.CallUnknown {
		t.Fatalf("recovered = %+v", unknown)
	}
	_, err = l.MarkSent(ctx, harness, c.CallID, "prov-req-2")
	wantErr(t, err, domain.ErrInvalidTransition)
	conv := conversation(t, s, agentA)
	if conv.Version != 1 || conv.Epoch != 0 || conv.InFlightCallID != c.CallID {
		t.Fatalf("after restart: %+v", conv)
	}

	// Reconcile, restart after commit, and record the outcome again.
	r1 := completed(1, "R1")
	done, err := l.RecordOutcome(ctx, harness, c.CallID, r1)
	must(t, err)
	must(t, s.Close())
	s, err = sqlite.Open(ctx, path)
	must(t, err)
	t.Cleanup(func() { s.Close() })
	l = newLedger(s)
	before := lastSeq(t, s)
	dup, err := l.RecordOutcome(ctx, harness, c.CallID, r1)
	must(t, err)
	if dup.Revision != done.Revision || lastSeq(t, s) != before {
		t.Fatalf("duplicate outcome after restart changed state: %+v", dup)
	}
	if conv := conversation(t, s, agentA); conv.Version != 2 || conv.Epoch != 1 || conv.LogicalCalls != 1 || conv.InFlightCallID != "" {
		t.Fatalf("after reconciliation: %+v", conv)
	}
	want := []string{">PREPARED:prepare", "PREPARED>SENT:send", "SENT>UNKNOWN:recover", "UNKNOWN>COMPLETED:outcome"}
	if got := transitions(callEvents(t, s, c.CallID)); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

// TestSQLiteRandomizedInvariants runs the randomized invariant check on the
// SQLite store.
func TestSQLiteRandomizedInvariants(t *testing.T) {
	for seed := range uint64(3) {
		t.Run(fmt.Sprint("seed", seed), func(t *testing.T) {
			s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"))
			must(t, err)
			t.Cleanup(func() { s.Close() })
			runRandomOn(t, newLedger(s), s, seed, 150)
		})
	}
}

// TestSQLiteConcurrentPrepare covers T10 step 2 across real SQLite
// transactions.
func TestSQLiteConcurrentPrepare(t *testing.T) {
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "ledger.db"))
	must(t, err)
	t.Cleanup(func() { s.Close() })
	l := newLedger(s)
	const n = 16
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = l.Prepare(ctx, request(agentA, fmt.Sprintf("r%d", i), 1, 0, 0))
		}()
	}
	wg.Wait()
	wins := 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, domain.ErrCallInFlight), errors.Is(err, domain.ErrVersionConflict):
		default:
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d reservations, want 1", wins)
	}
}
