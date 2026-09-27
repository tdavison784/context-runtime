package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

var errInjected = errors.New("injected write failure")

// faultTx fails the fail-th write reaching it (1-based) with errInjected and
// counts every write, so a test can walk each constituent write of one
// mutation. The failing write never reaches the backend.
type faultTx struct {
	store.Tx
	writes *int
	fail   int
}

func (f faultTx) hit() error {
	*f.writes++
	if *f.writes == f.fail {
		return errInjected
	}
	return nil
}

func (f faultTx) InsertItem(it domain.ContextItem) error {
	if err := f.hit(); err != nil {
		return err
	}
	return f.Tx.InsertItem(it)
}
func (f faultTx) UpdateItem(id string, v uint64, c domain.ItemChange, e domain.LifecycleEvent) (domain.ContextItem, error) {
	if err := f.hit(); err != nil {
		return domain.ContextItem{}, err
	}
	return f.Tx.UpdateItem(id, v, c, e)
}
func (f faultTx) InsertRelationship(r domain.Relationship) error {
	if err := f.hit(); err != nil {
		return err
	}
	return f.Tx.InsertRelationship(r)
}
func (f faultTx) AppendLifecycleEvent(e domain.LifecycleEvent) error {
	if err := f.hit(); err != nil {
		return err
	}
	return f.Tx.AppendLifecycleEvent(e)
}
func (f faultTx) RetireObligationVersion(id string, v, rev uint64, e domain.LifecycleEvent) (domain.ObligationVersion, error) {
	if err := f.hit(); err != nil {
		return domain.ObligationVersion{}, err
	}
	return f.Tx.RetireObligationVersion(id, v, rev, e)
}
func (f faultTx) InsertGrant(g domain.MutationGrant) error {
	if err := f.hit(); err != nil {
		return err
	}
	return f.Tx.InsertGrant(g)
}
func (f faultTx) RevokeGrant(id string, e domain.LifecycleEvent) (domain.MutationGrant, error) {
	if err := f.hit(); err != nil {
		return domain.MutationGrant{}, err
	}
	return f.Tx.RevokeGrant(id, e)
}
func (f faultTx) PutTask(t domain.TaskState, v uint64, e domain.LifecycleEvent) (domain.TaskState, error) {
	if err := f.hit(); err != nil {
		return domain.TaskState{}, err
	}
	return f.Tx.PutTask(t, v, e)
}
func (f faultTx) SemanticTransaction() (store.SemanticTx, error) {
	sem, err := store.Semantic(f.Tx)
	return faultSem{SemanticTx: sem, f: f}, err
}
func (f faultTx) SemanticReadBackend() store.SemanticReader {
	r, err := store.ReadSemantic(f.Tx)
	if err != nil {
		return nil
	}
	return r
}

type faultSem struct {
	store.SemanticTx
	f faultTx
}

func (s faultSem) InsertSemanticChange(c domain.SemanticChange) error {
	if err := s.f.hit(); err != nil {
		return err
	}
	return s.SemanticTx.InsertSemanticChange(c)
}
func (s faultSem) InsertCreationDeclaration(d domain.CreationDeclaration) error {
	if err := s.f.hit(); err != nil {
		return err
	}
	return s.SemanticTx.InsertCreationDeclaration(d)
}
func (s faultSem) SetCurrentVersion(id, prior string) error {
	if err := s.f.hit(); err != nil {
		return err
	}
	return s.SemanticTx.SetCurrentVersion(id, prior)
}
func (s faultSem) InsertMutationReceipt(r domain.MutationReceipt) error {
	if err := s.f.hit(); err != nil {
		return err
	}
	return s.SemanticTx.InsertMutationReceipt(r)
}
func (s faultSem) InsertGCRequest(r domain.GCRequest) error {
	if err := s.f.hit(); err != nil {
		return err
	}
	return s.SemanticTx.InsertGCRequest(r)
}
func (s faultSem) InsertGCResult(r domain.GCResult) error {
	if err := s.f.hit(); err != nil {
		return err
	}
	return s.SemanticTx.InsertGCResult(r)
}
func (s faultSem) InsertCollectReceipt(r domain.CollectReceipt) error {
	if err := s.f.hit(); err != nil {
		return err
	}
	return s.SemanticTx.InsertCollectReceipt(r)
}

// walkWrites fails each write of op in turn, on a fresh store each time, and
// requires that the failure is reported, that ignoring it still aborts the
// transaction, and that nothing (not even a sequence) commits. It returns the
// number of writes of the successful run, which must commit.
func walkWrites(t *testing.T, seed func(store.Store), op func(*Service, store.Tx) error) int {
	t.Helper()
	ctx := context.Background()
	for fail := 1; ; fail++ {
		for _, ignore := range []bool{false, true} {
			mem := memory.New()
			seed(mem)
			s, _ := New(mem, testPolicy())
			var before uint64
			_ = mem.View(ctx, "s", func(tx store.ReadTx) error { before = tx.LastSeq(); return nil })
			writes := 0
			var opErr error
			err := mem.Update(ctx, "s", func(tx store.Tx) error {
				opErr = op(s, faultTx{Tx: tx, writes: &writes, fail: fail})
				if ignore {
					return nil
				}
				return opErr
			})
			var after uint64
			_ = mem.View(ctx, "s", func(tx store.ReadTx) error { after = tx.LastSeq(); return nil })
			mem.Close()
			if writes < fail {
				if err != nil || opErr != nil || after == before {
					t.Fatalf("successful run (%d writes): op %v, commit %v, seq %d→%d", writes, opErr, err, before, after)
				}
				return writes
			}
			if !errors.Is(opErr, errInjected) {
				t.Fatalf("write %d: op returned %v, want injected failure", fail, opErr)
			}
			if err == nil || after != before {
				t.Fatalf("write %d (ignored=%v): commit %v, seq %d→%d", fail, ignore, err, before, after)
			}
		}
	}
}

