package ingest

import (
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/lifecycle"
	"github.com/tdavison784/context-runtime/internal/store"
)

// withGCTriggers enables every GC trigger in both the ingester's recorded
// policy and its lifecycle service's.
func (f *fixture) withGCTriggers() {
	f.t.Helper()
	pol := testPolicy()
	pol.GCTriggers = domain.DefaultGCTriggers()
	f.in.Semantic = &pol
	svc, err := lifecycle.New(f.s, pol)
	if err != nil {
		f.t.Fatal(err)
	}
	f.in.Lifecycle = svc
}

// gcRequests returns the session's pending GC requests by trigger.
func (f *fixture) gcRequests() map[domain.GCTrigger][]domain.GCRequest {
	f.t.Helper()
	out := map[domain.GCTrigger][]domain.GCRequest{}
	f.view(func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		page, err := sem.PendingGCRequests(store.Page{Limit: 256})
		for _, r := range page.Records {
			out[r.Trigger] = append(out[r.Trigger], r)
		}
		return err
	})
	return out
}

// TestGCProducers_SPEC16 (P3-39, SPEC-1.6): with the SUPERSESSION and TTL
// triggers enabled, ingest enqueues a durable GC request, in the event's
// transaction, when a parsed directive replaces a current version (origin:
// the replacing source actor) and when an event advances its task's turn
// (one per turn). A duplicate restatement, an event that opens no turn, and
// retries enqueue nothing more.
func TestGCProducers_SPEC16(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.withGCTriggers()
		user, sys := principal(domain.AuthorityUser), principal(domain.AuthoritySystem)
		// Every USER event opens a turn; a SYSTEM event opens none.
		q := userEvent("q1", "hi", false)
		f.mustIngest(user, q)
		ttl := f.gcRequests()[domain.GCTTL]
		if len(ttl) != 1 || ttl[0].TaskID != "T" || ttl[0].Scope != domain.CollectTask || ttl[0].Origin != user {
			t.Fatalf("TTL requests after turn 1 = %+v", ttl)
		}

		f.mustIngest(sys, sysEvent("v1", "## Pinned\n- [p] one\n"))
		f.mustIngest(sys, sysEvent("dup", "Note.\n## Pinned\n- [p] one\n"))
		if got := f.gcRequests(); len(got[domain.GCSupersession]) != 0 || len(got[domain.GCTTL]) != 1 {
			t.Fatalf("a first version, a duplicate or a turnless event enqueued: %+v", got)
		}
		v2 := sysEvent("v2", "## Pinned\n- [p] two\n")
		f.mustIngest(sys, v2)
		sup := f.gcRequests()[domain.GCSupersession]
		if len(sup) != 1 || sup[0].TaskID != "T" || sup[0].Scope != domain.CollectTask || sup[0].Origin != sys {
			t.Fatalf("supersession requests = %+v", sup)
		}

		q2 := userEvent("q2", "next", false)
		f.mustIngest(user, q2)
		seq := f.lastSeq()
		for _, st := range []struct {
			p domain.Principal
			e domain.Event
		}{{user, q}, {sys, v2}, {user, q2}} {
			if _, err := f.ingest(st.p, st.e); err != nil {
				t.Fatal(err)
			}
		}
		got := f.gcRequests()
		if len(got[domain.GCTTL]) != 2 || len(got[domain.GCSupersession]) != 1 || f.lastSeq() != seq {
			t.Fatalf("after turn 2 and retries: TTL %d, supersession %d, seq %d -> %d", len(got[domain.GCTTL]), len(got[domain.GCSupersession]), seq, f.lastSeq())
		}
	})
}

// gcTriggersOff records a policy that disables SUPERSESSION and TTL, for
// tests that deliberately run ingest with a routing fake or no lifecycle
// executor and are not about GC.
func (f *fixture) gcTriggersOff() {
	pol := testPolicy()
	pol.GCTriggers = []domain.GCTrigger{domain.GCManual, domain.GCTaskCompletion}
	f.in.Semantic = &pol
}

// withoutGCTriggers records and executes a policy that disables SUPERSESSION
// and TTL, now that the default manifest enables them.
func (f *fixture) withoutGCTriggers() {
	f.t.Helper()
	f.gcTriggersOff()
	pol := *f.in.Semantic
	svc, err := lifecycle.New(f.s, pol)
	if err != nil {
		f.t.Fatal(err)
	}
	f.in.Lifecycle = svc
}

