package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_27_PlainSummaryIsNeverACheckpoint (P3-27, ADR 8 :1373): a plain
// kind=summary item is never treated as a checkpoint. The existing subtest in
// checkpoint_test.go ("plain summary is not a checkpoint") asserts this on
// the default store only; this is the dedicated both-stores test with the
// same assertion shape, plus a positive control: a real checkpoint created in
// the same conversation IS listed, and the listing names only it — never the
// plain summary sitting in the same conversation.
func TestP3_27_PlainSummaryIsNeverACheckpoint(t *testing.T) {
	p342bEachStore(t, func(t *testing.T, st store.Store) {
		i := seedToolFixture(t, st)
		st, s, i2, manifest := checkpointConversationOn(t, st, i, true)
		var real domain.ToolResult
		update(t, st, func(tx store.Tx) error {
			var err error
			real, err = s.CreateCheckpoint(tx, dispatcher(i2), Request[domain.CheckpointIntent]{i2, summary("p27c-real", manifest, "real checkpoint")}, tx.NextSeq())
			return err
		})
		if real.CheckpointID == "" {
			t.Fatalf("control checkpoint missing: %+v", real)
		}
		update(t, st, func(tx store.Tx) error {
			// The probe: a plain kind=summary item in the same conversation,
			// no Role, no checkpoint record — exactly as the original subtest
			// seeds it.
			plain := storetest.NewItem("s", "p27c-plain", tx.NextSeq(), "summary")
			plain.Kind = domain.KindSummary
			if err := tx.InsertItem(plain); err != nil {
				return err
			}
			sem, _ := store.Semantic(tx)
			page, err := sem.CheckpointsByConversation(i2.Principal, i2.ConversationID, store.Page{Limit: 4})
			if err != nil {
				return err
			}
			if len(page.Records) != 1 {
				t.Fatalf("checkpoints = %+v, want only the real one", page.Records)
			}
			if page.Records[0].ItemID == plain.ID {
				t.Fatalf("plain summary %s listed as a checkpoint: %+v", plain.ID, page.Records)
			}
			it, err := tx.Item(page.Records[0].ItemID)
			if err != nil || it.ID == plain.ID || it.Role != domain.RoleCheckpoint {
				t.Fatalf("listed item = %+v %v, want the Role=CHECKPOINT item", it, err)
			}
			return nil
		})
	})
}
