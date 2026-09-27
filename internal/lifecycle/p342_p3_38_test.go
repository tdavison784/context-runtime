package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_38_ExpiredLeaseReleasesOnlyLeaseProtection closes the P3-42 table
// row "expired lease releases only lease protection": a lease exhausted by
// its holder's completed-inference counter protects nothing — an
// otherwise-collectible item under it is archived exactly as a lease-free
// twin — while the same expired lease over an independent requirement
// (OPEN goal) leaves that requirement protected, and a possibly-live lease
// still protects a lease-free-collectible twin.
func TestP3_38_ExpiredLeaseReleasesOnlyLeaseProtection(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			task := storetest.NewTask("s", "task")
			task.Turn, task.TurnID = 2, "turn-2"
			if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
				return err
			}
			hashes := map[string]string{}
			for _, id := range []string{"eph-free", "eph-exp-lease", "eph-live-lease", "goal-exp-lease"} {
				var it domain.ContextItem
				if id == "goal-exp-lease" {
					it = storetest.NewGoal("s", id, tx.NextSeq(), "open goal")
				} else {
					it = storetest.NewItem("s", id, tx.NextSeq(), id)
					it.Generation = domain.GenerationEphemeral
				}
				hashes[id] = it.ContentHash
				if err := tx.InsertItem(it); err != nil {
					return err
				}
			}
			// One exhausted holder conversation serves both expired leases:
			// issued 0 + allowance 2 calls completed.
			holder := storetest.NewPrincipal("s", domain.AuthorityAgent)
			conv := storetest.NewConversation("s", domain.ConversationIDFor(holder.TaskID, holder.AgentID))
			conv.LogicalCalls = 2
			if _, err := tx.PutConversation(conv, 0); err != nil {
				return err
			}
			for _, id := range []string{"eph-exp-lease", "goal-exp-lease"} {
				if err := insertLeaseTx(tx, holder, "lease-"+id, id, hashes[id]); err != nil {
					return err
				}
			}
			// An unknown holder conversation keeps the lease possibly live.
			watcher := storetest.NewPrincipal("s", domain.AuthorityAgent)
			watcher.AgentID = "watcher"
			return insertLeaseTx(tx, watcher, "lease-eph-live-lease", "eph-live-lease", hashes["eph-live-lease"])
		}); err != nil {
			t.Fatal(err)
		}
		s, _ := New(db, testPolicy())
		out, err := collect(newFacets(), db, s, storetest.NewPrincipal("s", domain.AuthoritySystem), domain.CollectIntent{RequestID: "c", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual})
		if err != nil || out.Result.Collect == nil {
			t.Fatalf("collect: %+v %v", out, err)
		}
		want := map[string]domain.GCDecisionCode{
			"eph-free": domain.GCArchive, "eph-exp-lease": domain.GCArchive, "eph-live-lease": domain.GCProtected, "goal-exp-lease": domain.GCProtected,
		}
		got := map[string]domain.GCDecisionCode{}
		for _, d := range out.Result.Collect.Decisions {
			got[d.Target.ItemID] = d.Code
		}
		if len(got) != len(want) {
			t.Fatalf("decisions %v, want exactly %v", got, want)
		}
		for id, code := range want {
			if got[id] != code {
				t.Errorf("%s = %s, want %s", id, got[id], code)
			}
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			for id, code := range want {
				it, _ := tx.Item(id)
				if (it.Residency == domain.ResidencyArchived) != (code == domain.GCArchive) {
					t.Fatalf("%s residency %s after code %s", id, it.Residency, code)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func insertLeaseTx(tx store.Tx, holder domain.Principal, id, itemID, contentHash string) error {
	sem, err := store.Semantic(tx)
	if err != nil {
		return err
	}
	return sem.InsertRetrievalLease(domain.RetrievalLease{SemanticMeta: domain.SemanticMeta{ID: id, SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
		Holder: holder, ConversationID: domain.ConversationIDFor(holder.TaskID, holder.AgentID), TurnID: "turn-2",
		Source: domain.ItemContentRef{ItemID: itemID, ContentHash: contentHash}, CallAllowance: 2, PolicyVersion: "lease/v1"})
}

// TestP3_38_SupersededSystemInstructionCollectibleWithProperActor closes the
// P3-42 table row "superseded SYSTEM instruction collectible with proper
// actor": a superseded SYSTEM instruction is collectible — but only for the
// SYSTEM actor; a HARNESS collector is told INELIGIBLE and nothing moves,
// while the SYSTEM collector archives the superseded occurrence and still
// protects its current successor.
func TestP3_38_SupersededSystemInstructionCollectibleWithProperActor(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			task := storetest.NewTask("s", "task")
			task.Turn, task.TurnID = 2, "turn-2"
			if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
				return err
			}
			for _, it := range []domain.ContextItem{
				storetest.NewDirective("s", "sys-old", "sd", tx.NextSeq(), "first"),
				storetest.NewDirective("s", "sys-new", "sd", tx.NextSeq(), "second"),
			} {
				it.Authority = domain.AuthoritySystem
				it.Access = storetest.DirectiveBoundary("s")
				if err := tx.InsertItem(it); err != nil {
					return err
				}
			}
			if err := storetest.UncheckedSetCurrentVersion(tx, "sys-old"); err != nil {
				return err
			}
			return storetest.UncheckedSetCurrentVersion(tx, "sys-new")
		}); err != nil {
			t.Fatal(err)
		}
		s, _ := New(db, testPolicy())
		intent := func(id string) domain.CollectIntent {
			return domain.CollectIntent{RequestID: id, Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual}
		}

		// The HARNESS collector lacks authority over SYSTEM content: the
		// superseded instruction is INELIGIBLE and stays resident. The current
		// successor is PROTECTED regardless of actor (requirement outranks
		// authorization, which is only consulted for archive candidates).
		harnessOut, err := collect(newFacets(), db, s, storetest.NewPrincipal("s", domain.AuthorityHarness), intent("c-harness"))
		if err != nil || harnessOut.Result.Collect == nil {
			t.Fatalf("harness collect: %+v %v", harnessOut, err)
		}
		codes := map[string]domain.GCDecisionCode{}
		for _, d := range harnessOut.Result.Collect.Decisions {
			codes[d.Target.ItemID] = d.Code
		}
		if codes["sys-old"] != domain.GCIneligible || codes["sys-new"] != domain.GCProtected {
			t.Fatalf("harness decisions: %v", codes)
		}

		// The SYSTEM collector archives the superseded occurrence and keeps
		// the current one.
		sysOut, err := collect(newFacets(), db, s, storetest.NewPrincipal("s", domain.AuthoritySystem), intent("c-system"))
		if err != nil || sysOut.Result.Collect == nil {
			t.Fatalf("system collect: %+v %v", sysOut, err)
		}
		codes = map[string]domain.GCDecisionCode{}
		for _, d := range sysOut.Result.Collect.Decisions {
			codes[d.Target.ItemID] = d.Code
		}
		if codes["sys-old"] != domain.GCArchive || codes["sys-new"] != domain.GCProtected {
			t.Fatalf("system decisions: %v", codes)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			old, _ := tx.Item("sys-old")
			cur, _ := tx.Item("sys-new")
			if old.Residency != domain.ResidencyArchived || cur.Residency != domain.ResidencyResident {
				t.Fatalf("residencies: old=%s new=%s", old.Residency, cur.Residency)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestP3_38_ArchiveUnarchiveThenOldRequestReplays closes the P3-42 table row
// "archive→unarchive→old request replay": after an archive commits and a
// later request unarchives the item, retrying the old archive request
// replays its frozen result — same version transition and audit ID — without
// re-executing against the now-resident item.
func TestP3_38_ArchiveUnarchiveThenOldRequestReplays(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		seedItem(t, db, storetest.NewItem("s", "fact", 0, "plain"))
		s, _ := New(db, testPolicy())
		p := storetest.NewPrincipal("s", domain.AuthorityUser)
		first, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "a", ItemID: "fact", ExpectedVersion: 1})
		if err != nil || first.After.Residency != domain.ResidencyArchived || first.AfterVersion != 2 {
			t.Fatalf("archive: %+v %v", first, err)
		}
		if _, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u", ItemID: "fact", ExpectedVersion: 2}); err != nil {
			t.Fatal(err)
		}
		again, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "a", ItemID: "fact", ExpectedVersion: 1})
		if err != nil || again.AfterVersion != first.AfterVersion || again.AuditID != first.AuditID || again.After.Residency != domain.ResidencyArchived {
			t.Fatalf("old archive retry: %+v %v, want frozen %+v", again, err, first)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			it, err := tx.Item("fact")
			if err != nil || it.Residency != domain.ResidencyResident || it.Version != 3 {
				t.Fatalf("replayed archive re-executed: %+v %v", it, err)
			}
			events, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetItem, TargetID: "fact"})
			if err != nil || len(events) != 2 {
				t.Fatalf("lifecycle events: %d %v", len(events), err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestP3_38_NoLastUsedCallZeroHeuristic closes the P3-42 table row "no
// LastUsedCall=0 heuristic": the pure decision never consults a usage
// counter — items differing only in LastUsedCall/AccessCount get identical
// (code, reason) in every snapshot shape — and at the service level a
// never-used (LastUsedCall 0) ended-turn ephemeral is archived exactly like
// its used twin.
func TestP3_38_NoLastUsedCallZeroHeuristic(t *testing.T) {
	// Pure rule: GCSnapshot carries no counter, and no item counter moves a
	// decision.
	shapes := []struct {
		name    string
		mut     func(*domain.ContextItem)
		current domain.ItemCurrentness
		code    domain.GCDecisionCode
		why     policy.GCReason
	}{
		{"ended-turn ephemeral", func(it *domain.ContextItem) { it.Generation = domain.GenerationEphemeral }, domain.ItemUnkeyed, domain.GCArchive, policy.GCReasonEndedTurn},
		{"live working fact", func(*domain.ContextItem) {}, domain.ItemUnkeyed, domain.GCIneligible, policy.GCReasonLive},
		{"superseded fact", func(*domain.ContextItem) {}, domain.ItemHistorical, domain.GCArchive, policy.GCReasonStale},
	}
	for _, shape := range shapes {
		task := domain.TaskState{SessionID: "s", TaskID: "t", WorkflowID: "wf", Status: domain.TaskActive, Turn: 2, TurnID: "turn-2", Version: 1}
		build := func(used uint64, accesses int) domain.ContextItem {
			parts := []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: "x"}}
			it := domain.ContextItem{ID: "i", EventID: "e", Seq: 1, SessionID: "s", WorkflowID: "wf", TaskID: "t", AgentID: "a", TurnID: "turn-1", CreatedTurn: 1,
				Kind: domain.KindFact, Generation: domain.GenerationWorking, Authority: domain.AuthorityUser, Scope: domain.ScopeTask,
				Access: domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: "s", TaskID: "t"}, Residency: domain.ResidencyResident,
				Retention: domain.RetentionNormal, Parts: parts, Version: 1, LastUsedCall: used, AccessCount: accesses}
			it.ContentHash, it.SemanticBytes = domain.ContentHash(parts), domain.SemanticBytes(parts)
			shape.mut(&it)
			return it
		}
		snap := func(it domain.ContextItem) policy.GCSnapshot {
			return policy.GCSnapshot{OwnerSnapshot: policy.OwnerSnapshot{Seq: 10, Task: &task}, Item: domain.ItemRevisionRef{ItemID: it.ID, Version: it.Version},
				Currentness: shape.current, ObligationsKnown: true}
		}
		baseCode, baseWhy, err := policy.CollectDecision(build(0, 0), snap(build(0, 0)))
		if err != nil {
			t.Fatalf("%s: %v", shape.name, err)
		}
		if baseCode != shape.code || baseWhy != shape.why {
			t.Fatalf("%s: fixture decided %s/%s", shape.name, baseCode, baseWhy)
		}
		for _, mutated := range []domain.ContextItem{build(42, 7), build(^uint64(0), 1<<20)} {
			code, why, err := policy.CollectDecision(mutated, snap(mutated))
			if err != nil || code != baseCode || why != baseWhy {
				t.Errorf("%s: usage counter moved the decision: %s/%s %v vs %s/%s", shape.name, code, why, err, baseCode, baseWhy)
			}
		}
	}

	// Service level: the never-used ephemeral archives like the used one.
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			task := storetest.NewTask("s", "task")
			task.Turn, task.TurnID = 2, "turn-2"
			if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
				return err
			}
			for _, id := range []string{"never", "used"} {
				it := storetest.NewItem("s", id, tx.NextSeq(), id)
				it.Generation = domain.GenerationEphemeral
				if id == "used" {
					it.LastUsedCall = 42
				}
				if err := tx.InsertItem(it); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		s, _ := New(db, testPolicy())
		out, err := collect(newFacets(), db, s, storetest.NewPrincipal("s", domain.AuthoritySystem), domain.CollectIntent{RequestID: "c", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual})
		if err != nil || out.Result.Collect == nil {
			t.Fatalf("collect: %+v %v", out, err)
		}
		codes := map[string]domain.GCDecisionCode{}
		for _, d := range out.Result.Collect.Decisions {
			codes[d.Target.ItemID] = d.Code
		}
		if codes["never"] != domain.GCArchive || codes["used"] != domain.GCArchive {
			t.Fatalf("decisions: %v", codes)
		}
	})
}
