package retrieve

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func TestGetHistoricalGoalIsReadOnly(t *testing.T) {
	s := memory.New()
	goal := storetest.NewGoal("s", "goal", 1, "finished work")
	goal.Scope = domain.ScopeTask
	goal.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "task"}
	goal.Residency = domain.ResidencyArchived
	resolved := domain.GoalResolved
	goal.GoalStatus = &resolved
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		goal.Seq = tx.NextSeq()
		return tx.InsertItem(goal)
	}); err != nil {
		t.Fatal(err)
	}
	principal := storetest.NewPrincipal("s", domain.AuthorityHarness)
	before := lastSeq(t, s)
	got, err := New(s).Get(context.Background(), principal, goal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Item.ID != goal.ID || *got.Observed.GoalStatus != domain.GoalResolved || got.Observed.Residency != domain.ResidencyArchived || got.Observed.Currentness != domain.ItemUnkeyed || got.Observed.Source.ContentHash != goal.ContentHash || got.SnapshotSeq != before {
		t.Fatalf("historical snapshot = %+v", got)
	}
	got.Item.Parts[0].Text = "changed"
	if after := lastSeq(t, s); after != before {
		t.Fatalf("Get wrote sequence: %d -> %d", before, after)
	}
	again, err := New(s).Get(context.Background(), principal, goal.ID)
	if err != nil || again.Item.Parts[0].Text != "finished work" {
		t.Fatalf("Get mutated stored content: %+v, %v", again, err)
	}
}

func TestGetPrivateAndMissingAreIndistinguishable(t *testing.T) {
	s := memory.New()
	item := storetest.NewItem("s", "private", 1, "secret")
	item.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "agent"}
	item.Scope = domain.ScopeAgent
	if err := s.Update(context.Background(), "s", func(tx store.Tx) error {
		item.Seq = tx.NextSeq()
		return tx.InsertItem(item)
	}); err != nil {
		t.Fatal(err)
	}
	p := storetest.NewPrincipal("s", domain.AuthorityHarness)
	p.AgentID = "other"
	for _, id := range []string{"private", "missing"} {
		_, err := New(s).Get(context.Background(), p, id)
		if !errors.Is(err, domain.ErrNotFound) || err.Error() != domain.ErrNotFound.Error() {
			t.Fatalf("Get(%s) = %v, want bare ErrNotFound", id, err)
		}
	}
}

func lastSeq(t *testing.T, s store.Store) uint64 {
	t.Helper()
	var seq uint64
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error { seq = tx.LastSeq(); return nil }); err != nil {
		t.Fatal(err)
	}
	return seq
}
