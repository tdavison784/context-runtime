package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func TestReadInvocationRequiresCompleteOutputAndToolCallMembership(t *testing.T) {
	s, invocation := toolFixture(t)
	update(t, s, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		state, err := readInvocation(tx, sem, invocation, testPolicy())
		if err != nil || state.nextPosition != 3 || state.output.ItemID != "output" {
			t.Fatalf("authenticated state: %+v, %v", state, err)
		}
		for _, change := range []string{"missing", "private", "tool", "bound"} {
			i, policy := invocation, testPolicy()
			switch change {
			case "missing":
				i.ExchangeID = "missing"
			case "private":
				i.Principal.WorkflowID = "other"
			case "tool":
				i.ToolCallID = "other"
			case "bound":
				policy.MaxCoverageMembers = 1
			}
			_, err := readInvocation(tx, sem, i, policy)
			if err == nil {
				t.Fatalf("%s accepted", change)
			}
			if (change == "missing" || change == "private") && FixedError(err).Error() != domain.ToolErrorNotFound.Message() {
				t.Fatalf("%s disclosed ownership: %v", change, err)
			}
		}
		return nil
	})
}
