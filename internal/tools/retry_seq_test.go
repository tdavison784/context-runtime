package tools

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func lastSeq(t *testing.T, st store.Store) uint64 {
	t.Helper()
	var seq uint64
	update(t, st, func(tx store.Tx) error { seq = tx.LastSeq(); return nil })
	return seq
}

// FR-ING-006: an identical retry through the outer boundaries writes nothing
// and consumes no sequence; the operation sequence is allocated only after
// the replay check.
func TestExactToolRetryLeavesLastSeqUnchanged(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	remember := func() domain.ToolResult {
		r, err := Execute(testContext, st, "s", func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
			return s.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, keyed("retry", "db", "postgres")}, seq)
		})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	first := remember()
	before := lastSeq(t, st)
	if again := remember(); !reflect.DeepEqual(again, first) || lastSeq(t, st) != before {
		t.Fatalf("keyed retry: %+v, LastSeq %d -> %d", again, before, lastSeq(t, st))
	}

	st, s, ri := retrievalFixture(t)
	r := Request[domain.RehydrateIntent]{ri, rehydrate("get", "history")}
	got, err := s.RunRetrieval(testContext, st, dispatcher(ri), r, MethodGet)
	if err != nil {
		t.Fatal(err)
	}
	before = lastSeq(t, st)
	if again, err := s.RunRetrieval(testContext, st, dispatcher(ri), r, MethodGet); err != nil || again != got || lastSeq(t, st) != before {
		t.Fatalf("retrieval retry: %+v, %v, LastSeq %d -> %d", again, err, before, lastSeq(t, st))
	}

	// A HARNESS checkpoint retry with a deferred sequence is also free.
	st, s, i2, manifest := checkpointConversation(t, true)
	actor := dispatcher(i2)
	req := HarnessCheckpointRequest{i2.Principal, i2.ExchangeID, summary("h", manifest, "harness")}
	var id string
	update(t, st, func(tx store.Tx) error {
		var err error
		id, err = s.ApplyHarnessCheckpoint(tx, actor, req, 0)
		return err
	})
	before = lastSeq(t, st)
	update(t, st, func(tx store.Tx) error {
		again, err := s.ApplyHarnessCheckpoint(tx, actor, req, 0)
		if err != nil || again != id || tx.LastSeq() != before {
			t.Fatalf("harness checkpoint retry: %s, %v, LastSeq %d -> %d", again, err, before, tx.LastSeq())
		}
		return nil
	})
}
