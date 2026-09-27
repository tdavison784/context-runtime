package retrieve

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
)

func chainPolicy() domain.Phase3Policy {
	p := leasePolicy()
	p.MaxTransactionWork, p.MaxPageSize, p.MaxCoverageMembers = 1024, 64, 64
	return p
}

// checkStoredProjection runs the dispatch-time checker on a committed result.
func checkStoredProjection(t *testing.T, s store.Store, p domain.Principal, projectionID string, pol domain.Phase3Policy) (string, error) {
	t.Helper()
	var item string
	var checkErr error
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		sem, err := store.ReadSemantic(tx)
		if err != nil {
			return err
		}
		rec, err := sem.Projection(projectionID)
		if err != nil {
			return err
		}
		task, err := tx.Task(p.TaskID)
		if err != nil {
			return err
		}
		conv, err := tx.Conversation(domain.ConversationIDFor(p.TaskID, p.AgentID))
		if err != nil {
			return err
		}
		item = rec.ItemID
		checkErr = CheckStoredProjectionDependencies(tx, sem, rec, p, task, conv, pol.MaxPageSize, pol.MaxTransactionWork)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return item, checkErr
}

// SEC-1.14: every committed re-projection is dispatchable while its leases
// are live, at any chain depth the policy admits.
func TestReprojectionChainStaysDispatchable(t *testing.T) {
	s := memory.New()
	defer s.Close()
	p, _ := seedLeaseStore(t, s)
	svc, pol, item := New(s), chainPolicy(), "source"
	for depth := 1; depth <= 5; depth++ {
		i := harnessIntent(p, fmt.Sprintf("chain-%d", depth))
		i.Rehydrate.ItemID = item
		r, err := svc.Rehydrate(context.Background(), p, i, pol, false)
		if err != nil {
			t.Fatalf("rehydrate depth %d: %v", depth, err)
		}
		var checkErr error
		if item, checkErr = checkStoredProjection(t, s, p, r.ProjectionID, pol); checkErr != nil {
			t.Fatalf("depth %d projection not dispatchable: %v", depth, checkErr)
		}
	}
}

// SEC-1.14: when bounds cannot hold a deeper chain, rehydration fails with a
// fixed error and commits nothing it could not later dispatch.
func TestReprojectionBeyondBoundsIsRejectedNotCommitted(t *testing.T) {
	s := memory.New()
	defer s.Close()
	p, _ := seedLeaseStore(t, s)
	svc, pol, item := New(s), leasePolicy(), "source"
	for depth := 1; ; depth++ {
		if depth > 32 {
			t.Fatal("finite bounds admitted an unbounded chain")
		}
		i := harnessIntent(p, fmt.Sprintf("bounded-%d", depth))
		i.Rehydrate.ItemID = item
		r, err := svc.Rehydrate(context.Background(), p, i, pol, false)
		if err != nil {
			if depth < 2 || !errors.Is(err, ErrRetrievalInvalidArgument) {
				t.Fatalf("depth %d rejection = %v, want fixed invalid-argument", depth, err)
			}
			return
		}
		var checkErr error
		if item, checkErr = checkStoredProjection(t, s, p, r.ProjectionID, pol); checkErr != nil {
			t.Fatalf("depth %d committed an undispatchable projection: %v", depth, checkErr)
		}
	}
}
