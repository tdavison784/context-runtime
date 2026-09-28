package retrieve

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// p342bSem reads the semantic facet inside a View.
func p342bSem(t *testing.T, s store.Store, fn func(store.SemanticReader) error) {
	t.Helper()
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		return fn(sem)
	}); err != nil {
		t.Fatal(err)
	}
}

func p342bStores28(t *testing.T) map[string]func(*testing.T) store.Store {
	return map[string]func(*testing.T) store.Store{
		"memory": func(t *testing.T) store.Store { return memory.New() },
		"sqlite": func(t *testing.T) store.Store {
			s, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "p342b28.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			return s
		},
	}
}

// TestP3_28_ExpiredSourceReadsUnchanged closes the P3-42 table row
// "historical resolved/expired/archived reads unchanged" (ADR8:1223). The
// cited test covered resolved+archived only; this adds the expired half on
// both stores: a TASK item whose TTL is provably dead still reads back
// byte-identically with Expiry=EXPIRED reported per requester, beside a
// resolved+archived goal control — and neither read mutates anything.
func TestP3_28_ExpiredSourceReadsUnchanged(t *testing.T) {
	ctx := context.Background()
	for name, open := range p342bStores28(t) {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			one := 1
			p := storetest.NewPrincipal("s", domain.AuthorityUser)
			if err := s.Update(ctx, "s", func(tx store.Tx) error {
				task := storetest.NewTask("s", p.TaskID)
				task.Turn, task.TurnID = 3, "turn-3"
				if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, task.TaskID)); err != nil {
					return err
				}
				aging := storetest.NewItem("s", "aging", tx.NextSeq(), "short-lived fact")
				aging.Scope = domain.ScopeTask
				aging.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: p.TaskID}
				aging.CreatedTurn, aging.TTLTurns = 1, &one
				if err := tx.InsertItem(aging); err != nil {
					return err
				}
				done := storetest.NewGoal("s", "done", tx.NextSeq(), "finished work")
				done.Residency = domain.ResidencyArchived
				resolved := domain.GoalResolved
				done.GoalStatus = &resolved
				return tx.InsertItem(done)
			}); err != nil {
				t.Fatal(err)
			}
			svc := New(s)
			got, err := svc.Get(ctx, p, "aging")
			if err != nil {
				t.Fatal(err)
			}
			if got.Observed.Expiry != domain.ExpiryExpired {
				t.Fatalf("expired TTL reported as %s, want EXPIRED", got.Observed.Expiry)
			}
			if got.Item.ContentHash != storetest.NewItem("s", "aging", 0, "short-lived fact").ContentHash || got.Item.Version != 1 {
				t.Fatalf("expired read changed content: %+v", got.Item)
			}
			first := got.SnapshotSeq
			goal, err := svc.Get(ctx, p, "done")
			if err != nil {
				t.Fatal(err)
			}
			if goal.Observed.Residency != domain.ResidencyArchived || goal.Observed.GoalStatus == nil || *goal.Observed.GoalStatus != domain.GoalResolved {
				t.Fatalf("resolved+archived control: %+v", goal.Observed)
			}
			if again, err := svc.Get(ctx, p, "aging"); err != nil || again.SnapshotSeq != first {
				t.Fatalf("second read moved the snapshot: %d vs %d (%v)", again.SnapshotSeq, first, err)
			}
			if err := s.View(ctx, "s", func(tx store.ReadTx) error {
				for id := range map[string]bool{"aging": true, "done": true} {
					it, err := tx.Item(id)
					if err != nil || it.Version != 1 {
						t.Fatalf("%s mutated by a read: %+v %v", id, it, err)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestP3_28_WrongSessionAndAgentGetExactNotFound closes the P3-42 table row
// "wrong session/agent exact ErrNotFound" (ADR8:1224). The cited test varied
// the agent only; this adds the wrong-session half on both stores: a reader
// from another session, another session+agent, a wrong agent, and a missing
// item all converge on the exact bare ErrNotFound sentinel, and the private
// item is untouched.
func TestP3_28_WrongSessionAndAgentGetExactNotFound(t *testing.T) {
	ctx := context.Background()
	for name, open := range p342bStores28(t) {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			private := storetest.NewItem("s", "private", 0, "agent-private fact")
			private.Scope, private.AgentID = domain.ScopeAgent, "agent"
			private.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "agent"}
			if err := s.Update(ctx, "s", func(tx store.Tx) error {
				private.Seq = tx.NextSeq()
				return tx.InsertItem(private)
			}); err != nil {
				t.Fatal(err)
			}
			svc := New(s)
			base := storetest.NewPrincipal("s", domain.AuthorityUser)
			wrongAgent := base
			wrongAgent.AgentID = "other"
			wrongSession := base
			wrongSession.SessionID = "other-session"
			wrongBoth := base
			wrongBoth.SessionID, wrongBoth.AgentID = "other-session", "other"
			for _, tc := range []struct {
				name string
				p    domain.Principal
				id   string
			}{
				{"wrong session", wrongSession, "private"},
				{"wrong session and agent", wrongBoth, "private"},
				{"wrong agent", wrongAgent, "private"},
				{"wrong session, missing item", wrongSession, "missing"},
			} {
				_, err := svc.Get(ctx, tc.p, tc.id)
				if !errors.Is(err, domain.ErrNotFound) || err.Error() != domain.ErrNotFound.Error() {
					t.Fatalf("%s: %v (%T), want the exact bare ErrNotFound", tc.name, err, err)
				}
			}
			if err := s.View(ctx, "s", func(tx store.ReadTx) error {
				it, err := tx.Item("private")
				if err != nil || it.Version != 1 || it.Residency != domain.ResidencyResident {
					t.Fatalf("private item touched: %+v %v", it, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestP3_28_CompletedRequesterIsDeniedAdmission closes the P3-42 table row
// "completed requester denied" (ADR8:1225): the cited test only proved a
// completed ORIGIN still reads; the requester side was never denied
// anywhere. This drives the real admission path on both stores: while the
// task is active the harness rehydrates and mints a lease; once the task
// completes, the same requester is refused with the closed bare ErrNotFound,
// a denial event (and only that) is stored, and nothing is consumed.
func TestP3_28_CompletedRequesterIsDeniedAdmission(t *testing.T) {
	ctx := context.Background()
	for name, open := range p342bStores28(t) {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			p, _ := seedLeaseStore(t, s)
			svc := New(s)
			if _, err := svc.Rehydrate(ctx, p, harnessIntent(p, "live"), leasePolicy(), false); err != nil {
				t.Fatalf("active-task rehydrate: %v", err)
			}
			var completed domain.TaskState
			if err := s.Update(ctx, "s", func(tx store.Tx) error {
				task, err := tx.Task(p.TaskID)
				if err != nil {
					return err
				}
				at := tx.NextSeq()
				task.Status, task.CompletedSeq = domain.TaskCompleted, at
				completed, err = tx.PutTask(task, task.Version, domain.LifecycleEvent{ID: "p342b25-complete", SessionID: "s", Seq: at,
					TargetKind: domain.TargetTask, TargetID: task.TaskID, Action: "complete", Actor: p})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if completed.Status != domain.TaskCompleted {
				t.Fatalf("task not completed: %+v", completed)
			}
			_, err := svc.Rehydrate(ctx, p, harnessIntent(p, "dead"), leasePolicy(), false)
			if !errors.Is(err, domain.ErrNotFound) || err.Error() != domain.ErrNotFound.Error() {
				t.Fatalf("completed requester: %v, want the closed bare ErrNotFound", err)
			}
			p342bSem(t, s, func(sem store.SemanticReader) error {
				events, err := sem.RetrievalEventsByRequest(p, "dead", store.Page{Limit: 8})
				if err != nil || len(events.Records) != 1 {
					t.Fatalf("denial audit: %+v %v", events, err)
				}
				ev := events.Records[0]
				if ev.ErrorCode != domain.ToolErrorNotFound || ev.Source != nil || ev.ResultID != "" || ev.RequestID != "dead" {
					t.Fatalf("denial event content: %+v", ev)
				}
				if _, err := sem.MutationReceipt(domain.MutationRetrieval, "dead"); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("denied request stored a receipt: %v", err)
				}
				return nil
			})
			// The earlier success is the only lease; the denial added none.
			if leases := storedLeases(t, s, p); len(leases) != 1 {
				t.Fatalf("denial minted or consumed leases: %+v", leases)
			}
		})
	}
}

// TestP3_28_CompletedOriginBoundaryStillEnforced closes the P3-42 table row
// "completed origin accessible only where boundary allows" (ADR8:1226): the
// cited test had the positive half only. On both stores an in-boundary
// TASK-scoped item of a completed task still reads (reported EXPIRED), while
// requesters outside the boundary — foreign task, wrong session — and a
// missing item get the exact ErrNotFound, and the item never moves.
func TestP3_28_CompletedOriginBoundaryStillEnforced(t *testing.T) {
	ctx := context.Background()
	for name, open := range p342bStores28(t) {
		t.Run(name, func(t *testing.T) {
			s := open(t)
			p := storetest.NewPrincipal("s", domain.AuthorityUser)
			if err := s.Update(ctx, "s", func(tx store.Tx) error {
				at := tx.NextSeq()
				task := storetest.NewTask("s", p.TaskID)
				task.Status, task.CompletedSeq = domain.TaskCompleted, at
				if _, err := tx.PutTask(task, 0, domain.LifecycleEvent{ID: "p342b26-complete", SessionID: "s", Seq: at,
					TargetKind: domain.TargetTask, TargetID: task.TaskID, Action: "complete", Actor: p}); err != nil {
					return err
				}
				item := storetest.NewItem("s", "finished", tx.NextSeq(), "completed-origin fact")
				item.Scope = domain.ScopeTask
				item.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: p.TaskID}
				return tx.InsertItem(item)
			}); err != nil {
				t.Fatal(err)
			}
			svc := New(s)
			got, err := svc.Get(ctx, p, "finished")
			if err != nil || got.Observed.Expiry != domain.ExpiryExpired || got.Item.ContentHash != storetest.NewItem("s", "finished", 0, "completed-origin fact").ContentHash {
				t.Fatalf("in-boundary completed-origin read: %+v %v", got, err)
			}
			for _, tc := range []struct {
				name string
				p    domain.Principal
			}{
				{"foreign task", func() domain.Principal { q := p; q.TaskID = "other"; return q }()},
				{"wrong session", func() domain.Principal { q := p; q.SessionID = "other-session"; return q }()},
			} {
				if _, err := svc.Get(ctx, tc.p, "finished"); !errors.Is(err, domain.ErrNotFound) || err.Error() != domain.ErrNotFound.Error() {
					t.Fatalf("%s: %v, want the exact bare ErrNotFound", tc.name, err)
				}
			}
			if _, err := svc.Get(ctx, p, "missing"); !errors.Is(err, domain.ErrNotFound) {
				t.Fatalf("missing item: %v", err)
			}
			if err := s.View(ctx, "s", func(tx store.ReadTx) error {
				it, err := tx.Item("finished")
				if err != nil || it.Version != 1 {
					t.Fatalf("completed-origin item moved: %+v %v", it, err)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
