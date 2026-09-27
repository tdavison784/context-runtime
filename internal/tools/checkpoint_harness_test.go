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
