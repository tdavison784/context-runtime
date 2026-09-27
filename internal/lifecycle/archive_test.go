package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func seedItem(t *testing.T, mem store.Store, it domain.ContextItem) {
	t.Helper()
	if err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
		it.Seq = tx.NextSeq()
		return tx.InsertItem(it)
	}); err != nil {
		t.Fatal(err)
	}
}

func grantTo(t *testing.T, mem store.Store, id string, action domain.Action, itemID string, grantee domain.Principal) {
	t.Helper()
	issuer := storetest.NewPrincipal("s", domain.AuthoritySystem)
	if err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
		return tx.InsertGrant(domain.MutationGrant{ID: id, SessionID: "s", Action: action, Targets: []domain.GrantTarget{domain.ItemGrantTarget("s", itemID)},
			Issuer: issuer, Grantee: &grantee, IssuedSeq: tx.NextSeq()})
	}); err != nil {
		t.Fatal(err)
	}
}

func TestArchiveRequiresTargetAuthorityOrExactGrant(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	sys := storetest.NewItem("s", "sys", 0, "system fact")
	sys.Authority = domain.AuthoritySystem
	seedItem(t, mem, sys)
	intent := domain.ArchiveIntent{RequestID: "r", ItemID: "sys", ExpectedVersion: 1}
	for _, a := range []domain.Authority{domain.AuthorityHarness, domain.AuthorityUser, domain.AuthorityAgent, domain.AuthorityTool} {
		if _, err := s.ArchiveStandalone(ctx, storetest.NewPrincipal("s", a), intent); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("%s archived SYSTEM target: %v", a, err)
		}
	}
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	grantTo(t, mem, "unarchive-only", domain.ActionUnarchive, "sys", harness)
	if _, err := s.ArchiveStandalone(ctx, harness, intent); !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatalf("grant for another action authorized archive: %v", err)
	}
	grantTo(t, mem, "archive-grant", domain.ActionArchive, "sys", harness)
	var out LifecycleOutcome
	if err := mem.Update(ctx, "s", func(tx store.Tx) error {
		var err error
		out, err = s.Archive(tx, harness, intent, tx.NextSeq())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	r := out.Result
	if out.GrantID != "archive-grant" || out.MutationReceiptID == "" || r.Before.Residency != domain.ResidencyResident || r.After.Residency != domain.ResidencyArchived ||
		r.After.Authority != domain.AuthoritySystem || r.After.Generation != r.Before.Generation || r.Before.Currentness != domain.ItemUnkeyed || r.ExplicitProtectedRemoval {
		t.Fatalf("archive outcome: %+v", out)
	}
	private := storetest.NewItem("s", "private", 0, "private")
	private.AgentID, private.Access = "other", domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "other"}
	private.Scope = domain.ScopeAgent
	seedItem(t, mem, private)
	for _, id := range []string{"private", "missing"} {
		if _, err := s.ArchiveStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.ArchiveIntent{RequestID: "r-" + id, ItemID: id, ExpectedVersion: 1}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s: %v", id, err)
		}
	}
}

func TestArchiveDisclosesExplicitProtectedRemoval(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	goal := storetest.NewGoal("s", "goal", 0, "open goal")
	pinned := storetest.NewItem("s", "pinned", 0, "pinned fact")
	pinned.Generation = domain.GenerationPinned
	source := storetest.NewItem("s", "source", 0, "obligation source")
	plain := storetest.NewItem("s", "plain", 0, "plain fact")
	for _, it := range []domain.ContextItem{goal, pinned, source, plain} {
		seedItem(t, mem, it)
	}
	if err := mem.Update(ctx, "s", func(tx store.Tx) error {
		return tx.InsertObligationVersion(storetest.NewObligation("s", "o", 1, tx.NextSeq(), "source"))
	}); err != nil {
		t.Fatal(err)
	}
	p := storetest.NewPrincipal("s", domain.AuthorityUser)
	for id, want := range map[string]bool{"goal": true, "pinned": true, "source": true, "plain": false} {
		r, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "r-" + id, ItemID: id, ExpectedVersion: 1})
		if err != nil || r.ExplicitProtectedRemoval != want {
			t.Fatalf("%s: %+v %v", id, r, err)
		}
		if id == "goal" && *r.After.GoalStatus != domain.GoalOpen {
			t.Fatal("archive changed goal status")
		}
	}
}

func TestUnarchiveRestoresResidencyOnlyAndReplays(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	goal := storetest.NewGoal("s", "goal", 0, "resolved goal")
	resolved := domain.GoalResolved
	goal.GoalStatus, goal.Residency = &resolved, domain.ResidencyArchived
	seedItem(t, mem, goal)
	p := storetest.NewPrincipal("s", domain.AuthorityUser)
	if _, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "a", ItemID: "goal", ExpectedVersion: 1}); !errors.Is(err, graph.ErrLifecycleTargetMismatch) {
		t.Fatalf("archived an archived item: %v", err)
	}
	if _, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u", ItemID: "goal", ExpectedVersion: 2}); !errors.Is(err, domain.ErrVersionConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	first, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u", ItemID: "goal", ExpectedVersion: 1})
	if err != nil || first.After.Residency != domain.ResidencyResident || *first.After.GoalStatus != domain.GoalResolved {
		t.Fatalf("unarchive: %+v %v", first, err)
	}
	if _, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "a2", ItemID: "goal", ExpectedVersion: 2}); err != nil {
		t.Fatal(err)
	}
	again, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u", ItemID: "goal", ExpectedVersion: 1})
	if err != nil || again.AfterVersion != first.AfterVersion || again.AuditID != first.AuditID {
		t.Fatalf("replay after later archive: %+v %v", again, err)
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		it, err := tx.Item("goal")
		if err != nil || it.Version != 3 || it.Residency != domain.ResidencyArchived || it.ContentHash != goal.ContentHash || len(it.Parts) != 1 {
			t.Fatalf("content or state changed: %+v %v", it, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
