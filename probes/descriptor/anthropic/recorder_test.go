package main

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func TestCostSplitsCacheWriteTTLs(t *testing.T) {
	u := usageSum{Input: 1_000_000, Output: 100_000, CacheWrite: 300_000, Write1h: 100_000, CacheRead: 1_000_000}
	// opus-5-5: 4 in + 2 out + 1.0 (200k 5m writes) + 0.8 (100k 1h writes) + 0.2 read = 8.0
	if got := cost("claude-opus-5-5", u); math.Abs(got-8.0) > 1e-9 {
		t.Fatalf("cost = %v, want 8.0", got)
	}
	if got := cost("unknown-model", usageSum{Input: 1_000_000}); got != 10 {
		t.Fatalf("unknown model should price as the most expensive tier, got %v", got)
	}
}

func TestDescribeBlockCountsCharacters(t *testing.T) {
	var b anthropic.BetaContentBlockUnion
	if err := json.Unmarshal([]byte(`{"type":"compaction","content":"41 × 17 − 3","signature":"sig"}`), &b); err != nil {
		t.Fatal(err)
	}
	if got, want := describeBlock(b), "compaction(content=11 encrypted=0 sig=3)"; got != want {
		t.Fatalf("describeBlock = %q, want %q", got, want)
	}
}
