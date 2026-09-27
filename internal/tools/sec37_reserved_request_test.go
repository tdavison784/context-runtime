package tools

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestToolsRefuseReservedRequestIDs_SEC37 (H5 conformance): a tool caller
// can never name a reserved runtime namespace as its request ID, including
// a runtime req_ ID derived for itself.
func TestToolsRefuseReservedRequestIDs_SEC37(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	// A refused call writes nothing, so the same tool call stays open for
	// every attempt and for the control.
	for n, id := range []string{"gc_x", "gcq_x", "evt_x", "itm_x", "REQ"} {
		err := st.Update(testContext, "s", func(tx store.Tx) error {
			seq := tx.NextSeq()
			rid := id
			if id == "REQ" {
				rid, _ = domain.OperationRequestID(i.Principal, i.Principal, domain.CallerOccurrenceID("s", "mine"), seq, 0, 0)
			}
			_, err := s.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, keyed(rid, "k"+string(rune('a'+n)), "v")}, seq)
			return err
		})
		if err == nil {
			t.Errorf("tool accepted caller request ID in reserved namespace %q", id)
		}
	}
	if err := st.Update(testContext, "s", func(tx store.Tx) error {
		_, err := s.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, keyed("req-ok", "kz", "v")}, tx.NextSeq())
		return err
	}); err != nil && !errors.Is(err, domain.ErrInvalidRecord) {
		t.Fatalf("control: %v", err)
	} else if err != nil {
		t.Fatalf("control refused: %v", err)
	}
}
