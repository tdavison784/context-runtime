package tools

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// NEW variant of SEC-2.8 on the HARNESS checkpoint: an oversized probe.
func TestOversizedCheckpointProbeIsNotAnOracle_SEC35(t *testing.T) {
	for _, big := range []bool{false, true} {
		securityStores(t, func(t *testing.T, st store.Store) {
			_, s, i2, manifest := checkpointConversationOn(t, st, seedToolFixture(t, st), true)
			owner := dispatcher(i2)
			var registered, absent string
			err := st.Update(testContext, "s", func(tx store.Tx) error {
				seq, occ := tx.NextSeq(), domain.CallerOccurrenceID("s", "event-1")
				registered, _ = domain.OperationRequestID(owner, owner, occ, seq, 1, 0)
				absent, _ = domain.OperationRequestID(owner, owner, occ, seq, 2, 0)
				// Registering under a runtime ID is refused (SEC-3.7); the probes
				// below must still be indistinguishable.
				_, err := s.ApplyHarnessCheckpoint(tx, owner, HarnessCheckpointRequest{i2.Principal, i2.ExchangeID, summary(registered, manifest, "owner")}, 0)
				return err
			})
			if !errors.Is(err, domain.ErrInvalidRecord) {
				t.Fatalf("owner registered a checkpoint under a runtime request ID: %v", err)
			}
			b := seedAgentInvocation(t, st, "b")
			text := "probe"
			if big {
				text = strings.Repeat("x", 1<<17)
			}
			probe := func(requestID string) error {
				return st.Update(testContext, "s", func(tx store.Tx) error {
					_, err := s.ApplyHarnessCheckpoint(tx, dispatcher(b), HarnessCheckpointRequest{b.Principal, b.ExchangeID, summary(requestID, manifest, text)}, 0)
					return err
				})
			}
			existing, missing := probe(registered), probe(absent)
			t.Logf("big=%v existing=%v missing=%v", big, existing, missing)
			if existing == nil || missing == nil || existing.Error() != missing.Error() {
				t.Errorf("ORACLE (big=%v)", big)
			}
		})
	}
}

func securityStores(t *testing.T, f func(*testing.T, store.Store)) {
	t.Run("memory", func(t *testing.T) { s := memory.New(); t.Cleanup(func() { s.Close() }); f(t, s) })
	t.Run("sqlite", func(t *testing.T) { f(t, sqlitetest.Open(t)) })
}
