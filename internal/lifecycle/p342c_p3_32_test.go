package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// P3-32 (ADR 8 line 1410): an unknown legacy association fails closed for
// automatic selection while retaining archive access. The cited
// TestRegisteredBroadOwnersOutliveTheirTask(register=false) asserts only the
// GC INELIGIBLE decision; this test adds the two missing halves.
//
//  1. Never selected: the same automatic collection that ARCHIVES an ordinary
//     ended-task item (the control) leaves both unknown-owner items RESIDENT
//     with INELIGIBLE decisions, and policy.Eligibility — the automatic
//     new-selection rule — refuses them (NewSelection=false,
//     UNKNOWN_OWNER) even for an in-boundary principal.
//  2. Keeps archive access: an in-boundary lifecycle principal can still
//     explicitly archive and unarchive the unknown-owner items — the unknown
//     owner never strips Access — while an out-of-boundary principal gets
//     ErrNotFound on both, and archival leaves the stored Access boundary
//     and the OPEN goal status byte-identical.
func TestP3_32_UnknownLegacyOwnerNeverSelectedAndKeepsArchiveAccess(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		seedBroadOwners(t, db, false)
		// The control: an ordinary TASK-scoped fact of the same task, whose
		// lifetime ends with it.
		if err := db.Update(ctx, "s", func(tx store.Tx) error {
			note := storetest.NewItem("s", "task-note", tx.NextSeq(), "ordinary task fact")
			note.Scope = domain.ScopeTask
			note.Access = storetest.DirectiveBoundary("s")
			return tx.InsertItem(note)
		}); err != nil {
			t.Fatal(err)
		}
		s, _ := New(db, testPolicy())
		if _, err := s.CompleteTaskStandalone(ctx, storetest.NewPrincipal("s", domain.AuthorityUser), domain.CompleteTaskIntent{RequestID: "r", TaskID: "task"}); err != nil {
			t.Fatal(err)
		}

		// 1a. Never selected: the decisions.
		got := collectDecisions(t, db, "c1")
		for _, id := range []string{"wf-goal", "agent-pin"} {
			if got[id] != domain.GCIneligible {
				t.Fatalf("%s: decision %s, want GCIneligible (unknown owner never justifies selection)", id, got[id])
			}
		}
		if got["task-note"] != domain.GCArchive {
			t.Fatalf("task-note: decision %s, want GCArchive (control: the collection does archive)", got["task-note"])
		}
		// 1b. Never selected: the effects. The control is archived; the
		// unknown-owner items stay RESIDENT.
		var last uint64
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			last = tx.LastSeq()
			for _, id := range []string{"wf-goal", "agent-pin"} {
				it, err := tx.Item(id)
				if err != nil {
					return err
				}
				if it.Residency != domain.ResidencyResident {
					t.Fatalf("%s: residency %s after collection, want RESIDENT", id, it.Residency)
				}
			}
			note, err := tx.Item("task-note")
			if err != nil {
				return err
			}
			if note.Residency != domain.ResidencyArchived {
				t.Fatalf("task-note: residency %s after collection, want ARCHIVED (control)", note.Residency)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// 1c. Never selected: the automatic new-selection rule itself. An
		// in-boundary principal with a snapshot that carries no owner
		// registration gets UNKNOWN_OWNER, never a new selection.
		p := storetest.NewPrincipal("s", domain.AuthorityUser)
		for _, id := range []string{"wf-goal", "agent-pin"} {
			var it domain.ContextItem
			if err := db.View(ctx, "s", func(tx store.ReadTx) error {
				var err error
				it, err = tx.Item(id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			r := policy.Eligibility(it, policy.EligibilitySnapshot{
				OwnerSnapshot:  policy.OwnerSnapshot{Seq: last},
				Item:           domain.ItemRevisionRef{ItemID: it.ID, Version: it.Version},
				Currentness:    domain.ItemUnkeyed,
				Representation: domain.ExpiryLive,
			}, p, "turn-1")
			if r.NewSelection || r.OrdinaryTemporal || r.TemporalReason != policy.ReasonUnknownOwner || r.SelectionReason != policy.ReasonUnknownOwner {
				t.Fatalf("%s: eligibility = %+v, want NewSelection=false with UNKNOWN_OWNER", id, r)
			}
		}

		// 2a. Keeps archive access: an in-boundary principal archives the
		// unknown-owner items explicitly; the OPEN goal's removal is
		// disclosed, never silently refused.
		version := func(id string) uint64 {
			t.Helper()
			var v uint64
			if err := db.View(ctx, "s", func(tx store.ReadTx) error {
				it, err := tx.Item(id)
				if err != nil {
					return err
				}
				v = it.Version
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			return v
		}
		res, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "a1", ItemID: "wf-goal", ExpectedVersion: version("wf-goal")})
		if err != nil {
			t.Fatalf("in-boundary archive of the unknown-owner goal: %v", err)
		}
		if res.After.Residency != domain.ResidencyArchived || !res.ExplicitProtectedRemoval {
			t.Fatalf("archive result = %+v, want ARCHIVED with the protected removal disclosed", res.After)
		}
		if _, err := s.ArchiveStandalone(ctx, p, domain.ArchiveIntent{RequestID: "a2", ItemID: "agent-pin", ExpectedVersion: version("agent-pin")}); err != nil {
			t.Fatalf("in-boundary archive of the unknown-owner pin: %v", err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			goal, err := tx.Item("wf-goal")
			if err != nil {
				return err
			}
			pin, err := tx.Item("agent-pin")
			if err != nil {
				return err
			}
			if goal.Residency != domain.ResidencyArchived || pin.Residency != domain.ResidencyArchived {
				t.Fatalf("residency after explicit archive: goal %s, pin %s", goal.Residency, pin.Residency)
			}
			// Archival changed residency only: the boundary and the goal
			// status the owners' access depends on are untouched.
			if goal.Access != (domain.AccessBoundary{Scope: domain.ScopeWorkflow, SessionID: "s", WorkflowID: "wf"}) ||
				pin.Access != (domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: "s", WorkflowID: "wf", AgentID: "agent"}) {
				t.Fatalf("archive rewrote the access boundary: goal %+v, pin %+v", goal.Access, pin.Access)
			}
			if goal.GoalStatus == nil || *goal.GoalStatus != domain.GoalOpen {
				t.Fatalf("archive changed the goal status: %+v", goal.GoalStatus)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// 2b. Keeps archive access — and keeps it exclusive: the boundary
		// still governs reads from the archive (FR-DOM-003). An
		// out-of-boundary lifecycle principal learns nothing (ErrNotFound)
		// on both the archived goal and the archived pin, while the
		// in-boundary principal can still unarchive what it archived.
		stranger := storetest.NewPrincipal("s", domain.AuthorityUser)
		stranger.WorkflowID = "elsewhere"
		if _, err := s.UnarchiveStandalone(ctx, stranger, domain.UnarchiveIntent{RequestID: "u0", ItemID: "wf-goal", ExpectedVersion: version("wf-goal")}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("out-of-boundary unarchive of the archived goal: %v, want ErrNotFound", err)
		}
		if _, err := s.ArchiveStandalone(ctx, stranger, domain.ArchiveIntent{RequestID: "a3", ItemID: "agent-pin", ExpectedVersion: version("agent-pin")}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("out-of-boundary archive of the archived pin: %v, want ErrNotFound", err)
		}
		if _, err := s.UnarchiveStandalone(ctx, p, domain.UnarchiveIntent{RequestID: "u1", ItemID: "wf-goal", ExpectedVersion: version("wf-goal")}); err != nil {
			t.Fatalf("in-boundary unarchive of the archived goal: %v", err)
		}
		if err := db.View(ctx, "s", func(tx store.ReadTx) error {
			goal, err := tx.Item("wf-goal")
			if err != nil {
				return err
			}
			if goal.Residency != domain.ResidencyResident {
				t.Fatalf("goal residency %s after unarchive, want RESIDENT", goal.Residency)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
