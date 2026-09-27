package tools

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// DUR-1.8: tool and HARNESS checkpoint receipts replay after a later policy
// lowers MaxMetadataBytes; the lower limit applies only to new requests.
func TestToolReceiptsReplayAfterLowerLimit(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	intent := keyed("r", "db", "postgres")
	first := runTool(t, st, func(tx store.Tx) (domain.ToolResult, error) {
		return s.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, intent}, 0)
	})
	lowered := *s
	lowered.policy.MaxMetadataBytes = 16
	again := runTool(t, st, func(tx store.Tx) (domain.ToolResult, error) {
		return lowered.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, intent}, 0)
	})
	if !reflect.DeepEqual(again, first) {
		t.Fatalf("tool replay under a lower limit: %+v", again)
	}

	st, s, i2, manifest := checkpointConversation(t, true)
	req := HarnessCheckpointRequest{i2.Principal, i2.ExchangeID, summary("h", manifest, "harness summary")}
	var id string
	update(t, st, func(tx store.Tx) error {
		var err error
		id, err = s.ApplyHarnessCheckpoint(tx, dispatcher(i2), req, 0)
		return err
	})
	lowered = *s
	lowered.policy.MaxMetadataBytes = 16
	update(t, st, func(tx store.Tx) error {
		again, err := lowered.ApplyHarnessCheckpoint(tx, dispatcher(i2), req, 0)
		if err != nil || again != id {
			t.Fatalf("harness replay under a lower limit: %s, %v", again, err)
		}
		return nil
	})
}
