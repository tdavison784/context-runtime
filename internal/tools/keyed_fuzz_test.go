package tools

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Model-supplied key, kind and text never choose owner, authority, boundary or
// namespace, and failures are closed templates that echo no input.
func FuzzKeyedWriteInputs(f *testing.F) {
	for _, seed := range [][3]string{{"db", "fact", "postgres"}, {".x", "fact", "t"}, {"a..b", "decision", "t"}, {"k", "goal", "t"}, {"k", "task_state", ""}, {strings.Repeat("k", 75), "fact", "t"}, {"agent.k", "fact", "\x00"}} {
		f.Add(seed[0], seed[1], seed[2])
	}
	closed := map[string]bool{}
	for _, c := range []domain.ToolErrorCode{domain.ToolErrorNotFound, domain.ToolErrorInvalidArgument, domain.ToolErrorConflict, domain.ToolErrorUnavailable, domain.ToolErrorTooLarge} {
		closed[c.Message()] = true
	}
	f.Fuzz(func(t *testing.T, key, kind, text string) {
		st, i := toolFixture(t)
		s := testService(t)
		intent := domain.KeyedWriteIntent{RequestID: "fuzz", Key: key, Kind: domain.Kind(kind), Parts: []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: text}}}
		r, err := Execute(testContext, st, "s", func(tx store.Tx, seq uint64) (domain.ToolResult, error) {
			return s.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, intent}, seq)
		})
		if err != nil {
			if !closed[err.Error()] {
				t.Fatalf("open error template: %q", err)
			}
			return
		}
		if !domain.ValidAgentKey(key) || kind != string(domain.KindFact) && kind != string(domain.KindDecision) {
			t.Fatalf("accepted key %q kind %q", key, kind)
		}
		update(t, st, func(tx store.Tx) error {
			it, err := tx.Item(r.Keyed.ItemID)
			if err != nil || it.Namespace != domain.NamespaceAgentKey || it.DirectiveID != domain.AgentKeyID(key) || it.Authority != domain.AuthorityAgent || it.Access != conversationBoundary(i.Principal) {
				t.Fatalf("keyed item: %+v, %v", it, err)
			}
			return nil
		})
	})
}
