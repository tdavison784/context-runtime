package obligation

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// TestObligationEntriesRefuseReservedRequestIDs_SEC37: a caller of the
// obligation *Tx entry points can never name a reserved runtime namespace.
func TestObligationEntriesRefuseReservedRequestIDs_SEC37(t *testing.T) {
	f := newFixture(t)
	for n, id := range []string{"gc_x", "gcq_x", "evt_x", "itm_x"} {
		in := bindIntent("ws-r"+string(rune('a'+n)), 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"})
		in.RequestID = id
		if _, err := f.s.bindWS(t, f.st, f.harness, in); !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("BindWorkspaceTx accepted reserved request ID %q: %v", id, err)
		}
	}
	if _, err := f.s.bindWS(t, f.st, f.harness, bindIntent("ws-ok", 1, domain.WorkspaceSourceContext{Kind: domain.WorkspaceTask, ID: "task"})); err != nil {
		t.Fatalf("control: %v", err)
	}
}