// TestGCProducers_RecordedPolicyDecides_SPEC211 (SPEC-2.11, P3-39): the
// event's RECORDED policy alone decides whether a SUPERSESSION or TTL
// trigger is produced, through the leaf producer, whatever the lifecycle
// executor's own policy says, and whether or not an executor is configured.
// A trigger the recorded policy disables produces nothing.
func TestGCProducers_RecordedPolicyDecides_SPEC211(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		user, sys := principal(domain.AuthorityUser), principal(domain.AuthoritySystem)
		// Recorded policy disables both; the executor's enables both.
		f.withGCTriggers()
		off := testPolicy()
		off.GCTriggers = []domain.GCTrigger{domain.GCManual, domain.GCTaskCompletion}
		f.in.Semantic = &off
		f.mustIngest(user, userEvent("q1", "hi", false))
		f.mustIngest(sys, sysEvent("v1", "## Pinned\n- [p] one\n"))
		f.mustIngest(sys, sysEvent("v2", "## Pinned\n- [p] two\n"))
		if got := f.gcRequests(); len(got[domain.GCTTL])+len(got[domain.GCSupersession]) != 0 {
			t.Fatalf("triggers the recorded policy disables were enqueued: %+v", got)
		}
		// Recorded policy enables both; the executor's disables both, or
		// there is no executor at all.
		on := testPolicy()
		on.GCTriggers = domain.DefaultGCTriggers()
		svc, err := lifecycle.New(f.s, off)
		if err != nil {
			t.Fatal(err)
		}
		for i, exec := range []LifecycleExecutor{svc, nil} {
			f.in.Semantic, f.in.Lifecycle = &on, exec
			f.mustIngest(user, userEvent(fmt.Sprintf("q-on-%d", i), "next", false))
			f.mustIngest(sys, sysEvent(fmt.Sprintf("v-on-%d", i), fmt.Sprintf("## Pinned\n- [p] version %d\n", i+3)))
			got := f.gcRequests()
			if len(got[domain.GCTTL]) != i+1 || len(got[domain.GCSupersession]) != i+1 {
				t.Fatalf("executor %d: TTL %d, supersession %d, want %d each", i, len(got[domain.GCTTL]), len(got[domain.GCSupersession]), i+1)
			}
			for _, r := range append(got[domain.GCTTL], got[domain.GCSupersession]...) {
				if r.PolicyVersion != on.Version {
					t.Fatalf("request policy %q, want the recorded %q", r.PolicyVersion, on.Version)
				}
			}
		}
	})
}

// TestGCProducers_TasklessSupersession_H4 (H4, SPEC-2.2/DUR-2.5): replacing
// a directive with no TaskID succeeds and produces no GC request; Phase 3
// has no session-scoped GC.
func TestGCProducers_TasklessSupersession_H4(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.withGCTriggers()
		sys := domain.Principal{SessionID: sess, Authority: domain.AuthoritySystem}
		event := func(id, text string) domain.Event {
			e := sysEvent(id, text)
			e.Spans[0].Access = domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sess}
			return e
		}
		f.mustIngest(sys, event("s1", "## Pinned\n- [p] {scope=SESSION} one\n"))
		r := f.mustIngest(sys, event("s2", "## Pinned\n- [p] {scope=SESSION} two\n"))
		if len(r.Replacements) != 1 {
			t.Fatalf("task-less replacement: %+v", r.Replacements)
		}
		if got := f.gcRequests(); len(got) != 0 {
			t.Fatalf("task-less supersession enqueued %+v", got)
		}
	})
}

// TestGCProducers_WorkingSnapshot_SPEC16: a filed Working snapshot that
// retires earlier members enqueues exactly one SUPERSESSION request,
// however many members it retires; a first snapshot or an identical
// restatement retires nothing and enqueues nothing.
func TestGCProducers_WorkingSnapshot_SPEC16(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		f.withGCTriggers()
		sys := principal(domain.AuthoritySystem)
		f.mustIngest(sys, sysEvent("w1", "## Working\n- step one\n- step two\n"))
		f.mustIngest(sys, sysEvent("w1-dup", "Again.\n## Working\n- step one\n- step two\n"))
		if n := len(f.gcRequests()[domain.GCSupersession]); n != 0 {
			t.Fatalf("a first or identical snapshot enqueued %d requests", n)
		}
		r := f.mustIngest(sys, sysEvent("w2", "## Working\n- step three\n"))
		if len(r.Replacements) != 2 {
			t.Fatalf("snapshot retired %d members, want 2", len(r.Replacements))
		}
		sup := f.gcRequests()[domain.GCSupersession]
		if len(sup) != 1 || sup[0].TaskID != "T" || sup[0].Origin != sys {
			t.Fatalf("snapshot supersession requests = %+v", sup)
		}
	})
}

// TestGCProducers_DefaultPolicy_SPEC16 (W3): once ingest produces them, the
// default manifest enables SUPERSESSION and TTL, so an unconfigured ingester
// creates their durable GC requests: one per turn advance, one per parsed
// replacement and one per retiring Working snapshot.
func TestGCProducers_DefaultPolicy_SPEC16(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		user, sys := principal(domain.AuthorityUser), principal(domain.AuthoritySystem)
		f.mustIngest(user, userEvent("q1", "hi", false))
		f.mustIngest(sys, sysEvent("v1", "## Pinned\n- [p] one\n"))
		f.mustIngest(sys, sysEvent("v2", "## Pinned\n- [p] two\n"))
		f.mustIngest(sys, sysEvent("w1", "## Working\n- step one\n"))
		f.mustIngest(sys, sysEvent("w2", "## Working\n- step two\n"))
		got := f.gcRequests()
		if len(got[domain.GCTTL]) != 1 || len(got[domain.GCSupersession]) != 2 {
			t.Fatalf("default policy: TTL %d (want 1), supersession %d (want 2): %+v", len(got[domain.GCTTL]), len(got[domain.GCSupersession]), got)
		}
	})
}
