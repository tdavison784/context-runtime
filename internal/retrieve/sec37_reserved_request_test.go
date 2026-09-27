package retrieve

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestRetrievalRefusesReservedRequestIDs_SEC37: a new retrieval request can
// never name a reserved runtime namespace.
func TestRetrievalRefusesReservedRequestIDs_SEC37(t *testing.T) {
	owner := storetest.NewPrincipal("s", domain.AuthorityHarness)
	for _, id := range []string{"gc_x", "gcq_x", "evt_x", "itm_x"} {
		i := AdmissionIntent{Rehydrate: domain.RehydrateIntent{RequestID: id, ItemID: "source"},
			Origin: domain.RetrievalOrigin{Holder: owner, ConversationID: domain.ConversationIDFor(owner.TaskID, owner.AgentID), TurnID: "turn"}, Method: "rehydrate"}
		if _, _, err := replayRetrieval(replayReader{}, owner, i, leasePolicy()); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("retrieval accepted reserved request ID %q: %v", id, err)
		}
	}
}
