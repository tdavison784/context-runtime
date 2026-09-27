package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func executeGC(f *facets, mem store.Store, s *Service, p domain.Principal, id string) (MutationOutcome, error) {
	var out MutationOutcome
	err := f.update(mem, func(tx store.Tx) error {
		var err error
		out, err = s.ExecuteGCRequest(tx, p, id, tx.NextSeq())
		return err
	})
	return out, err
}

func TestCompletionGCRequestExecutesOnceAfterProducerCommit(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	seedCompletion(t, mem, nil, "", false)
	seedItem(t, mem, func() domain.ContextItem {
		it := storetest.NewItem("s", "scratch", 0, "scratch")
		it.Scope, it.Access, it.Generation = domain.ScopeTask, storetest.DirectiveBoundary("s"), domain.GenerationEphemeral
		return it
	}())
	f := newFacets("scratch")
	done, err := completeTask(f, mem, s, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, true)
	if err != nil {
		t.Fatal(err)
	}
	id := done.Result.Completion.GCRequestID
	// The producer committed; a failing collector leaves the request pending.
	for name, p := range map[string]domain.Principal{
		"completion actor": storetest.NewPrincipal("s", domain.AuthorityUser),
		"foreign task": func() domain.Principal {
			p := storetest.NewPrincipal("s", domain.AuthorityHarness)
			p.TaskID = "other"
			return p
		}(),
	} {
		if _, err := executeGC(f, mem, s, p, id); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("%s collected: %v", name, err)
		}
	}
	readSemantic(t, mem, func(sem store.SemanticReader) error {
		if _, err := sem.GCResult(id); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("failed attempt linked a result: %v", err)
		}
		return nil
	})
	collector := storetest.NewPrincipal("s", domain.AuthorityHarness)
	first, err := executeGC(f, mem, s, collector, id)
	if err != nil {
		t.Fatal(err)
	}
	r := first.Result.Collect
	readSemantic(t, mem, func(sem store.SemanticReader) error {
		link, err := sem.GCResult(id)
		if err != nil || r.GCRequestID != id || len(r.ArchivedRefs) != 1 || r.ArchivedRefs[0].ItemID != "scratch" || link.CollectReceiptID != r.ID {
			t.Fatalf("collection: %+v link %+v %v", r, link, err)
		}
		return nil
	})
	if err := f.update(mem, func(tx store.Tx) error {
		sem, _ := store.Semantic(tx)
		page, err := sem.PendingGCRequests(store.Page{Limit: 4})
		if len(page.Records) != 0 {
			t.Fatalf("request still pending: %+v", page.Records)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	again, err := executeGC(f, mem, s, collector, id)
	if err != nil || again.Result.Collect.ID != r.ID {
		t.Fatalf("retry: %+v %v", again, err)
	}
	// A later, tightened policy still replays the committed collection.
	// Shrunk after New: a work bound of 1 has no room for a valid policy's
	// live-proof dependents (MaxLiveProofDependents).
	s2, _ := New(mem, testPolicy())
	s2.policy.MaxTransactionWork = 1
	if replay, err := executeGC(f, mem, s2, collector, id); err != nil || replay.Result.Collect.ID != r.ID {
		t.Fatalf("replay under new policy: %+v %v", replay, err)
	}
	other := collector
	other.AgentID = "other-agent"
	if _, err := executeGC(f, mem, s, other, id); !errors.Is(err, domain.ErrEventIDConflict) {
		t.Fatalf("different collector replayed: %v", err)
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		task, _ := tx.Task("task")
		if task.Status != domain.TaskCompleted {
			t.Fatal("collection changed producer state")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestEnqueueGCDeduplicatesTriggerIdentity(t *testing.T) {
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	f := newFacets()
	p := storetest.NewPrincipal("s", domain.AuthoritySystem)
	if err := f.update(mem, func(tx store.Tx) error {
		for _, id := range []string{"task", "other"} {
			if _, err := tx.PutTask(storetest.NewTask("s", id), 0, storetest.NewLifecycleEvent("s", "created-"+id, tx.NextSeq(), domain.TargetTask, id)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for range 2 {
		if err := f.update(mem, func(tx store.Tx) error {
			id, err := s.EnqueueGC(tx, p, domain.GCSupersession, domain.CollectTask, "task", "event-1")
			ids = append(ids, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if pending := pendingGC(t, mem); ids[0] == "" || ids[0] != ids[1] || len(pending) != 1 {
		t.Fatalf("duplicate trigger: %v %+v", ids, pending)
	}
	for name, enqueue := range map[string]func(store.Tx) (string, error){
		"changed task": func(tx store.Tx) (string, error) {
			return s.EnqueueGC(tx, p, domain.GCSupersession, domain.CollectTask, "other", "event-1")
		},
		"manual": func(tx store.Tx) (string, error) {
			return s.EnqueueGC(tx, p, domain.GCManual, domain.CollectTask, "task", "event-2")
		},
	} {
		if err := f.update(mem, func(tx store.Tx) error { _, err := enqueue(tx); return err }); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestCollectPendingExecutesDurableRequestsOnRealStore(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	seedCompletion(t, mem, nil, "", false)
	scratch := storetest.NewItem("s", "scratch", 0, "scratch")
	scratch.Scope, scratch.Access, scratch.Generation = domain.ScopeTask, storetest.DirectiveBoundary("s"), domain.GenerationEphemeral
	seedItem(t, mem, scratch)
	if _, err := s.CompleteTaskStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}); err != nil {
		t.Fatal(err)
	}
	collector := storetest.NewPrincipal("s", domain.AuthorityHarness)
	skip := func(domain.GCRequest) (domain.Principal, bool) { return domain.Principal{}, false }
	if n, err := s.CollectPending(ctx, "s", skip, 4); n != 0 || err != nil {
		t.Fatalf("skipped: %d %v", n, err)
	}
	pick := func(r domain.GCRequest) (domain.Principal, bool) { return collector, r.TaskID == collector.TaskID }
	if n, err := s.CollectPending(ctx, "s", pick, 4); n != 1 || err != nil {
		t.Fatalf("pending: %d %v", n, err)
	}
	if n, err := s.CollectPending(ctx, "s", pick, 4); n != 0 || err != nil {
		t.Fatalf("request executed twice: %d %v", n, err)
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		it, err := tx.Item("scratch")
		if err != nil || it.Residency != domain.ResidencyArchived {
			t.Fatalf("scratch not collected: %+v %v", it, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGCTriggerSetIsEnforced(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	manualOnly := testPolicy()
	manualOnly.GCTriggers = []domain.GCTrigger{domain.GCManual}
	s, err := New(mem, manualOnly)
	if err != nil {
		t.Fatal(err)
	}
	seedCompletion(t, mem, nil, "", false)
	// Completion always persists its durable request, even while disabled.
	done, err := s.CompleteTaskStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"})
	if err != nil || done.GCRequestID == "" {
		t.Fatalf("completion: %+v %v", done, err)
	}
	collector := storetest.NewPrincipal("s", domain.AuthorityHarness)
	f := newFacets()
	if _, err := executeGC(f, mem, s, collector, done.GCRequestID); !errors.Is(err, ErrGCTriggerDisabled) {
		t.Fatalf("disabled trigger executed: %v", err)
	}
	pick := func(domain.GCRequest) (domain.Principal, bool) { return collector, true }
	if n, err := s.CollectPending(ctx, "s", pick, 4); n != 0 || !errors.Is(err, ErrGCTriggerDisabled) {
		t.Fatalf("disabled request not skipped: %d %v", n, err)
	}
	if pending := pendingGC(t, mem); len(pending) != 1 {
		t.Fatalf("disabled request lost: %+v", pending)
	}
	// A disabled producer trigger persists nothing.
	if err := f.update(mem, func(tx store.Tx) error {
		id, err := s.EnqueueGC(tx, collector, domain.GCSupersession, domain.CollectSession, "", "event-1")
		if id != "" {
			t.Fatalf("disabled trigger enqueued %s", id)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if pending := pendingGC(t, mem); len(pending) != 1 {
		t.Fatalf("disabled producer persisted: %+v", pending)
	}
	if _, err := collect(f, mem, s, collector, domain.CollectIntent{RequestID: "m", Scope: domain.CollectSession, Trigger: domain.GCManual}); err != nil {
		t.Fatalf("enabled manual collection: %v", err)
	}
	// Re-enabling lets the same durable request run once.
	all, _ := New(mem, testPolicy())
	if n, err := all.CollectPending(ctx, "s", pick, 4); n != 1 || err != nil {
		t.Fatalf("re-enabled request: %d %v", n, err)
	}
	completionOnly := testPolicy()
	completionOnly.GCTriggers = []domain.GCTrigger{domain.GCTaskCompletion}
	s2, _ := New(mem, completionOnly)
	if _, err := collect(f, mem, s2, collector, domain.CollectIntent{RequestID: "m2", Scope: domain.CollectSession, Trigger: domain.GCManual}); !errors.Is(err, ErrGCTriggerDisabled) {
		t.Fatalf("disabled manual collection: %v", err)
	}
}

// facetless is a store double whose transactions expose only the Phase 2
// interfaces: no semantic facet exists for GC, receipts or the goal index.
type facetless struct{ store.Store }
type legacyRead struct{ store.ReadTx }

func (f facetless) Update(ctx context.Context, session string, fn func(store.Tx) error) error {
	return f.Store.Update(ctx, session, func(tx store.Tx) error { return fn(legacyOnly{tx}) })
}
func (f facetless) View(ctx context.Context, session string, fn func(store.ReadTx) error) error {
	return f.Store.View(ctx, session, func(tx store.ReadTx) error { return fn(legacyRead{tx}) })
}

func TestGCAndCompletionFailClosedWithoutSemanticFacet(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	seedCompletion(t, mem, nil, "", false)
	s, _ := New(facetless{mem}, testPolicy())
	pick := func(domain.GCRequest) (domain.Principal, bool) {
		return storetest.NewPrincipal("s", domain.AuthorityHarness), true
	}
	if n, err := s.CollectPending(ctx, "s", pick, 4); n != 0 || !errors.Is(err, domain.ErrUnsupportedSchema) {
		t.Fatalf("pending without facet: %d %v", n, err)
	}
	if _, err := s.CompleteTaskStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}); !errors.Is(err, domain.ErrUnsupportedSchema) {
		t.Fatalf("completion without facet: %v", err)
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		if task, _ := tx.Task("task"); task.Status != domain.TaskActive {
			t.Fatal("completion committed without receipt storage")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
