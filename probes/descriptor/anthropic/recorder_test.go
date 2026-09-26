package main

import (
	"math"
	"testing"
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
