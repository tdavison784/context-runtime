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

// seedCollection commits task "task" at turn 2 plus one candidate per rule.
func seedCollection(t *testing.T, mem store.Store) *facets {
	t.Helper()
	f := newFacets("old", "new", "eph", "live", "leased", "sys", "private", "now")
	if err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
		task := storetest.NewTask("s", "task")
		task.Turn, task.TurnID = 2, "turn-2"
		if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
			return err
		}
		items := []domain.ContextItem{
			storetest.NewDirective("s", "old", "d", tx.NextSeq(), "first"),
			storetest.NewDirective("s", "new", "d", tx.NextSeq(), "second"),
		}
		for _, id := range []string{"eph", "live", "leased", "sys", "private", "now"} {
			it := storetest.NewItem("s", id, tx.NextSeq(), id)
			it.Generation = domain.GenerationEphemeral
			switch id {
			case "live":
				it.Generation = domain.GenerationWorking
			case "sys":
				it.Authority = domain.AuthoritySystem
			case "private":
				it.Scope, it.AgentID, it.Access = domain.ScopeAgent, "other", domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", AgentID: "other"}
			case "now":
				it.TurnID = "turn-2"
			}
			items = append(items, it)
		}
		for _, it := range items {
			if err := tx.InsertItem(it); err != nil {
				return err
			}
		}
		if err := tx.SetCurrentVersion("old"); err != nil {
			return err
		}
		return tx.SetCurrentVersion("new")
	}); err != nil {
		t.Fatal(err)
	}
	// A lease whose holder state is unknown counts as possibly live.
	f.leases["leased"] = []domain.RetrievalLease{{SemanticMeta: domain.SemanticMeta{ID: "lease", SessionID: "s", Seq: 1}, Holder: storetest.NewPrincipal("s", domain.AuthorityAgent)}}
	f.leases["leased"][0].Holder.TaskID = "ghost"
	return f
}

func collect(f *facets, mem store.Store, s *Service, p domain.Principal, i domain.CollectIntent) (MutationOutcome, error) {
	var out MutationOutcome
	err := f.update(mem, func(tx store.Tx) error {
		var err error
		out, err = s.Collect(tx, p, i, tx.NextSeq())
		return err
	})
	return out, err
}

func TestCollectArchivesOnlyAuthorizedUnprotectedCandidates(t *testing.T) {
	ctx := context.Background()
	mem := memory.New()
	t.Cleanup(func() { mem.Close() })
	s, _ := New(mem, testPolicy())
	f := seedCollection(t, mem)
	harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
	intent := domain.CollectIntent{RequestID: "c1", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual}
	out, err := collect(f, mem, s, harness, intent)
	if err != nil {
		t.Fatal(err)
	}
	r := out.Result.Collect
	if r == nil || r.Validate() != nil || out.MutationReceiptID == "" {
		t.Fatalf("receipt: %+v", out)
	}
	want := map[string]domain.GCDecisionCode{"old": domain.GCArchive, "new": domain.GCProtected, "eph": domain.GCArchive, "live": domain.GCIneligible,
		"leased": domain.GCProtected, "sys": domain.GCIneligible, "now": domain.GCProtected}
	if len(r.Decisions) != len(want) {
		t.Fatalf("decisions (private must be absent): %+v", r.Decisions)
	}
	for n, d := range r.Decisions {
		if want[d.Target.ItemID] != d.Code || n > 0 && r.CandidateRefs[n-1].ItemID == d.Target.ItemID {
			t.Fatalf("%s: %s, want %s", d.Target.ItemID, d.Code, want[d.Target.ItemID])
		}
	}
	if len(r.ArchivedRefs) != 2 || f.collects[r.ID].RequestID != "c1" {
		t.Fatalf("archived %+v, stored %+v", r.ArchivedRefs, f.collects[r.ID])
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		for id, code := range want {
			it, _ := tx.Item(id)
			if (it.Residency == domain.ResidencyArchived) != (code == domain.GCArchive) {
				t.Fatalf("%s residency %s", id, it.Residency)
			}
		}
		events, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetItem, TargetID: "eph"})
		if len(events) != 1 || events[0].Actor != harness || events[0].Action != string(domain.ActionArchive) {
			t.Fatalf("archive audit: %+v", events)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	// Archive→unarchive→old request retry replays; it does not recollect.
	if _, err := s.UnarchiveStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.UnarchiveIntent{RequestID: "u", ItemID: "eph", ExpectedVersion: 2}); err != nil {
		t.Fatal(err)
	}
	again, err := collect(f, mem, s, harness, intent)
	if err != nil || again.Result.Collect.ID != r.ID || len(again.Result.Collect.ArchivedRefs) != 2 {
		t.Fatalf("replay: %+v %v", again, err)
	}
	if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
		if it, _ := tx.Item("eph"); it.Residency != domain.ResidencyResident || it.Version != 3 {
			t.Fatalf("replay recollected: %+v", it)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestCollectFailsClosedAtomically(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		actor  domain.Authority
		policy func(*domain.Phase3Policy)
		intent domain.CollectIntent
		want   error
	}{
		"user collector":     {domain.AuthorityUser, nil, domain.CollectIntent{RequestID: "c", Scope: domain.CollectSession, Trigger: domain.GCManual}, domain.ErrInvalidAuthorityPromotion},
		"agent collector":    {domain.AuthorityAgent, nil, domain.CollectIntent{RequestID: "c", Scope: domain.CollectSession, Trigger: domain.GCManual}, domain.ErrInvalidAuthorityPromotion},
		"unknown task":       {domain.AuthoritySystem, nil, domain.CollectIntent{RequestID: "c", Scope: domain.CollectTask, TaskID: "nope", Trigger: domain.GCManual}, domain.ErrNotFound},
		"decision overflow":  {domain.AuthoritySystem, func(p *domain.Phase3Policy) { p.MaxGCDecisions = 3 }, domain.CollectIntent{RequestID: "c", Scope: domain.CollectSession, Trigger: domain.GCManual}, domain.ErrResourceLimit},
		"work bound overrun": {domain.AuthoritySystem, func(p *domain.Phase3Policy) { p.MaxTransactionWork = 30 }, domain.CollectIntent{RequestID: "c", Scope: domain.CollectSession, Trigger: domain.GCManual}, domain.ErrResourceLimit},
	} {
		t.Run(name, func(t *testing.T) {
			mem := memory.New()
			t.Cleanup(func() { mem.Close() })
			pol := testPolicy()
			if tc.policy != nil {
				tc.policy(&pol)
			}
			s, err := New(mem, pol)
			if err != nil {
				t.Fatal(err)
			}
			f := seedCollection(t, mem)
			if _, err := collect(f, mem, s, storetest.NewPrincipal("s", tc.actor), tc.intent); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if len(f.collects) != 0 {
				t.Fatal("failed collection stored a receipt")
			}
			if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
				for _, id := range f.items {
					if it, _ := tx.Item(id); it.Residency != domain.ResidencyResident || it.Version != 1 {
						t.Fatalf("partial collection archived %s", id)
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
