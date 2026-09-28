package tools

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

// p24Stores runs fn on the memory store and W2's SQLite backend, the two
// stores this package's receipts must survive on.
func p24Stores(t *testing.T, fn func(t *testing.T, st store.Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		st := memory.New()
		t.Cleanup(func() { st.Close() })
		fn(t, st)
	})
	t.Run("sqlite", func(t *testing.T) {
		st, err := sqlite.Open(testContext, filepath.Join(t.TempDir(), "p24.db"))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { st.Close() })
		fn(t, st)
	})
}

// TestP3_24_SameInvocationDifferentToolPrincipalOrArgsConflicts: one
// invocation's committed request replays only itself. Retrying the same
// request ID as a different tool (context_update_state for a committed
// context_remember), through a principal the invocation does not name (the
// same invocation identity — its ID excludes the principal — carrying
// another workflow), or with different arguments, conflicts instead of
// replaying or executing. Every conflict surfaces as the same closed
// ToolErrorConflict with no underlying diagnostics, commits nothing, and
// leaves the original request still replayable exactly as first executed.
func TestP3_24_SameInvocationDifferentToolPrincipalOrArgsConflicts(t *testing.T) {
	p24Stores(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		i := seedToolFixture(t, st)
		first := remember(t, st, s, i, keyed("p24", "db", "postgres"))
		before := lastSeq(t, st)

		run := func(call func(tx store.Tx, seq uint64) (domain.ToolResult, error)) error {
			_, err := Execute(testContext, st, "s", call)
			return err
		}
		rememberCall := func(invocation domain.ToolInvocation, intent domain.KeyedWriteIntent) func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
			return func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
				return s.Remember(tx, dispatcher(invocation), Request[domain.KeyedWriteIntent]{invocation, intent}, seq)
			}
		}
		var texts []string
		for name, err := range map[string]error{
			// Same request ID committed as context_remember, retried as
			// context_update_state.
			"tool": run(func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
				return s.UpdateState(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, keyed("p24", "db", "postgres")}, seq)
			}),
			// The same invocation identity under a principal of another
			// workflow: the ID excludes the principal, so the committed
			// receipt is found and must refuse the stranger.
			"principal": func() error {
				foreign := i
				foreign.Principal.WorkflowID = "p24-other"
				return run(rememberCall(foreign, keyed("p24", "db", "postgres")))
			}(),
			// The same tool and principal, different arguments.
			"args":       run(rememberCall(i, keyed("p24", "db2", "postgres"))),
			"args again": run(rememberCall(i, keyed("p24", "db", "mysql"))),
		} {
			var te *Error
			if !errors.As(err, &te) || te.Code() != domain.ToolErrorConflict {
				t.Errorf("%s: %v, want closed %s", name, err, domain.ToolErrorConflict)
				continue
			}
			texts = append(texts, err.Error())
		}
		for _, text := range texts {
			if text != domain.ToolErrorConflict.Message() {
				t.Fatalf("conflict errors differ or leak diagnostics: %q", texts)
			}
		}
		if lastSeq(t, st) != before {
			t.Fatalf("conflicting retries wrote state: LastSeq %d -> %d", before, lastSeq(t, st))
		}
		if !current(t, st, first.ItemID) {
			t.Fatal("conflicting retries disturbed the committed filing")
		}

		// The identical retry still replays the frozen result of the one
		// committed execution, consuming no sequence.
		again, err := Execute(testContext, st, "s", rememberCall(i, keyed("p24", "db", "postgres")))
		if err != nil || again.Keyed == nil || *again.Keyed != first {
			t.Fatalf("identical retry after conflicts: %+v, %v (want %+v)", again.Keyed, err, first)
		}
		if lastSeq(t, st) != before {
			t.Fatalf("identical retry consumed a sequence: LastSeq %d -> %d", before, lastSeq(t, st))
		}
	})
}