func TestEveryConstituentWriteIsAtomic(t *testing.T) {
	user, harness, system := storetest.NewPrincipal("s", domain.AuthorityUser), storetest.NewPrincipal("s", domain.AuthorityHarness), storetest.NewPrincipal("s", domain.AuthoritySystem)
	seedSys := func(mem store.Store) {
		sys := storetest.NewItem("s", "sys", 0, "system fact")
		sys.Authority = domain.AuthoritySystem
		seedItem(t, mem, sys)
	}
	for name, tc := range map[string]struct {
		seed      func(store.Store)
		op        func(*Service, store.Tx) error
		minWrites int
	}{
		"complete task": {
			seed: func(mem store.Store) { seedCompletion(t, mem, []string{"g1", "g2"}, "", false) },
			op: func(s *Service, tx store.Tx) error {
				_, err := s.CompleteTask(tx, user, domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, tx.NextSeq())
				return err
			},
			// 2×(goal update + change), task, GC request, receipt
			minWrites: 7,
		},
		"collect": {
			seed: func(mem store.Store) { seedCollection(t, mem) },
			op: func(s *Service, tx store.Tx) error {
				_, err := s.Collect(tx, harness, domain.CollectIntent{RequestID: "c", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual}, tx.NextSeq())
				return err
			},
			// 2×(archive + change), collect receipt, receipt
			minWrites: 6,
		},
		"issue grant": {
			seed: seedSys,
			op: func(s *Service, tx store.Tx) error {
				_, err := s.IssueGrant(tx, system, archiveGrant("g", "grant", "sys", harness), tx.NextSeq())
				return err
			},
			minWrites: 2,
		},
		"archive": {
			seed: seedSys,
			op: func(s *Service, tx store.Tx) error {
				_, err := s.Archive(tx, system, domain.ArchiveIntent{RequestID: "a", ItemID: "sys", ExpectedVersion: 1}, tx.NextSeq())
				return err
			},
			minWrites: 3,
		},
		"replace directive": {
			seed: func(mem store.Store) { seedDirective(t, mem, domain.AuthorityUser, false) },
			op: func(s *Service, tx store.Tx) error {
				_, err := s.ReplaceDirective(tx, user, replaceIntent("r", 1, "ship it"), tx.NextSeq())
				return err
			},
			// item, declaration, edge, audit, retirement, current pointer, receipt
			minWrites: 7,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if n := walkWrites(t, tc.seed, tc.op); n < tc.minWrites {
				t.Fatalf("only %d writes were exercised, want at least %d", n, tc.minWrites)
			}
		})
	}
}

func TestCompletionGrantExpiringBetweenGoalsFailsWhole(t *testing.T) {
	for _, expire := range []bool{false, true} {
		t.Run(fmt.Sprintf("expire=%v", expire), func(t *testing.T) { completionWithGrants(t, expire) })
	}
}

// completionWithGrants resolves two SYSTEM goals through USER grants; with
// expire, the second grant lapses one sequence before its goal's mutation.
func completionWithGrants(t *testing.T, expire bool) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	if err := mem.Update(ctx, "s", func(tx store.Tx) error {
		for _, id := range []string{"g1", "g2"} {
			g := storetest.NewGoal("s", id, tx.NextSeq(), id)
			g.Scope, g.Access, g.Authority = domain.ScopeTask, storetest.DirectiveBoundary("s"), domain.AuthoritySystem
			if err := tx.InsertItem(g); err != nil {
				return err
			}
		}
		_, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task"))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	user := storetest.NewPrincipal("s", domain.AuthorityUser)
	var last uint64
	if err := mem.Update(ctx, "s", func(tx store.Tx) error {
		issuer := storetest.NewPrincipal("s", domain.AuthoritySystem)
		seq := tx.NextSeq()
		for n, id := range []string{"g1", "g2"} {
			// g1 is authorized at the completion's first seq; g2's grant is
			// valid through exactly that seq, so it lapses before g2's own.
			g := domain.MutationGrant{ID: fmt.Sprintf("grant-%d", n), SessionID: "s", Action: domain.ActionResolve, Targets: []domain.GrantTarget{domain.ItemGrantTarget("s", id)},
				Issuer: issuer, Grantee: &user, IssuedSeq: seq}
			if n == 1 && expire {
				g.ExpiresAtSeq = seq + 1
			}
			if err := tx.InsertGrant(g); err != nil {
				return err
			}
		}
		last = seq
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f := newFacets("g1", "g2")
	out, err := completeTask(f, mem, s, user, domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}, true)
	if !expire {
		if err != nil || len(out.Result.Completion.ResolvedGoals) != 2 {
			t.Fatalf("control completion: %+v %v", out, err)
		}
		return
	}
	if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatalf("expired mid-completion grant: %v", err)
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		g1, _ := tx.Item("g1")
		task, _ := tx.Task("task")
		if tx.LastSeq() != last || *g1.GoalStatus != domain.GoalOpen || task.Status != domain.TaskActive {
			t.Fatal("partial completion committed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
