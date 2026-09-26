package domain

import (
	"strings"
	"testing"
)

func TestAgentKeyGrammarAndKindAllowlist(t *testing.T) {
	for _, key := range []string{"a", "_a", "-a", "a.b", strings.Repeat("a", 74)} {
		if !ValidAgentKey(key) {
			t.Fatalf("valid key %q rejected", key)
		}
	}
	for _, key := range []string{"", ".a", "a b", strings.Repeat("a", 75)} {
		if ValidAgentKey(key) {
			t.Fatalf("invalid key %q accepted", key)
		}
	}
	i := KeyedWriteIntent{RequestID: "r", Key: "g", Kind: KindGoal, Parts: []ContentPart{{Type: PartText, Text: "goal"}}}
	if i.Validate() == nil {
		t.Fatal("tool-created goal accepted")
	}
}
