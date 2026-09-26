package tools

import (
	"path/filepath"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

// The whole membership → keyed write → checkpoint path persists on SQLite,
// and every receipt replays unchanged after reopen without a new sequence.
func TestToolReceiptsAndCheckpointSurviveSQLiteReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tools.db")
	st, err := sqlite.Open(testContext, path)
	if err != nil {
		t.Fatal(err)
	}
	i := seedToolFixture(t, st)
	_, s, i2, manifest := checkpointConversationOn(t, st, i, true)
	intent := summary("c1", manifest, "sqlite summary")
	var first domain.ToolResult
	update(t, st, func(tx store.Tx) error {
		var err error
		first, err = s.CreateCheckpoint(tx, i2, intent)
		return err
	})
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err = sqlite.Open(testContext, path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	update(t, st, func(tx store.Tx) error {
		before := tx.LastSeq()
		again, err := s.CreateCheckpoint(tx, i2, intent)
		if err != nil || again != first || tx.LastSeq() != before {
			t.Fatalf("checkpoint replay: %+v, %v", again, err)
		}
		keyedAgain, err := s.Remember(tx, i, keyed("r1", "db", "postgres"))
		if err != nil || keyedAgain.Keyed == nil || tx.LastSeq() != before {
			t.Fatalf("keyed replay: %+v, %v", keyedAgain, err)
		}
		sem, _ := store.Semantic(tx)
		c, err := sem.Checkpoint(first.CheckpointID)
		if err != nil || c.CoveredFrontier != 1 {
			t.Fatalf("reopened checkpoint: %+v, %v", c, err)
		}
		members, err := sem.CoverageMembers(c.CoveredExchangesID, store.Page{Limit: 4})
		if err != nil || len(members.Records) != 1 || members.Records[0].ExchangeID != i.ExchangeID {
			t.Fatalf("reopened coverage: %+v, %v", members, err)
		}
		return nil
	})
}
