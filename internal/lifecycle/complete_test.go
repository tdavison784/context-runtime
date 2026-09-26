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

func TestCompleteTaskRejectsForeignWorkflowBeforeOwnerQueries(t *testing.T) {
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	p := storetest.NewPrincipal("s", domain.AuthorityUser)
	if err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
		_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	p.WorkflowID = "foreign"
	err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
		out, err := s.CompleteTask(tx, p, domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, tx.NextSeq())
		if err != domain.ErrNotFound || out.MutationReceiptID != "" || out.Result.Completion != nil {
			t.Fatalf("completion: %+v %v", out, err)
		}
		return nil
	})
	if err != domain.ErrNotFound {
		t.Fatalf("ignored completion failure: %v", err)
	}
}

func completeTask(f *facets, mem store.Store, s *Service, p domain.Principal, i domain.CompleteTaskIntent, allocate bool) (MutationOutcome, error) {
	var out MutationOutcome
	err := f.update(mem, func(tx store.Tx) error {
		var seq uint64
		if allocate {
			seq = tx.NextSeq()
		}
		var err error
		out, err = s.CompleteTask(tx, p, i, seq)
		return err
	})
	return out, err
}

// seedCompletion commits task "task" and TASK-owned goals; hide makes the named
// goal inaccessible to the completing agent while it stays task-owned.
func seedCompletion(t *testing.T, mem store.Store, goals []string, hide string, obligation bool) {
	t.Helper()
	if err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
		for _, id := range goals {
			it := storetest.NewGoal("s", id, tx.NextSeq(), "goal "+id)
			it.Scope, it.Access = domain.ScopeTask, storetest.DirectiveBoundary("s")
			if id == hide {
				it.AgentID, it.Access.AgentID = "other-agent", "other-agent"
			}
			if err := tx.InsertItem(it); err != nil {
				return err
			}
		}
		if obligation {
			src := storetest.NewItem("s", "source", tx.NextSeq(), "source")
			src.Scope, src.Access = domain.ScopeTask, storetest.DirectiveBoundary("s")
			if err := tx.InsertItem(src); err != nil {
				return err
			}
			if err := tx.InsertObligationVersion(storetest.NewObligation("s", "obligation", 1, tx.NextSeq(), src.ID)); err != nil {
				return err
			}
		}
		_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCompleteTaskResolvesOwnedGoalsAndReplaysFrozenReceipt(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	p := storetest.NewPrincipal("s", domain.AuthorityUser)
	seedCompletion(t, mem, []string{"g1", "g2"}, "", false)
	intent := domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}
	f := newFacets("g1", "g2")
	res, err := completeTask(f, mem, s, p, intent, true)
	first := res.Result.Completion
	if err != nil || first == nil || res.MutationReceiptID == "" {
		t.Fatal(err)
	}
	if first.Validate() != nil || len(first.ResolvedGoals) != 2 || first.BeforeVersion != 1 || first.AfterVersion != 2 {
		t.Fatalf("receipt: %+v", first)
	}
	var last uint64
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		last = tx.LastSeq()
		task, _ := tx.Task("task")
		if task.Status != domain.TaskCompleted || task.CompletedSeq == 0 {
			t.Fatalf("task: %+v", task)
		}
		for _, id := range []string{"g1", "g2"} {
			g, _ := tx.Item(id)
			if *g.GoalStatus != domain.GoalResolved || g.Retention != domain.RetentionHigh || g.Version != 2 {
				t.Fatalf("goal %s not resolved: %+v", id, g)
			}
		}
		gc, ok := f.requests[first.GCRequestID]
		if !ok || gc.Trigger != domain.GCTaskCompletion || gc.TaskID != "task" || gc.Origin != p {
			t.Fatalf("GC request: %+v", gc)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The completed task no longer passes first-execution checks, yet the same
	// request replays its original success without allocating a sequence.
	replayed, err := completeTask(f, mem, s, p, intent, false)
	again := replayed.Result.Completion
	if err != nil || again == nil || replayed.MutationReceiptID != res.MutationReceiptID || again.AuditID != first.AuditID || again.GCRequestID != first.GCRequestID || len(again.ResolvedGoals) != 2 {
		t.Fatalf("replay: %+v %v", again, err)
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		if tx.LastSeq() != last {
			t.Fatal("replay allocated a sequence")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := completeTask(f, mem, s, p, domain.CompleteTaskIntent{RequestID: "r2", TaskID: "task"}, true); err != domain.ErrInvalidTransition {
		t.Fatalf("distinct request on completed task: %v", err)
	}
	other := p
	other.Authority = domain.AuthoritySystem
	if _, err := completeTask(f, mem, s, other, intent, true); err != domain.ErrEventIDConflict {
		t.Fatalf("changed principal replay: %v", err)
	}
}

func TestCompleteTaskFailsClosedWithoutPartialEffects(t *testing.T) {
	for name, tc := range map[string]struct {
		hide       string
		obligation bool
		actor      domain.Authority
		want       error
	}{
		"hidden goal":          {hide: "g2", actor: domain.AuthorityUser, want: domain.ErrInvalidAuthorityPromotion},
		"unfinished":           {obligation: true, actor: domain.AuthoritySystem, want: domain.ErrUnfinishedObligations},
		"untrusted agent":      {actor: domain.AuthorityAgent, want: domain.ErrInvalidAuthorityPromotion},
		"hidden before blocks": {hide: "g1", obligation: true, actor: domain.AuthorityUser, want: domain.ErrInvalidAuthorityPromotion},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			mem := memory.New()
			t.Cleanup(func() { mem.Close() })
			s, _ := New(mem, testPolicy())
			seedCompletion(t, mem, []string{"g1", "g2"}, tc.hide, tc.obligation)
			var before uint64
			_ = mem.View(ctx, "s", func(tx store.ReadTx) error { before = tx.LastSeq(); return nil })
			p := storetest.NewPrincipal("s", tc.actor)
			if _, err := completeTask(newFacets("g1", "g2"), mem, s, p, domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, true); err != tc.want {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
				task, _ := tx.Task("task")
				g, _ := tx.Item("g1")
				if tx.LastSeq() != before || task.Status != domain.TaskActive || *g.GoalStatus != domain.GoalOpen {
					t.Fatal("failed completion left effects")
				}
				sem, _ := store.ReadSemantic(tx)
				if _, err := sem.MutationReceipt(domain.MutationLifecycle, "r"); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("failed completion receipt: %v", err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompleteTaskWithoutGoalsStillRecordsReceiptAndGC(t *testing.T) {
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	seedCompletion(t, mem, nil, "", false)
	out, err := completeTask(newFacets(), mem, s, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, true)
	if err != nil || out.MutationReceiptID == "" || out.GrantID != "" || out.Result.Completion == nil || len(out.Result.Completion.ResolvedGoals) != 0 || out.Result.Validate() != nil {
		t.Fatalf("empty completion: %+v %v", out, err)
	}
}
