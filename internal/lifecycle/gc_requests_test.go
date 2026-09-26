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
	if _, ok := f.results[id]; ok {
		t.Fatal("failed attempt linked a result")
	}
	collector := storetest.NewPrincipal("s", domain.AuthorityHarness)
	first, err := executeGC(f, mem, s, collector, id)
	if err != nil {
		t.Fatal(err)
	}
	r := first.Result.Collect
	if r.GCRequestID != id || len(r.ArchivedRefs) != 1 || r.ArchivedRefs[0].ItemID != "scratch" || f.results[id].CollectReceiptID != r.ID {
		t.Fatalf("collection: %+v link %+v", r, f.results[id])
	}
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
	var ids []string
	for range 2 {
		if err := f.update(mem, func(tx store.Tx) error {
			id, err := s.EnqueueGC(tx, p, domain.GCSupersession, domain.CollectSession, "", "event-1")
			ids = append(ids, id)
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	if ids[0] != ids[1] || len(f.requests) != 1 {
		t.Fatalf("duplicate trigger: %v %d", ids, len(f.requests))
	}
	for name, enqueue := range map[string]func(store.Tx) (string, error){
		"changed scope": func(tx store.Tx) (string, error) {
			return s.EnqueueGC(tx, p, domain.GCSupersession, domain.CollectTask, "task", "event-1")
		},
		"manual": func(tx store.Tx) (string, error) {
			return s.EnqueueGC(tx, p, domain.GCManual, domain.CollectSession, "", "event-2")
		},
	} {
		if err := f.update(mem, func(tx store.Tx) error { _, err := enqueue(tx); return err }); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestCollectPendingFailsClosedWithoutBackendSupport(t *testing.T) {
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	n, err := s.CollectPending(context.Background(), "s", func(domain.GCRequest) (domain.Principal, bool) {
		return storetest.NewPrincipal("s", domain.AuthoritySystem), true
	}, 4)
	if n != 0 || !errors.Is(err, domain.ErrUnsupportedSchema) {
		t.Fatalf("pending: %d %v", n, err)
	}
}
