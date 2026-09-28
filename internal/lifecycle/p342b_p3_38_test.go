package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// seedP3_38 commits task "task" at turn 2 with two ended-turn ephemeral
// items: "x-eph", which will belong to a logical exchange, and "ctl-eph",
// the unprotected control.
func seedP3_38(t *testing.T, db store.Store) {
	t.Helper()
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		task := storetest.NewTask("s", "task")
		task.Turn, task.TurnID = 2, "turn-2"
		if _, err := tx.PutTask(task, 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
			return err
		}
		for _, id := range []string{"x-eph", "ctl-eph"} {
			it := storetest.NewItem("s", id, tx.NextSeq(), id)
			it.Generation = domain.GenerationEphemeral
			if err := tx.InsertItem(it); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// p3_38OpenExchange inserts an OPEN exchange over item x-eph's content and
// returns its id.
func p3_38OpenExchange(t *testing.T, db store.Store, itemID string) string {
	t.Helper()
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		it, err := tx.Item(itemID)
		if err != nil {
			return err
		}
		x := storetest.NewExchange("s", "x-open", "task", "agent", 1, tx.NextSeq())
		if err := sem.InsertLogicalExchange(x); err != nil {
			return err
		}
		return sem.InsertExchangeMember(domain.ExchangeMember{SemanticMeta: storetest.Meta("s", "member-x", tx.NextSeq()),
			ExchangeID: "x-open", Position: 1, Role: domain.MemberInput, Source: storetest.ContentRef(it)})
	}); err != nil {
		t.Fatal(err)
	}
	return "x-open"
}

