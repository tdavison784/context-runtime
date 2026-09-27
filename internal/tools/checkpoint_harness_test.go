package tools

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestHarnessCheckpointKeepsHarnessAuthorityAndReplays(t *testing.T) {
	st, s, i2, manifest := checkpointConversation(t, true)
	actor := i2.Principal
	actor.Authority = domain.AuthorityHarness
	intent := summary("harness-1", manifest, "harness compaction")
	var id string
	update(t, st, func(tx store.Tx) error {
		var err error
		if id, err = s.ApplyHarnessCheckpoint(tx, actor, HarnessCheckpointRequest{i2.Principal, i2.ExchangeID, intent}, tx.NextSeq()); err != nil {
			return err
		}
		sem, _ := store.Semantic(tx)
		c, err := sem.Checkpoint(id)
		if err != nil {
			return err
		}
		item, _ := tx.Item(c.ItemID)
		if item.Authority != domain.AuthorityHarness || c.CoveredFrontier != 1 || c.GenerationManifestID != manifest {
			t.Fatalf("harness checkpoint: %+v %+v", c, item)
		}
		before := tx.LastSeq()
		again, err := s.ApplyHarnessCheckpoint(tx, actor, HarnessCheckpointRequest{i2.Principal, i2.ExchangeID, intent}, tx.NextSeq())
		if err != nil || again != id || tx.LastSeq() != before+1 {
			t.Fatalf("replay: %s, %v", again, err)
		}
		return nil
	})
	other := actor
	other.AgentID = "b"
	changed := intent
	changed.Parts = summary("x", manifest, "different").Parts
	for name, tc := range map[string]struct {
		actor  domain.Principal
		intent domain.CheckpointIntent
		want   error
	}{
		"agent actor":   {i2.Principal, summary("h2", manifest, "s"), domain.ErrInvalidAuthorityPromotion},
		"other owner":   {other, summary("h3", manifest, "s"), domain.ErrInvalidAuthorityPromotion},
		"changed retry": {actor, changed, domain.ErrEventIDConflict},
	} {
		err := st.Update(testContext, "s", func(tx store.Tx) error {
			_, err := s.ApplyHarnessCheckpoint(tx, tc.actor, HarnessCheckpointRequest{i2.Principal, i2.ExchangeID, tc.intent}, tx.NextSeq())
			return err
		})
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// SEC-2.8: another principal's derived HARNESS-checkpoint request ID gives
// the same error whether or not its receipt exists (no existence oracle).
func TestHarnessCheckpointDerivedRequestIDIsNoExistenceOracle(t *testing.T) {
	st, s, i2, manifest := checkpointConversation(t, true)
	owner := dispatcher(i2)
	// H5: a runtime request ID binds the event sequence allocated in the
	// transaction that first uses it.
	var registered, absent string
	err := st.Update(testContext, "s", func(tx store.Tx) error {
		seq, occurrence := tx.NextSeq(), domain.CallerOccurrenceID("s", "event-1")
		var err error
		if registered, err = domain.OperationRequestID(owner, owner, occurrence, seq, 1, 0); err != nil {
			return err
		}
		if absent, err = domain.OperationRequestID(owner, owner, occurrence, seq, 2, 0); err != nil {
			return err
		}
		// A harness checkpoint is a caller request, so even its owner can
		// never register one under a runtime ID (SEC-3.7).
		_, err = s.ApplyHarnessCheckpoint(tx, owner, HarnessCheckpointRequest{i2.Principal, i2.ExchangeID, summary(registered, manifest, "owner")}, 0)
		return err
	})
	if !errors.Is(err, domain.ErrInvalidRecord) {
		t.Fatalf("owner registered a checkpoint under a runtime request ID: %v", err)
	}
	b := seedAgentInvocation(t, st, "b")
	probe := func(requestID string) error {
		return st.Update(testContext, "s", func(tx store.Tx) error {
			_, err := s.ApplyHarnessCheckpoint(tx, dispatcher(b), HarnessCheckpointRequest{b.Principal, b.ExchangeID, summary(requestID, manifest, "probe")}, 0)
			return err
		})
	}
	existing, missing := probe(registered), probe(absent)
	if existing == nil || missing == nil || existing.Error() != missing.Error() || errors.Is(existing, domain.ErrEventIDConflict) {
		t.Fatalf("existence oracle: existing=%v missing=%v", existing, missing)
	}
}
