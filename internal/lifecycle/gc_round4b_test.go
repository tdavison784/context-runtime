package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// SEC-4.5 (J5 wedge): a pending request's continuation binds to authority
// class and task, not the exact principal that ran batch 1. A same-task
// HARNESS with another AgentID finishes the request; binding to the first
// collector left it pending forever (uncollectable, never FAILED, one
// CollectPending attempt every cycle).
func TestContinuationIsNotBoundToTheFirstCollector_SEC45(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxGCDecisions = 1
		s, _ := New(db, pol)
		seedEphemeral(t, db, 3, 0)
		id := enqueueScratch(t, db, s)
		first := storetest.NewPrincipal("s", domain.AuthorityHarness)
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			_, err := s.ExecuteGCRequest(tx, first, id, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		readSemantic(t, db, func(sem store.SemanticReader) error {
			p, err := sem.GCProgress(id)
			if p.Batches != 1 {
				t.Fatalf("setup: %d batches", p.Batches)
			}
			return err
		})
		mate := first
		mate.AgentID = "agent-2"
		for range 10 {
			if _, ok := gcResult(t, db, id); ok {
				break
			}
			if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
				_, err := s.ExecuteGCRequest(tx, mate, id, 0)
				return err
			}); err != nil {
				t.Fatalf("same-task mate cannot continue: %v", err)
			}
		}
		res, found := gcResult(t, db, id)
		if !found || res.Outcome != domain.GCCollected {
			t.Fatalf("request wedged on its first collector: %+v found=%v", res, found)
		}
	})
}
