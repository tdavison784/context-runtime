package tools

import (
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func pendingGC(t *testing.T, st store.Store) []domain.GCRequest {
	t.Helper()
	var out []domain.GCRequest
	update(t, st, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		page, err := sem.PendingGCRequests(store.Page{Limit: 32})
		out = page.Records
		return err
	})
	return out
}

// SPEC-2.3: a keyed replacement is a SUPERSESSION and enqueues one durable
// GC request under the service's recorded policy; a first filing, a
// duplicate and a replay enqueue nothing more.
func TestKeyedReplacementEnqueuesSupersessionGC(t *testing.T) {
	st, i := toolFixture(t)
	s := testService(t)
	first := remember(t, st, s, i, keyed("r1", "db", "postgres"))
	remember(t, st, s, addToolCall(t, st, i, "t2"), keyed("r2", "db", "postgres")) // duplicate
	if got := pendingGC(t, st); len(got) != 0 {
		t.Fatalf("first filing or duplicate enqueued GC: %+v", got)
	}
	t3 := addToolCall(t, st, i, "t3")
	second := remember(t, st, s, t3, keyed("r3", "db", "mysql"))
	if second.SupersededItemID != first.ItemID {
		t.Fatalf("replacement: %+v", second)
	}
	remember(t, st, s, t3, keyed("r3", "db", "mysql")) // replay
	got := pendingGC(t, st)
	if len(got) != 1 {
		t.Fatalf("supersession GC requests: %+v", got)
	}
	r := got[0]
	if r.Trigger != domain.GCSupersession || r.Scope != domain.CollectTask || r.TaskID != i.Principal.TaskID || r.Origin != i.Principal || r.PolicyVersion != testPolicy().Version {
		t.Fatalf("GC request: %+v", r)
	}

	// A policy that disables SUPERSESSION enqueues nothing.
	st, i = toolFixture(t)
	p := testPolicy()
	p.GCTriggers = slices.DeleteFunc(domain.DefaultGCTriggers(), func(g domain.GCTrigger) bool { return g == domain.GCSupersession })
	quiet, err := NewService(p)
	if err != nil {
		t.Fatal(err)
	}
	remember(t, st, quiet, i, keyed("r1", "db", "postgres"))
	remember(t, st, quiet, addToolCall(t, st, i, "t2"), keyed("r2", "db", "mysql"))
	if got := pendingGC(t, st); len(got) != 0 {
		t.Fatalf("disabled trigger enqueued GC: %+v", got)
	}
}
