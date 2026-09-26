package sqlite_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/ingest"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
)

// ingestPinned ingests one SYSTEM event whose span carries n Pinned items
// into a fresh SQLite store and returns how long it took.
func ingestPinned(t *testing.T, n int) time.Duration {
	t.Helper()
	s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "scale.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var b strings.Builder
	b.WriteString("## Pinned\n")
	for i := range n {
		fmt.Fprintf(&b, "- pinned requirement number %d\n", i)
	}
	p := domain.Principal{SessionID: "s", WorkflowID: "wf", TaskID: "task", AgentID: "agent", Authority: domain.AuthoritySystem}
	e := domain.Event{EventID: "e", Kind: domain.EventSystem, Spans: []domain.Span{{Authority: domain.AuthoritySystem,
		Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"},
		Parts:  []domain.InputPart{{Type: domain.PartText, MediaType: "text/markdown", Text: b.String()}}}}}
	in := ingest.Ingester{IDs: &domain.SequentialIDs{}, Limits: domain.Limits{MaxItemsPerSpan: n + 1}}
	start := time.Now()
	r, err := in.Ingest(context.Background(), s, p, e)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != n+1 {
		t.Fatalf("receipt items = %d, want %d", len(r.Items), n+1)
	}
	return time.Since(start)
}

// TestOneLargeEventScalesLinearly is SPEC-3.1 item 2 end to end: one event
// with 4000 Pinned items on SQLite takes about 8x as long as one with 500,
// not the 32x a per-item reload of the growing transcript cost.
func TestOneLargeEventScalesLinearly(t *testing.T) {
	if testing.Short() {
		t.Skip("timing test")
	}
	small, large := ingestPinned(t, 500), ingestPinned(t, 4000)
	if ratio := float64(large) / float64(small); ratio > 16 {
		t.Errorf("one event: 500 items %v, 4000 items %v (x%.1f); want about x8 (linear)", small, large, ratio)
	}
	t.Logf("one event: 500 items %v, 4000 items %v (x%.1f)", small, large, float64(large)/float64(small))
}
