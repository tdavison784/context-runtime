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
			// The entry refuses runtime IDs, so write the owner's runtime receipt
			// as ingest's runtime path would; the probes must not tell it apart
			// from an absent one (SEC-4.11: otherwise both probes miss).
			insertRuntimeReceipt(t, st, owner, registered)
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

// insertRuntimeReceipt writes a MutationMembership receipt under the
// current-format runtime request ID registered, owned by owner, in the
// transaction that allocates a fresh event sequence for it.
func insertRuntimeReceipt(t *testing.T, st store.Store, owner domain.Principal, registered string) {
	t.Helper()
	if err := st.Update(testContext, "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		rid, err := domain.MutationReceiptKey(owner.SessionID, domain.MutationMembership, registered)
		if err != nil {
			return err
		}
		args := []byte("x")
		h, err := domain.MutationRequestHash(owner, domain.MutationMembership, methodHarnessCheckpoint, args)
		if err != nil {
			return err
		}
		return sem.InsertMutationReceipt(domain.MutationReceipt{
			SemanticMeta: domain.SemanticMeta{ID: rid, SessionID: owner.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
			Family:       domain.MutationMembership, RequestID: registered, Principal: owner, CanonicalMethod: methodHarnessCheckpoint,
			CanonicalArguments: args, RequestHashVersion: domain.RequestHashV3, RequestHash: h, PolicyVersion: "p",
			Result: domain.MutationResult{Records: &domain.RecordResult{Kind: "MEMBERSHIP", IDs: []string{"chk_x"}}},
		})
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.View(testContext, "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		_, err = r.MutationReceipt(domain.MutationMembership, registered)
		return err
	}); err != nil {
		t.Fatalf("runtime receipt not stored: %v", err)
	}
}