// p3_38CloseExchange closes and acknowledges the exchange through its
// consuming inference: generation-input coverage, conversation membership,
// admission manifest, acknowledgment, then the state change.
func p3_38CloseExchange(t *testing.T, db store.Store, exchangeID, itemID string) {
	t.Helper()
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		it, err := tx.Item(itemID)
		if err != nil {
			return err
		}
		conv := domain.ConversationIDFor("task", "agent")
		cov, members := storetest.NewCoverage(t, "s", "gen-38", tx.NextSeq(), domain.CoverageGenerationInput, storetest.ContentRef(it))
		if err := sem.InsertCoverage(cov, members); err != nil {
			return err
		}
		if _, err := sem.PutConversationMembership(domain.ConversationMembershipState{SemanticMeta: storetest.Meta("s", "ms-38", tx.NextSeq()),
			ConversationID: conv, Revision: 1, LastOrdinal: 1}, 0); err != nil {
			return err
		}
		if err := sem.InsertAdmissionManifest(domain.AdmissionManifest{SemanticMeta: storetest.Meta("s", "adm-38", tx.NextSeq()), ConversationID: conv,
			ExchangeID: exchangeID, CallID: "call-38", Principal: storetest.AgentPrincipal("s", "task", "agent"),
			TurnID: "turn-1", Purpose: domain.AdmissionGenerationInput, CoverageID: "gen-38", MembershipRevision: 1,
			PolicyVersion: domain.Phase3PolicyVersion}); err != nil {
			return err
		}
		ack := domain.ExchangeAcknowledgment{SemanticMeta: storetest.Meta("s", "ack-38", tx.NextSeq()), ExchangeID: exchangeID,
			ManifestID: "adm-38", ConsumingCallID: "call-38", Actor: storetest.HarnessPrincipal("s")}
		if err := sem.InsertExchangeAcknowledgment(ack); err != nil {
			return err
		}
		x, err := sem.LogicalExchange(exchangeID)
		if err != nil {
			return err
		}
		x.State, x.AcknowledgmentID = domain.ExchangeClosed, ack.ID
		_, err = sem.PutLogicalExchange(x, x.Revision)
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// TestP3_38_OpenExchangeMemberSurvivesCollectUntilClosed closes the P3-42
// table row "open exchange and leased historical content survive" (ADR8:1285),
// exchange half (the lease half is TestLeaseTakenAfterFirstBatchProtects_SEC42
// and TestP3_38_ExpiredLeaseReleasesOnlyLeaseProtection). An item that belongs
// to an OPEN logical exchange gets an explicit PROTECTED decision and stays
// RESIDENT even though it is otherwise collectible (ended-turn ephemeral),
// while an identical unprotected control is archived in the same collection.
// After the exchange closes and is acknowledged, the same item becomes
// collectible and the next collection archives it.
func TestP3_38_OpenExchangeMemberSurvivesCollectUntilClosed(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		seedP3_38(t, db)
		p3_38OpenExchange(t, db, "x-eph")
		f := newFacets("x-eph", "ctl-eph")
		s, _ := New(db, testPolicy())
		harness := storetest.NewPrincipal("s", domain.AuthorityHarness)

		first, err := collect(f, db, s, harness, domain.CollectIntent{RequestID: "c38a", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual})
		if err != nil || first.Result.Collect == nil {
			t.Fatalf("collect: %+v %v", first, err)
		}
		codes := map[string]domain.GCDecisionCode{}
		for _, d := range first.Result.Collect.Decisions {
			codes[d.Target.ItemID] = d.Code
		}
		if codes["x-eph"] != domain.GCProtected {
			t.Fatalf("open-exchange member not PROTECTED: %+v", first.Result.Collect.Decisions)
		}
		if codes["ctl-eph"] != domain.GCArchive {
			t.Fatalf("unprotected control not archived: %+v", first.Result.Collect.Decisions)
		}
		if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
			for id, want := range map[string]domain.Residency{"x-eph": domain.ResidencyResident, "ctl-eph": domain.ResidencyArchived} {
				it, err := tx.Item(id)
				if err != nil || it.Residency != want {
					t.Fatalf("%s residency %s (err %v), want %s", id, it.Residency, err, want)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// The exchange closes and is acknowledged: the same item is now
		// collectible and the next collection archives it.
		p3_38CloseExchange(t, db, "x-open", "x-eph")
		second, err := collect(f, db, s, harness, domain.CollectIntent{RequestID: "c38b", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual})
		if err != nil || second.Result.Collect == nil {
			t.Fatalf("collect after closure: %+v %v", second, err)
		}
		archived := map[string]bool{}
		for _, ref := range second.Result.Collect.ArchivedRefs {
			archived[ref.ItemID] = true
		}
		if !archived["x-eph"] {
			t.Fatalf("closed-exchange member not archived: %+v", second.Result.Collect.Decisions)
		}
		if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
			it, err := tx.Item("x-eph")
			if err != nil || it.Residency != domain.ResidencyArchived {
				t.Fatalf("closed-exchange member residency %s (err %v)", it.Residency, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// TestP3_38_InaccessibleCandidateNotExposedSQLite closes the P3-42 table row
// "inaccessible candidate not exposed" (ADR8:1291). The cited test
// (collect_test.go:87) runs on the memory store only; this is the SQLite
// half: an agent-private item is never named in the decision list, receipts,
// archived refs or audit events, while every readable candidate is decided.
func TestP3_38_InaccessibleCandidateNotExposedSQLite(t *testing.T) {
	ctx := context.Background()
	t.Run("sqlite", func(t *testing.T) { // the memory half is collect_test.go's own
		db := sqlitetest.Open(t)
		s, _ := New(db, testPolicy())
		f := seedCollection(t, db)
		harness := storetest.NewPrincipal("s", domain.AuthorityHarness)
		out, err := collect(f, db, s, harness, domain.CollectIntent{RequestID: "c39b", Scope: domain.CollectTask, TaskID: "task", Trigger: domain.GCManual})
		if err != nil {
			t.Fatal(err)
		}
		r := out.Result.Collect
		if r == nil || r.Validate() != nil || out.MutationReceiptID == "" {
			t.Fatalf("receipt: %+v", out)
		}
		for _, d := range r.Decisions {
			if d.Target.ItemID == "private" {
				t.Fatalf("inaccessible candidate exposed in decisions: %+v", d)
			}
		}
		for _, ref := range r.CandidateRefs {
			if ref.ItemID == "private" {
				t.Fatalf("inaccessible candidate exposed in frozen candidates: %+v", ref)
			}
		}
		// ...and every READABLE candidate is decided (SPEC-6.8): an empty
		// decision list is not a passing collection. The expected set is the
		// memory half's (collect_test.go): one candidate per rule, minus the
		// inaccessible one.
		want := map[string]domain.GCDecisionCode{"old": domain.GCArchive, "new": domain.GCProtected, "eph": domain.GCArchive, "live": domain.GCIneligible,
			"leased": domain.GCProtected, "sys": domain.GCIneligible, "now": domain.GCProtected}
		got := map[string]domain.GCDecisionCode{}
		for _, d := range r.Decisions {
			got[d.Target.ItemID] = d.Code
		}
		if len(got) != len(want) {
			t.Fatalf("decisions %+v: every readable candidate must be decided, want %+v", r.Decisions, want)
		}
		for item, code := range want {
			if got[item] != code {
				t.Errorf("%s decided %q, want %q", item, got[item], code)
			}
		}
		readSemantic(t, db, func(sem store.SemanticReader) error {
			stored, err := sem.CollectReceipt(r.ID)
			if err != nil || stored.RequestID != "c39b" {
				t.Fatalf("stored receipt: %+v %v", stored, err)
			}
			for _, d := range stored.Decisions {
				if d.Target.ItemID == "private" {
					t.Fatalf("inaccessible candidate exposed in the stored receipt: %+v", d)
				}
			}
			return nil
		})
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			it, _ := tx.Item("private")
			if it.Residency != domain.ResidencyResident || it.Version != 1 {
				t.Fatalf("inaccessible candidate touched: %+v", it)
			}
			events, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetItem, TargetID: "private"})
			if len(events) != 0 {
				t.Fatalf("inaccessible candidate has audit events: %+v", events)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
}
