package graph

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

var ctx = context.Background()

// -- test helpers --------------------------------------------------------
//
// storetest's builders fix scope, task, and authority; these local variants
// let a test pick the dimension it's exercising while keeping every other
// field structurally valid.

func principal(sess string, a domain.Authority) domain.Principal {
	return storetest.NewPrincipal(sess, a)
}

func principalWithAgent(sess string, a domain.Authority, agentID string) domain.Principal {
	p := storetest.NewPrincipal(sess, a)
	p.AgentID = agentID
	return p
}

// taskItem returns a TASK-scoped item (task "task", set by storetest.NewItem)
// with the given authority.
func taskItem(sess, id string, seq uint64, authority domain.Authority) domain.ContextItem {
	it := storetest.NewItem(sess, id, seq, "text-"+id)
	it.Authority = authority
	it.Scope = domain.ScopeTask
	it.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: it.TaskID}
	return it
}

// taskItemIn is taskItem for an explicit task, so two items can disagree on
// task while everything else stays valid.
func taskItemIn(sess, id string, seq uint64, authority domain.Authority, taskID string) domain.ContextItem {
	it := taskItem(sess, id, seq, authority)
	it.TaskID = taskID
	it.Access.TaskID = taskID
	return it
}

// workflowItem returns a WORKFLOW-scoped item (workflow "wf").
func workflowItem(sess, id string, seq uint64, authority domain.Authority) domain.ContextItem {
	it := storetest.NewItem(sess, id, seq, "wf-"+id)
	it.Authority = authority
	it.Scope = domain.ScopeWorkflow
	it.Access = domain.AccessBoundary{Scope: domain.ScopeWorkflow, SessionID: sess, WorkflowID: it.WorkflowID}
	return it
}

// agentScopedItem returns an AGENT-scoped item private to agentID.
func agentScopedItem(sess, id string, seq uint64, agentID string) domain.ContextItem {
	it := storetest.NewItem(sess, id, seq, "evidence-"+id)
	it.Kind = domain.KindEvidence
	it.AgentID = agentID
	it.Scope = domain.ScopeAgent
	it.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sess, AgentID: agentID}
	return it
}

// agentDirective returns an AGENT-authority, task-scoped item carrying
// dirID, as context_update_state/context_remember write them (FR-TOOL-002).
func agentDirective(sess, id, dirID string, seq uint64) domain.ContextItem {
	it := storetest.NewItem(sess, id, seq, "state-"+id)
	it.Kind = domain.KindTaskState
	it.DirectiveID = dirID
	it.Authority = domain.AuthorityAgent
	it.Scope = domain.ScopeTask
	it.Access = domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: it.TaskID}
	return it
}

// workingItem returns a TASK-scoped task_state item, as a Working section's
// items are (FR-DIR-007).
func workingItem(sess, id string, seq uint64, authority domain.Authority) domain.ContextItem {
	it := taskItem(sess, id, seq, authority)
	it.Kind = domain.KindTaskState
	it.Section = domain.SectionWorking
	it.DirectiveID = "wd-" + id // Section != SectionNone requires a directive ID
	return it
}

// workingConversationItem is a Working item of kind=conversation: FR-DIR-003
// permits a Working section item of any kind, not only task_state
// (SPEC-1.1).
func workingConversationItem(sess, id string, seq uint64, authority domain.Authority) domain.ContextItem {
	it := workingItem(sess, id, seq, authority)
	it.Kind = domain.KindConversation
	return it
}

// independentTaskStateItem is a plain task_state item that was NOT written
// as part of a Working section (Section stays SectionNone): SupersedeSnapshot
// must never touch it, even if it happens to share a Working item's
// authority and access boundary (SPEC-1.1).
func independentTaskStateItem(sess, id string, seq uint64, authority domain.Authority) domain.ContextItem {
	it := taskItem(sess, id, seq, authority)
	it.Kind = domain.KindTaskState
	return it
}

// agentScopedWorkingItem is workingItem narrowed to one agent's own AGENT
// scope, as a keyed agent write is (FR-TOOL-002).
func agentScopedWorkingItem(sess, id string, seq uint64, authority domain.Authority, agentID string) domain.ContextItem {
	it := agentScopedItem(sess, id, seq, agentID)
	it.Kind = domain.KindTaskState
	it.Authority = authority
	it.Section = domain.SectionWorking
	it.DirectiveID = "wd-" + id
	return it
}

func mustInsert(t *testing.T, tx store.Tx, items ...domain.ContextItem) {
	t.Helper()
	for _, it := range items {
		if err := tx.InsertItem(it); err != nil {
			t.Fatalf("InsertItem(%s): %v", it.ID, err)
		}
	}
}

// -- T02: directive replacement ------------------------------------------

func TestReplaceDirective_T02(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess, taskID, dirID = "sess-t02", "task", "dep-version"
	actor := principal(sess, domain.AuthorityUser)

	var p1ID, p2ID string
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		p1 := storetest.NewDirective(sess, "p1", dirID, tx.NextSeq(), "Use dependency v2.")
		p1ID = p1.ID
		mustInsert(t, tx, p1)
		prev, err := ReplaceDirective(tx, actor, taskID, dirID, p1.ID, "evt-p1")
		if err != nil {
			return err
		}
		if prev != "" {
			t.Fatalf("previous = %q, want empty for a first version", prev)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("first version: %v", err)
	}

	err = s.Update(ctx, sess, func(tx store.Tx) error {
		p2 := storetest.NewDirective(sess, "p2", dirID, tx.NextSeq(), "Use dependency v3.")
		p2ID = p2.ID
		mustInsert(t, tx, p2)
		prev, err := ReplaceDirective(tx, actor, taskID, dirID, p2.ID, "evt-p2")
		if err != nil {
			return err
		}
		if prev != p1ID {
			t.Fatalf("previous = %q, want %q", prev, p1ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("second version: %v", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		boundary := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: taskID}
		cur, err := tx.CurrentDirective(taskID, dirID, boundary)
		if err != nil {
			return err
		}
		if cur != p2ID {
			t.Errorf("CurrentDirective = %q, want %q", cur, p2ID)
		}
		if ok, err := IsCurrent(tx, p1ID); err != nil || ok {
			t.Errorf("IsCurrent(p1) = %v, %v; want false, nil", ok, err)
		}
		if ok, err := IsCurrent(tx, p2ID); err != nil || !ok {
			t.Errorf("IsCurrent(p2) = %v, %v; want true, nil", ok, err)
		}
		// P1 retains its content and is still readable, just not current.
		if got, err := tx.Item(p1ID); err != nil || got.ID != p1ID {
			t.Errorf("Item(p1) = %v, %v; p1 must remain retained", got, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestReplaceDirective_MismatchedNewItem covers ErrDirectiveMismatch
// (TEST-1.1): a new item whose TaskID or DirectiveID disagrees with the
// (taskID, directiveID) it's being filed under is rejected, whether or not
// a current version already exists.
func TestReplaceDirective_MismatchedNewItem(t *testing.T) {
	const sess, taskID, dirID = "sess-mismatch", "task", "dep-version"
	actor := principal(sess, domain.AuthorityUser)

	t.Run("WrongDirectiveID", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			it := storetest.NewDirective(sess, "wrong-dir", "some-other-directive", tx.NextSeq(), "text")
			mustInsert(t, tx, it)
			_, err := ReplaceDirective(tx, actor, taskID, dirID, it.ID, "evt")
			return err
		})
		if !errors.Is(err, ErrDirectiveMismatch) {
			t.Fatalf("err = %v, want ErrDirectiveMismatch", err)
		}
	})

	t.Run("WrongTask", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			// SESSION-scoped (no task boundary constraint) so the actor can
			// still see it; only its ambient TaskID disagrees with the task
			// this directive is being filed under, so the call reaches the
			// identity guard rather than failing on access first.
			it := storetest.NewItem(sess, "wrong-task", tx.NextSeq(), "text")
			it.DirectiveID = dirID
			it.TaskID = "some-other-task"
			mustInsert(t, tx, it)
			_, err := ReplaceDirective(tx, actor, taskID, dirID, it.ID, "evt")
			return err
		})
		if !errors.Is(err, ErrDirectiveMismatch) {
			t.Fatalf("err = %v, want ErrDirectiveMismatch", err)
		}
	})
}

// TestReplaceDirective_InaccessibleNewItem covers the actor-cannot-see-its-
// own-new-item branch (TEST-1.1): access is checked before the identity
// guard, so an inaccessible new item fails with ErrNotFound.
func TestReplaceDirective_InaccessibleNewItem(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess, taskID, dirID = "sess-inaccessible-new", "task", "dep-version"

	err := s.Update(ctx, sess, func(tx store.Tx) error {
		it := agentScopedItem(sess, "owned-by-agent-b", tx.NextSeq(), "agent-b")
		it.DirectiveID = dirID
		mustInsert(t, tx, it)
		actor := principalWithAgent(sess, domain.AuthorityUser, "agent-a")
		_, err := ReplaceDirective(tx, actor, taskID, dirID, it.ID, "evt")
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestSupersedeSnapshot_TaskMismatch covers ErrSnapshotTaskMismatch
// (TEST-1.1): a new item from a different task than the snapshot's taskID
// is rejected, and nothing is written.
func TestSupersedeSnapshot_TaskMismatch(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess, taskID = "sess-snapshot-mismatch", "task"
	actor := principal(sess, domain.AuthorityUser)

	err := s.Update(ctx, sess, func(tx store.Tx) error {
		// SESSION-scoped (no task boundary constraint) so the actor can
		// still see it; only its ambient TaskID disagrees with the
		// snapshot's taskID, so the call reaches the task guard rather than
		// failing on access first.
		wrongTask := storetest.NewItem(sess, "wrong-task-item", tx.NextSeq(), "text")
		wrongTask.Kind = domain.KindTaskState
		wrongTask.Section = domain.SectionWorking
		wrongTask.DirectiveID = "wd-wrong-task-item"
		wrongTask.TaskID = "some-other-task"
		mustInsert(t, tx, wrongTask)
		_, err := SupersedeSnapshot(tx, actor, []string{wrongTask.ID}, taskID, "evt")
		return err
	})
	if !errors.Is(err, ErrSnapshotTaskMismatch) {
		t.Fatalf("err = %v, want ErrSnapshotTaskMismatch", err)
	}
}

// TestSupersedeSnapshot_NewItemNotWorking is SPEC-2.1/AUTH-2.2: a new
// "snapshot" item that was not itself written as part of a Working section
// (Section != WORKING, e.g. a PINNED directive) must never be allowed to
// retire a current Working item, and nothing may be written before that
// check runs.
func TestSupersedeSnapshot_NewItemNotWorking(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess, taskID = "sess-spec21", "task"
	actor := principal(sess, domain.AuthorityUser)

	var oldWorking, notWorking domain.ContextItem
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		oldWorking = workingItem(sess, "old-working", tx.NextSeq(), domain.AuthorityUser)
		notWorking = storetest.NewDirective(sess, "pinned-not-working", "some-pin", tx.NextSeq(), "text")
		mustInsert(t, tx, oldWorking, notWorking)
		return nil
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	err = s.Update(ctx, sess, func(tx store.Tx) error {
		_, err := SupersedeSnapshot(tx, actor, []string{notWorking.ID}, taskID, "evt")
		return err
	})
	if !errors.Is(err, ErrSnapshotNotWorking) {
		t.Fatalf("err = %v, want ErrSnapshotNotWorking", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		if ok, err := IsCurrent(tx, oldWorking.ID); err != nil || !ok {
			t.Errorf("IsCurrent(old-working) = %v, %v; want true, nil (must not have been superseded)", ok, err)
		}
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: notWorking.ID})
		if err != nil {
			return err
		}
		if len(rels) != 0 {
			t.Errorf("relationships from %s = %d, want 0", notWorking.ID, len(rels))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestSupersede_MissingAndInaccessibleErrorsAreIndistinguishable is AUTH-1.3:
// a genuinely missing item and one that exists but is inaccessible must
// fail with the identical bare domain.ErrNotFound, not merely
// errors.Is-equivalent errors that a caller could otherwise tell apart by
// their text (a store's "item <id>: not found" would reveal the ID exists
// when only the other case's item is truly missing).
func TestSupersede_MissingAndInaccessibleErrorsAreIndistinguishable(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess = "sess-auth13"
	actor := principalWithAgent(sess, domain.AuthorityUser, "agent-a")

	var newID, hiddenID string
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		newItem := taskItem(sess, "new", tx.NextSeq(), domain.AuthorityUser)
		hidden := agentScopedItem(sess, "hidden", tx.NextSeq(), "agent-b")
		newID, hiddenID = newItem.ID, hidden.ID
		mustInsert(t, tx, newItem, hidden)
		return nil
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	var missingErr, inaccessibleErr error
	err = s.Update(ctx, sess, func(tx store.Tx) error {
		_, missingErr = Supersede(tx, actor, newID, "does-not-exist-at-all", "evt", "")
		_, inaccessibleErr = Supersede(tx, actor, newID, hiddenID, "evt", "")
		return nil
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !errors.Is(missingErr, domain.ErrNotFound) {
		t.Fatalf("missing item error = %v, want ErrNotFound", missingErr)
	}
	if !errors.Is(inaccessibleErr, domain.ErrNotFound) {
		t.Fatalf("inaccessible item error = %v, want ErrNotFound", inaccessibleErr)
	}
	if missingErr.Error() != inaccessibleErr.Error() {
		t.Errorf("error text differs: missing=%q inaccessible=%q; a caller could tell the cases apart",
			missingErr.Error(), inaccessibleErr.Error())
	}
	if missingErr.Error() != domain.ErrNotFound.Error() {
		t.Errorf("error text = %q, want the bare sentinel %q (must not name the ID)", missingErr.Error(), domain.ErrNotFound.Error())
	}
}

// TestReplaceDirective_FirstVersionAuthorization is AUTH-1.5:
// domain.AuthorizeSupersession never runs for a directive's first version
// (there is nothing to compare against), so ReplaceDirective must apply the
// same actor rules itself before letting an untrusted actor set the
// current-directive pointer.
func TestReplaceDirective_FirstVersionAuthorization(t *testing.T) {
	const sess, taskID, dirID = "sess-auth15", "task", "dep-version"

	t.Run("ToolActorRejected", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			// No Section: Validate now requires SYSTEM/HARNESS/USER authority
			// for a directive section (AUTH-2.2), which would mask the actor
			// rule this test targets. A directive ID alone doesn't need one.
			it := taskItem(sess, "d1", tx.NextSeq(), domain.AuthorityTool)
			it.DirectiveID = dirID
			mustInsert(t, tx, it)
			_, err := ReplaceDirective(tx, principal(sess, domain.AuthorityTool), taskID, dirID, it.ID, "evt")
			return err
		})
		if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("err = %v, want ErrInvalidAuthorityPromotion", err)
		}
	})

	t.Run("AgentRejectedForNonKeyedItem", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			// AGENT authority but not a keyed "agent.<key>" directive ID; no
			// Section, for the same reason as ToolActorRejected above.
			it := taskItem(sess, "d2", tx.NextSeq(), domain.AuthorityAgent)
			it.DirectiveID = dirID
			mustInsert(t, tx, it)
			_, err := ReplaceDirective(tx, principal(sess, domain.AuthorityAgent), taskID, dirID, it.ID, "evt")
			return err
		})
		if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("err = %v, want ErrInvalidAuthorityPromotion", err)
		}
	})

	t.Run("AgentAllowedForItsOwnKeyedItem", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		keyedDirID := domain.AgentKeyID("status")
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			it := agentDirective(sess, "d3", keyedDirID, tx.NextSeq())
			mustInsert(t, tx, it)
			prev, err := ReplaceDirective(tx, principal(sess, domain.AuthorityAgent), taskID, keyedDirID, it.ID, "evt")
			if err != nil {
				return err
			}
			if prev != "" {
				t.Errorf("prev = %q, want empty", prev)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("UserActorInsufficientAuthorityRejected", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			it := storetest.NewDirective(sess, "d4", dirID, tx.NextSeq(), "text")
			it.Authority = domain.AuthoritySystem // outranks the USER actor below
			mustInsert(t, tx, it)
			_, err := ReplaceDirective(tx, principal(sess, domain.AuthorityUser), taskID, dirID, it.ID, "evt")
			return err
		})
		if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("err = %v, want ErrInvalidAuthorityPromotion", err)
		}
	})
}

// TestReplaceDirective_VisibleBoundaryConflict is AUTH-2.1: reusing a
// directive ID at a boundary the actor can already see, while a different
// current version at another boundary is also visible, is a scope change
// and must be rejected (previously it silently forked a second current
// version).
func TestReplaceDirective_VisibleBoundaryConflict(t *testing.T) {
	t.Run("VisibleOtherBoundaryRejected", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		const sess, taskID, dirID = "sess-auth21-visible", "task", "policy"

		err := s.Update(ctx, sess, func(tx store.Tx) error {
			h := taskItem(sess, "h", tx.NextSeq(), domain.AuthorityHarness)
			h.DirectiveID = dirID
			mustInsert(t, tx, h)
			_, err := ReplaceDirective(tx, principal(sess, domain.AuthorityHarness), taskID, dirID, h.ID, "evt-h")
			return err
		})
		if err != nil {
			t.Fatalf("filing h: %v", err)
		}

		err = s.Update(ctx, sess, func(tx store.Tx) error {
			// Same session and task, but TURN-scoped instead of TASK-scoped:
			// a different boundary, visible to the same USER principal.
			u := taskItem(sess, "u", tx.NextSeq(), domain.AuthorityUser)
			u.DirectiveID = dirID
			u.Scope = domain.ScopeTurn
			u.Access = domain.AccessBoundary{Scope: domain.ScopeTurn, SessionID: sess, TaskID: u.TaskID}
			mustInsert(t, tx, u)
			_, err := ReplaceDirective(tx, principal(sess, domain.AuthorityUser), taskID, dirID, u.ID, "evt-u")
			return err
		})
		if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("err = %v, want ErrInvalidAuthorityPromotion", err)
		}

		// h must remain the only current version; the rejected write left
		// nothing behind.
		err = s.View(ctx, sess, func(tx store.ReadTx) error {
			versions, err := tx.CurrentDirectives(taskID, dirID)
			if err != nil {
				return err
			}
			if len(versions) != 1 || versions[0] != "h" {
				t.Errorf("CurrentDirectives(%s) = %v, want [h]", dirID, versions)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("view: %v", err)
		}
	})

	t.Run("HiddenOtherBoundaryStaysIndependent", func(t *testing.T) {
		// The round-1 property AUTH-2.1 must not regress: a version at a
		// boundary the actor cannot see never blocks (or reveals) a first
		// version at the actor's own, different boundary.
		s := memory.New()
		defer s.Close()
		const sess, taskID, dirID = "sess-auth21-hidden", "task", "shared-id"

		err := s.Update(ctx, sess, func(tx store.Tx) error {
			hidden := agentScopedItem(sess, "hidden-v", tx.NextSeq(), "agent-b")
			hidden.DirectiveID = dirID
			mustInsert(t, tx, hidden)
			_, err := ReplaceDirective(tx, principalWithAgent(sess, domain.AuthorityUser, "agent-b"), taskID, dirID, hidden.ID, "evt-hidden")
			return err
		})
		if err != nil {
			t.Fatalf("filing hidden version: %v", err)
		}

		err = s.Update(ctx, sess, func(tx store.Tx) error {
			visible := agentScopedItem(sess, "visible-v", tx.NextSeq(), "agent-a")
			visible.DirectiveID = dirID
			mustInsert(t, tx, visible)
			prev, err := ReplaceDirective(tx, principalWithAgent(sess, domain.AuthorityUser, "agent-a"), taskID, dirID, visible.ID, "evt-visible")
			if err != nil {
				return err
			}
			if prev != "" {
				t.Errorf("prev = %q, want empty (agent-a's boundary is a first version)", prev)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("filing visible version: %v", err)
		}
	})
}

// -- authorization rules (FR-REL-006) -------------------------------------

func TestSupersede_AuthorizationRules(t *testing.T) {
	t.Run("AgentCannotSupersedeUser", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		const sess = "sess-authz-1"
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			oldItem := taskItem(sess, "old", tx.NextSeq(), domain.AuthorityUser)
			newItem := taskItem(sess, "new", tx.NextSeq(), domain.AuthorityAgent)
			mustInsert(t, tx, oldItem, newItem)
			_, err := Supersede(tx, principal(sess, domain.AuthorityAgent), newItem.ID, oldItem.ID, "evt", "")
			return err
		})
		if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("err = %v, want ErrInvalidAuthorityPromotion", err)
		}
	})

	t.Run("AgentSupersedesAgentAllowed", func(t *testing.T) {
		// An AGENT actor may supersede only a keyed agent write with the
		// same key (FR-TOOL-002): both items AGENT authority, both carrying
		// the identical "agent.<key>" directive ID.
		s := memory.New()
		defer s.Close()
		const sess = "sess-authz-1b"
		dirID := domain.AgentKeyID("status")
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			oldItem := agentDirective(sess, "old", dirID, tx.NextSeq())
			newItem := agentDirective(sess, "new", dirID, tx.NextSeq())
			mustInsert(t, tx, oldItem, newItem)
			_, err := Supersede(tx, principal(sess, domain.AuthorityAgent), newItem.ID, oldItem.ID, "evt", "")
			return err
		})
		if err != nil {
			t.Fatalf("AGENT superseding AGENT with the same key: unexpected error %v", err)
		}
	})

	t.Run("AgentCannotSupersedeDifferentKeyAgentItem", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		const sess = "sess-authz-1c"
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			oldItem := agentDirective(sess, "old", domain.AgentKeyID("status"), tx.NextSeq())
			newItem := agentDirective(sess, "new", domain.AgentKeyID("other-key"), tx.NextSeq())
			mustInsert(t, tx, oldItem, newItem)
			_, err := Supersede(tx, principal(sess, domain.AuthorityAgent), newItem.ID, oldItem.ID, "evt", "")
			return err
		})
		if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("err = %v, want ErrInvalidAuthorityPromotion", err)
		}
	})

	t.Run("AgentActorInaccessibleItemIsNotFoundNotPromotionError", func(t *testing.T) {
		// Access is checked before the authority/key rule (contract v3): an
		// AGENT actor superseding a USER item it cannot even see must get
		// ErrNotFound, never ErrInvalidAuthorityPromotion, so an
		// inaccessible endpoint never reveals its authority.
		s := memory.New()
		defer s.Close()
		const sess = "sess-authz-1d"
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			hidden := agentScopedItem(sess, "hidden-user-item", tx.NextSeq(), "agent-b")
			hidden.Authority = domain.AuthorityUser
			newItem := agentDirective(sess, "new", domain.AgentKeyID("status"), tx.NextSeq())
			mustInsert(t, tx, hidden, newItem)
			_, err := Supersede(tx, principalWithAgent(sess, domain.AuthorityAgent, "agent-a"), newItem.ID, hidden.ID, "evt", "")
			return err
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("ToolAndRetrievedContentActorsAlwaysRejected", func(t *testing.T) {
		// Tool output can never suppress state (section 9): a TOOL or
		// RETRIEVED_CONTENT actor is rejected outright, even when both
		// items share its own authority (which the old equal-rank check
		// alone would not have caught).
		for _, dir := range []struct {
			name   string
			actorA domain.Authority
		}{
			{"ToolActor", domain.AuthorityTool},
			{"RetrievedContentActor", domain.AuthorityRetrievedContent},
		} {
			t.Run(dir.name, func(t *testing.T) {
				s := memory.New()
				defer s.Close()
				sess := "sess-authz-2-" + dir.name
				err := s.Update(ctx, sess, func(tx store.Tx) error {
					oldItem := taskItem(sess, "old", tx.NextSeq(), dir.actorA)
					newItem := taskItem(sess, "new", tx.NextSeq(), dir.actorA)
					mustInsert(t, tx, oldItem, newItem)
					_, err := Supersede(tx, principal(sess, dir.actorA), newItem.ID, oldItem.ID, "evt", "")
					return err
				})
				if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
					t.Fatalf("err = %v, want ErrInvalidAuthorityPromotion", err)
				}
			})
		}
	})

	t.Run("DifferentBoundariesRejected", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		const sess = "sess-authz-3"
		// A principal fixed at workflow "wf" and task "task" can access
		// both a WORKFLOW-scoped and a TASK-scoped item, but the two
		// boundaries are not equal, so FR-REL-006 must reject the edge
		// before Permits alone would (a mismatched task or workflow would
		// hide the item entirely and surface as ErrNotFound instead).
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			wfScoped := workflowItem(sess, "wf-item", tx.NextSeq(), domain.AuthorityUser)
			taskScoped := taskItem(sess, "task-item", tx.NextSeq(), domain.AuthorityUser)
			mustInsert(t, tx, wfScoped, taskScoped)
			_, err := Supersede(tx, principal(sess, domain.AuthorityUser), taskScoped.ID, wfScoped.ID, "evt", "")
			return err
		})
		if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("err = %v, want ErrInvalidAuthorityPromotion", err)
		}
	})

	t.Run("InaccessibleEndpointIsNotFound", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		const sess = "sess-authz-4"
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			hidden := agentScopedItem(sess, "hidden", tx.NextSeq(), "agent-b")
			newItem := taskItem(sess, "new", tx.NextSeq(), domain.AuthorityUser)
			mustInsert(t, tx, hidden, newItem)
			_, err := Supersede(tx, principalWithAgent(sess, domain.AuthorityUser, "agent-a"), newItem.ID, hidden.ID, "evt", "")
			return err
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// -- cycle rejection (FR-REL-004, INV-06) ---------------------------------

func TestSupersede_CycleRejected(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess = "sess-cycle"
	actor := principal(sess, domain.AuthorityUser)

	err := s.Update(ctx, sess, func(tx store.Tx) error {
		i1 := taskItem(sess, "c1", tx.NextSeq(), domain.AuthorityUser)
		i2 := taskItem(sess, "c2", tx.NextSeq(), domain.AuthorityUser)
		i3 := taskItem(sess, "c3", tx.NextSeq(), domain.AuthorityUser)
		mustInsert(t, tx, i1, i2, i3)

		if _, err := Supersede(tx, actor, i2.ID, i1.ID, "e1", ""); err != nil {
			return err
		}
		if _, err := Supersede(tx, actor, i3.ID, i2.ID, "e2", ""); err != nil {
			return err
		}
		// i1 -> i3 would close the cycle i1 -> i3 -> i2 -> i1.
		_, err := Supersede(tx, actor, i1.ID, i3.ID, "e3", "")
		return err
	})
	if !errors.Is(err, domain.ErrSupersessionCycle) {
		t.Fatalf("err = %v, want ErrSupersessionCycle", err)
	}
}

// -- T17: keyed agent write chain ------------------------------------------

func TestReplaceDirective_KeyedAgentWriteChain_T17(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess, taskID = "sess-t17", "task"
	dirID := domain.AgentKeyID("status")
	actor := principal(sess, domain.AuthorityAgent)

	var v1, v2 string
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		it := agentDirective(sess, "status-v1", dirID, tx.NextSeq())
		v1 = it.ID
		mustInsert(t, tx, it)
		prev, err := ReplaceDirective(tx, actor, taskID, dirID, it.ID, "evt1")
		if err != nil {
			return err
		}
		if prev != "" {
			t.Fatalf("prev = %q, want empty", prev)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("v1: %v", err)
	}

	err = s.Update(ctx, sess, func(tx store.Tx) error {
		it := agentDirective(sess, "status-v2", dirID, tx.NextSeq())
		v2 = it.ID
		mustInsert(t, tx, it)
		prev, err := ReplaceDirective(tx, actor, taskID, dirID, it.ID, "evt2")
		if err != nil {
			return err
		}
		if prev != v1 {
			t.Fatalf("prev = %q, want %q", prev, v1)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("v2: %v", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		boundary := domain.AccessBoundary{Scope: domain.ScopeTask, SessionID: sess, TaskID: taskID}
		cur, err := tx.CurrentDirective(taskID, dirID, boundary)
		if err != nil || cur != v2 {
			t.Errorf("CurrentDirective = %q, %v; want %q, nil", cur, err, v2)
		}
		if ok, err := IsCurrent(tx, v1); err != nil || ok {
			t.Errorf("IsCurrent(v1) = %v, %v; want false, nil", ok, err)
		}
		if ok, err := IsCurrent(tx, v2); err != nil || !ok {
			t.Errorf("IsCurrent(v2) = %v, %v; want true, nil", ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

// -- FR-DIR-007 v0.6: Working snapshot supersession ------------------------

// TestSupersedeSnapshot_FRDIR007 ingests a Working section W2 (one new
// task_state item) into a task that already holds two current Working
// items: w1a, which shares W2's authority and TASK-scoped access boundary
// and must be superseded, and an AGENT-scoped item belonging to a different
// agent, which must be left alone even though it lives in the same task
// (FR-DIR-007 v0.6: same authority AND same access boundary, not mere task
// membership).
func TestSupersedeSnapshot_FRDIR007(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess, taskID = "sess-dir007", "task"
	actor := principal(sess, domain.AuthorityUser)

	var w1a, otherAgentItem, w2 domain.ContextItem
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		w1a = workingItem(sess, "w1a", tx.NextSeq(), domain.AuthorityUser)
		otherAgentItem = agentScopedWorkingItem(sess, "agent-restricted", tx.NextSeq(), domain.AuthorityUser, "agent-b")
		w2 = workingItem(sess, "w2", tx.NextSeq(), domain.AuthorityUser)
		mustInsert(t, tx, w1a, otherAgentItem, w2)

		rels, err := SupersedeSnapshot(tx, actor, []string{w2.ID}, taskID, "evt-w2")
		if err != nil {
			return err
		}
		if len(rels) != 1 || rels[0].ToID != w1a.ID {
			t.Errorf("SupersedeSnapshot relationships = %+v, want exactly one edge to %s", rels, w1a.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot supersession: %v", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		if ok, err := IsCurrent(tx, w1a.ID); err != nil || ok {
			t.Errorf("IsCurrent(w1a) = %v, %v; want false, nil", ok, err)
		}
		if ok, err := IsCurrent(tx, w2.ID); err != nil || !ok {
			t.Errorf("IsCurrent(w2) = %v, %v; want true, nil", ok, err)
		}
		// The agent-restricted item has a narrower access boundary than W2
		// (a different agent), so it must remain current and untouched.
		if ok, err := IsCurrent(tx, otherAgentItem.ID); err != nil || !ok {
			t.Errorf("IsCurrent(agent-restricted) = %v, %v; want true, nil (must not be superseded)", ok, err)
		}
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: w2.ID})
		if err != nil {
			return err
		}
		if len(rels) != 1 {
			t.Errorf("SUPERSEDES edges from w2 = %d, want 1", len(rels))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestSupersedeSnapshot_MultipleOldItems covers a Working section that
// replaces two current items sharing its authority and boundary at once
// (FR-DIR-007), the many-superseded-by-one shape T18's W2 exercises.
func TestSupersedeSnapshot_MultipleOldItems(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess, taskID = "sess-dir007-multi", "task"
	actor := principal(sess, domain.AuthorityUser)

	err := s.Update(ctx, sess, func(tx store.Tx) error {
		w1a := workingItem(sess, "m-w1a", tx.NextSeq(), domain.AuthorityUser)
		w1b := workingItem(sess, "m-w1b", tx.NextSeq(), domain.AuthorityUser)
		w2 := workingItem(sess, "m-w2", tx.NextSeq(), domain.AuthorityUser)
		mustInsert(t, tx, w1a, w1b, w2)

		rels, err := SupersedeSnapshot(tx, actor, []string{w2.ID}, taskID, "evt-w2")
		if err != nil {
			return err
		}
		if len(rels) != 2 {
			t.Errorf("SupersedeSnapshot relationships = %d, want 2", len(rels))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot supersession: %v", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		for _, id := range []string{"m-w1a", "m-w1b"} {
			if ok, err := IsCurrent(tx, id); err != nil || ok {
				t.Errorf("IsCurrent(%s) = %v, %v; want false, nil", id, ok, err)
			}
		}
		if ok, err := IsCurrent(tx, "m-w2"); err != nil || !ok {
			t.Errorf("IsCurrent(m-w2) = %v, %v; want true, nil", ok, err)
		}
		// The chain containing m-w1a also contains its sibling m-w1b, since
		// both were superseded by the same snapshot item m-w2.
		chain, err := SupersessionChain(tx, "m-w1a")
		if err != nil {
			return err
		}
		if len(chain) != 3 || chain[0] != "m-w2" {
			t.Errorf("SupersessionChain(m-w1a) = %v, want newest-first [m-w2 <m-w1a,m-w1b in some order>]", chain)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

// TestSupersedeSnapshot_ConversationKindAndIndependentTaskState is the
// SPEC-1.1 regression: a Working section may hold an item of kind
// conversation (FR-DIR-003), and a plain task_state item that merely shares
// the new item's authority and boundary, but was never part of a Working
// section, must not be swept up. Selection must key off Section==WORKING,
// not Kind==task_state.
func TestSupersedeSnapshot_ConversationKindAndIndependentTaskState(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess, taskID = "sess-dir007-spec11", "task"
	actor := principal(sess, domain.AuthorityUser)

	var oldConv, independent, newConv domain.ContextItem
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		oldConv = workingConversationItem(sess, "old-conv", tx.NextSeq(), domain.AuthorityUser)
		independent = independentTaskStateItem(sess, "independent", tx.NextSeq(), domain.AuthorityUser)
		newConv = workingConversationItem(sess, "new-conv", tx.NextSeq(), domain.AuthorityUser)
		mustInsert(t, tx, oldConv, independent, newConv)

		rels, err := SupersedeSnapshot(tx, actor, []string{newConv.ID}, taskID, "evt-conv")
		if err != nil {
			return err
		}
		if len(rels) != 1 || rels[0].ToID != oldConv.ID {
			t.Errorf("SupersedeSnapshot relationships = %+v, want exactly one edge to %s", rels, oldConv.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot supersession: %v", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		if ok, err := IsCurrent(tx, oldConv.ID); err != nil || ok {
			t.Errorf("IsCurrent(old conversation Working item) = %v, %v; want false, nil", ok, err)
		}
		if ok, err := IsCurrent(tx, newConv.ID); err != nil || !ok {
			t.Errorf("IsCurrent(new conversation Working item) = %v, %v; want true, nil", ok, err)
		}
		// The independent task_state item was never part of a Working
		// section (Section == SectionNone) and must be left alone.
		if ok, err := IsCurrent(tx, independent.ID); err != nil || !ok {
			t.Errorf("IsCurrent(independent task_state) = %v, %v; want true, nil (must not be superseded)", ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

// -- provenance ------------------------------------------------------------

func TestLinkDerived_And_Provenance_HappyPath(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess = "sess-prov-ok"
	actor := principal(sess, domain.AuthorityAgent)

	var derivedID, s1ID, s2ID string
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		derived := taskItem(sess, "derived", tx.NextSeq(), domain.AuthorityAgent)
		derived.EventID = "evt-link" // LinkDerived only runs in the creating event (AUTH-2.4)
		s1 := taskItem(sess, "src1", tx.NextSeq(), domain.AuthorityUser)
		s2 := taskItem(sess, "src2", tx.NextSeq(), domain.AuthorityUser)
		derivedID, s1ID, s2ID = derived.ID, s1.ID, s2.ID
		mustInsert(t, tx, derived, s1, s2)

		cov := &domain.Coverage{ConversationID: "conv", FromSeq: 1, ToSeq: 2}
		rels, err := LinkDerived(tx, actor, derived.ID, []string{s2.ID, s1.ID}, cov, "evt-link")
		if err != nil {
			return err
		}
		if len(rels) != 2 {
			t.Errorf("LinkDerived returned %d relationships, want 2", len(rels))
		}
		// Coverage.ItemIDs must be populated, sorted, and deduplicated from
		// the sources actually linked, regardless of the order given.
		wantIDs := []string{s1.ID, s2.ID}
		for _, r := range rels {
			if r.Coverage == nil {
				t.Fatalf("relationship %s has no coverage", r.ID)
			}
			if !slices.Equal(r.Coverage.ItemIDs, wantIDs) {
				t.Errorf("Coverage.ItemIDs = %v, want %v", r.Coverage.ItemIDs, wantIDs)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("LinkDerived: %v", err)
	}

	var got ProvenanceGraph
	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		var err error
		got, err = Provenance(tx, actor, derivedID)
		return err
	})
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	if got.Truncated {
		t.Error("Truncated = true, want false")
	}
	if len(got.Nodes) != 3 {
		t.Fatalf("Nodes = %d, want 3 (derived + 2 sources)", len(got.Nodes))
	}
	if len(got.Edges) != 2 {
		t.Fatalf("Edges = %d, want 2", len(got.Edges))
	}
	seen := map[string]bool{}
	for _, n := range got.Nodes {
		seen[n.ID] = true
	}
	for _, id := range []string{derivedID, s1ID, s2ID} {
		if !seen[id] {
			t.Errorf("Nodes missing %s", id)
		}
	}
}

// TestProvenance_Truncation_T04 mirrors T04 (changing agents does not carry
// private history): a node's provenance depends on an AGENT-scoped item
// belonging to a different agent than the querying principal. That node
// must be omitted, its content never disclosed, and the result marked
// Truncated.
func TestProvenance_Truncation_T04(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess = "sess-t04"
	var rootID, visibleID, hiddenID string

	err := s.Update(ctx, sess, func(tx store.Tx) error {
		root := taskItem(sess, "root", tx.NextSeq(), domain.AuthorityAgent)
		visible := taskItem(sess, "vis", tx.NextSeq(), domain.AuthorityTool)
		hidden := agentScopedItem(sess, "hidden", tx.NextSeq(), "agent-b")
		rootID, visibleID, hiddenID = root.ID, visible.ID, hidden.ID
		mustInsert(t, tx, root, visible, hidden)

		// DEPENDS_ON is inserted directly here (as tool-call/tool-result
		// wiring elsewhere in the system would) to set up a root whose
		// dependency graph reaches into another agent's private evidence.
		rel1 := domain.Relationship{
			ID: "rel-root-vis", SessionID: sess, Type: domain.RelDependsOn,
			FromID: root.ID, ToID: visible.ID, Seq: tx.NextSeq(),
			Authority: domain.AuthorityAgent, EventID: "e1",
		}
		if err := tx.InsertRelationship(rel1); err != nil {
			return err
		}
		rel2 := domain.Relationship{
			ID: "rel-root-hidden", SessionID: sess, Type: domain.RelDependsOn,
			FromID: root.ID, ToID: hidden.ID, Seq: tx.NextSeq(),
			Authority: domain.AuthorityAgent, EventID: "e1",
		}
		return tx.InsertRelationship(rel2)
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	queryingAgent := principalWithAgent(sess, domain.AuthorityAgent, "agent-a")
	var got ProvenanceGraph
	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		var err error
		got, err = Provenance(tx, queryingAgent, rootID)
		return err
	})
	if err != nil {
		t.Fatalf("Provenance: %v", err)
	}
	if !got.Truncated {
		t.Error("Truncated = false, want true")
	}
	if len(got.Nodes) != 2 {
		t.Errorf("Nodes = %d, want 2 (root + visible)", len(got.Nodes))
	}
	for _, n := range got.Nodes {
		if n.ID == hiddenID {
			t.Errorf("hidden node %s leaked into provenance result", hiddenID)
		}
	}
	if len(got.Edges) != 1 || got.Edges[0].ToID != visibleID {
		t.Errorf("Edges = %+v, want a single edge to %s", got.Edges, visibleID)
	}
}

func TestProvenance_RootMustBeAccessible(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess = "sess-prov-root"
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		hidden := agentScopedItem(sess, "hidden-root", tx.NextSeq(), "agent-b")
		return tx.InsertItem(hidden)
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		_, err := Provenance(tx, principalWithAgent(sess, domain.AuthorityAgent, "agent-a"), "hidden-root")
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// -- LinkDerived actor-authority and creation-event gates (TEST-2.2, AUTH-2.4) --

// TestLinkDerived_ActorAuthorityRequired is TEST-2.2 (the missing
// regression test for AUTH-1.1): a TOOL actor, a RETRIEVED_CONTENT actor,
// and an under-authority AGENT actor must each be rejected with
// ErrInvalidAuthorityPromotion, against an otherwise-valid call (accessible
// derived item, accessible source, satisfied boundary, matching creation
// event) so only the actor-authority gate is under test.
func TestLinkDerived_ActorAuthorityRequired(t *testing.T) {
	for _, tc := range []struct {
		name             string
		actorAuthority   domain.Authority
		derivedAuthority domain.Authority
	}{
		{"ToolActor", domain.AuthorityTool, domain.AuthorityAgent},
		{"RetrievedContentActor", domain.AuthorityRetrievedContent, domain.AuthorityAgent},
		{"UnderAuthorityAgentActor", domain.AuthorityAgent, domain.AuthorityUser},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := memory.New()
			defer s.Close()
			sess := "sess-linkderived-authority-" + tc.name
			actor := principal(sess, tc.actorAuthority)

			err := s.Update(ctx, sess, func(tx store.Tx) error {
				derived := taskItem(sess, "derived", tx.NextSeq(), tc.derivedAuthority)
				derived.EventID = "evt"
				src := taskItem(sess, "src", tx.NextSeq(), domain.AuthorityUser)
				mustInsert(t, tx, derived, src)
				_, err := LinkDerived(tx, actor, derived.ID, []string{src.ID}, nil, "evt")
				return err
			})
			if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
				t.Fatalf("err = %v, want ErrInvalidAuthorityPromotion", err)
			}
		})
	}
}

// TestLinkDerived_MustBeCreationEvent is AUTH-2.4: LinkDerived may only run
// in the transaction/event that created the derived item.
func TestLinkDerived_MustBeCreationEvent(t *testing.T) {
	t.Run("DifferentEventRejected", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		const sess = "sess-auth24-diff-event"
		actor := principal(sess, domain.AuthorityAgent)

		var derivedID string
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			derived := taskItem(sess, "note", tx.NextSeq(), domain.AuthorityAgent)
			derived.EventID = "evt-created-note"
			src := taskItem(sess, "src", tx.NextSeq(), domain.AuthorityUser)
			derivedID = derived.ID
			mustInsert(t, tx, derived, src)
			// A later, different event tries to attach provenance
			// post-hoc.
			_, err := LinkDerived(tx, actor, derived.ID, []string{src.ID}, nil, "evt-later-and-different")
			return err
		})
		if !errors.Is(err, ErrDerivedLinkNotAtCreation) {
			t.Fatalf("err = %v, want ErrDerivedLinkNotAtCreation", err)
		}

		err = s.View(ctx, sess, func(tx store.ReadTx) error {
			rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: derivedID})
			if err != nil {
				return err
			}
			if len(rels) != 0 {
				t.Errorf("relationships from %s = %d, want 0", derivedID, len(rels))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("view: %v", err)
		}
	})

	t.Run("SameEventAllowed", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		const sess, eventID = "sess-auth24-same-event", "evt-created-note"
		actor := principal(sess, domain.AuthorityAgent)

		err := s.Update(ctx, sess, func(tx store.Tx) error {
			derived := taskItem(sess, "note", tx.NextSeq(), domain.AuthorityAgent)
			derived.EventID = eventID
			src := taskItem(sess, "src", tx.NextSeq(), domain.AuthorityUser)
			mustInsert(t, tx, derived, src)
			_, err := LinkDerived(tx, actor, derived.ID, []string{src.ID}, nil, eventID)
			return err
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

// -- derived boundary rejection and all-or-nothing writes ------------------

func TestLinkDerived_BoundaryRejection(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess = "sess-boundary"
	actor := principal(sess, domain.AuthorityAgent)

	var derivedID string
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		src := taskItem(sess, "src", tx.NextSeq(), domain.AuthorityUser) // TASK-scoped
		derived := storetest.NewItem(sess, "derived", tx.NextSeq(), "summary")
		derived.Authority = domain.AuthorityAgent // passes the actor-authority gate (AUTH-1.1); SESSION scope still trips the boundary check
		derived.EventID = "evt"                   // passes the same-event gate (AUTH-2.4)
		derivedID = derived.ID
		mustInsert(t, tx, src, derived)
		_, err := LinkDerived(tx, actor, derived.ID, []string{src.ID}, nil, "evt")
		return err
	})
	if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		t.Fatalf("err = %v, want ErrInvalidAuthorityPromotion", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: derivedID})
		if err != nil {
			return err
		}
		if len(rels) != 0 {
			t.Errorf("relationships from %s = %d, want 0 (nothing should have been written)", derivedID, len(rels))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestLinkDerived_CoverageMismatchWritesNothing(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess = "sess-coverage-mismatch"
	actor := principal(sess, domain.AuthorityAgent)

	var derivedID string
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		derived := taskItem(sess, "derived-cm", tx.NextSeq(), domain.AuthorityAgent)
		derived.EventID = "evt"
		src := taskItem(sess, "src-cm", tx.NextSeq(), domain.AuthorityUser)
		derivedID = derived.ID
		mustInsert(t, tx, derived, src)

		// The coverage names an item that isn't among the sources being
		// linked, so it disagrees with the real source set.
		cov := &domain.Coverage{ItemIDs: []string{"someone-else-entirely"}}
		_, err := LinkDerived(tx, actor, derived.ID, []string{src.ID}, cov, "evt")
		return err
	})
	if !errors.Is(err, ErrCoverageMismatch) {
		t.Fatalf("err = %v, want ErrCoverageMismatch", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: derivedID})
		if err != nil {
			return err
		}
		if len(rels) != 0 {
			t.Errorf("relationships from %s = %d, want 0", derivedID, len(rels))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestLinkDerived_InaccessibleSourceWritesNothing(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess = "sess-t17-evidence"
	actor := principalWithAgent(sess, domain.AuthorityAgent, "agent-a")

	var derivedID, visibleID, hiddenID string
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		derived := taskItem(sess, "note", tx.NextSeq(), domain.AuthorityAgent)
		derived.EventID = "evt-note"
		visible := taskItem(sess, "x9", tx.NextSeq(), domain.AuthorityTool)
		hidden := agentScopedItem(sess, "other-session-item", tx.NextSeq(), "agent-b")
		derivedID, visibleID, hiddenID = derived.ID, visible.ID, hidden.ID
		mustInsert(t, tx, derived, visible, hidden)

		_, err := LinkDerived(tx, actor, derived.ID, []string{visible.ID, hidden.ID}, nil, "evt-note")
		return err
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: derivedID})
		if err != nil {
			return err
		}
		if len(rels) != 0 {
			t.Errorf("relationships from %s = %d, want 0", derivedID, len(rels))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
	_ = visibleID
	_ = hiddenID
}

// TestLinkDerived_AllOrNothing forces LinkDerived to fail partway through
// its insertion loop (a decoy relationship, committed beforehand, collides
// with the ID LinkDerived would generate for the second source) and checks
// that the first source's edge, written earlier in the same failed
// transaction, was not left behind.
func TestLinkDerived_AllOrNothing(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess, eventID = "sess-allornothing", "evt-link"
	actor := principal(sess, domain.AuthorityAgent)

	var derivedID, s1ID, s2ID string
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		derived := taskItem(sess, "derived2", tx.NextSeq(), domain.AuthorityAgent)
		derived.EventID = eventID
		s1 := taskItem(sess, "s1", tx.NextSeq(), domain.AuthorityUser)
		s2 := taskItem(sess, "s2", tx.NextSeq(), domain.AuthorityUser)
		derivedID, s1ID, s2ID = derived.ID, s1.ID, s2.ID
		mustInsert(t, tx, derived, s1, s2)

		decoy := domain.Relationship{
			ID:        relationshipID(sess, domain.RelDerivedFrom, derived.ID, s2.ID, eventID),
			SessionID: sess,
			Type:      domain.RelDerivedFrom,
			FromID:    derived.ID,
			ToID:      s2.ID,
			Seq:       tx.NextSeq(),
			Authority: domain.AuthorityAgent,
			EventID:   "decoy",
		}
		return tx.InsertRelationship(decoy)
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	err = s.Update(ctx, sess, func(tx store.Tx) error {
		_, err := LinkDerived(tx, actor, derivedID, []string{s1ID, s2ID}, nil, eventID)
		return err
	})
	if !errors.Is(err, domain.ErrImmutable) {
		t.Fatalf("err = %v, want ErrImmutable", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: derivedID, ToID: s1ID})
		if err != nil {
			return err
		}
		if len(rels) != 0 {
			t.Errorf("s1 relationship persisted despite a failed transaction: %d", len(rels))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

// -- ResolveLifecycleTarget (SPEC-2.2) --------------------------------------

func TestResolveLifecycleTarget_LiteralItemID(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess, taskID = "sess-resolve-literal", "task"
	actor := principal(sess, domain.AuthorityUser)

	var itemID string
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		it := taskItem(sess, "plain-item", tx.NextSeq(), domain.AuthorityUser)
		itemID = it.ID
		return tx.InsertItem(it)
	})
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		got, err := ResolveLifecycleTarget(tx, actor, taskID, itemID)
		if err != nil {
			return err
		}
		if got != itemID {
			t.Errorf("got %q, want %q", got, itemID)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}

func TestResolveLifecycleTarget_LiteralItemNotCurrentOrInaccessible(t *testing.T) {
	const sess, taskID = "sess-resolve-literal-bad", "task"

	t.Run("Superseded", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		actor := principal(sess, domain.AuthorityUser)
		var oldID string
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			oldItem := taskItem(sess, "old", tx.NextSeq(), domain.AuthorityUser)
			newItem := taskItem(sess, "new", tx.NextSeq(), domain.AuthorityUser)
			oldID = oldItem.ID
			mustInsert(t, tx, oldItem, newItem)
			_, err := Supersede(tx, actor, newItem.ID, oldItem.ID, "evt", "")
			return err
		})
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
		err = s.View(ctx, sess, func(tx store.ReadTx) error {
			_, err := ResolveLifecycleTarget(tx, actor, taskID, oldID)
			return err
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("Inaccessible", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		var hiddenID string
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			hidden := agentScopedItem(sess, "hidden", tx.NextSeq(), "agent-b")
			hiddenID = hidden.ID
			return tx.InsertItem(hidden)
		})
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
		err = s.View(ctx, sess, func(tx store.ReadTx) error {
			_, err := ResolveLifecycleTarget(tx, principalWithAgent(sess, domain.AuthorityUser, "agent-a"), taskID, hiddenID)
			return err
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

func TestResolveLifecycleTarget_DirectiveID(t *testing.T) {
	const taskID, dirID = "task", "g"

	t.Run("SingleAccessibleVersion", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		const sess = "sess-resolve-single"
		actor := principal(sess, domain.AuthorityUser)
		var goalID string
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			g := storetest.NewGoal(sess, "goal1", tx.NextSeq(), "Ship it")
			g.DirectiveID = dirID
			goalID = g.ID
			mustInsert(t, tx, g)
			_, err := ReplaceDirective(tx, actor, taskID, dirID, g.ID, "evt")
			return err
		})
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
		err = s.View(ctx, sess, func(tx store.ReadTx) error {
			got, err := ResolveLifecycleTarget(tx, actor, taskID, dirID)
			if err != nil {
				return err
			}
			if got != goalID {
				t.Errorf("got %q, want %q", got, goalID)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("view: %v", err)
		}
	})

	t.Run("NoAccessibleVersion", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		const sess = "sess-resolve-none"
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			hidden := agentScopedItem(sess, "hidden-goal", tx.NextSeq(), "agent-b")
			hidden.DirectiveID = dirID
			mustInsert(t, tx, hidden)
			_, err := ReplaceDirective(tx, principalWithAgent(sess, domain.AuthorityUser, "agent-b"), taskID, dirID, hidden.ID, "evt")
			return err
		})
		if err != nil {
			t.Fatalf("setup: %v", err)
		}
		err = s.View(ctx, sess, func(tx store.ReadTx) error {
			_, err := ResolveLifecycleTarget(tx, principalWithAgent(sess, domain.AuthorityUser, "agent-a"), taskID, dirID)
			return err
		})
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	// AmbiguousAcrossBoundaries reproduces AUTH-2.1's own residual case: a
	// private AGENT-scoped version is filed first; a task-wide version is
	// filed second by a principal who genuinely cannot see the private one
	// (so rejectVisibleBoundaryConflict correctly does not, and must not,
	// block that second write — doing so would require disclosing the
	// hidden version's existence to the task-wide filer). The two versions
	// are now both current, and both visible to the private version's own
	// agent, who must get ErrAmbiguousDirective rather than an arbitrary
	// pick.
	t.Run("AmbiguousAcrossBoundaries", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		const sess = "sess-resolve-ambiguous"

		err := s.Update(ctx, sess, func(tx store.Tx) error {
			private := agentScopedItem(sess, "d-private", tx.NextSeq(), "agent-a")
			private.DirectiveID = dirID
			mustInsert(t, tx, private)
			_, err := ReplaceDirective(tx, principalWithAgent(sess, domain.AuthorityUser, "agent-a"), taskID, dirID, private.ID, "evt-private")
			return err
		})
		if err != nil {
			t.Fatalf("setup private version: %v", err)
		}

		err = s.Update(ctx, sess, func(tx store.Tx) error {
			taskWide := taskItem(sess, "d-task-wide", tx.NextSeq(), domain.AuthorityHarness)
			taskWide.DirectiveID = dirID
			mustInsert(t, tx, taskWide)
			// A HARNESS/task-wide principal cannot see agent-a's private
			// version, so this must succeed (round-1 AUTH-2.1 property: a
			// hidden boundary never blocks or is revealed).
			prev, err := ReplaceDirective(tx, principal(sess, domain.AuthorityHarness), taskID, dirID, taskWide.ID, "evt-task-wide")
			if err != nil {
				return err
			}
			if prev != "" {
				t.Errorf("prev = %q, want empty (agent-a's private version must stay invisible)", prev)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("setup task-wide version: %v", err)
		}

		err = s.View(ctx, sess, func(tx store.ReadTx) error {
			_, err := ResolveLifecycleTarget(tx, principalWithAgent(sess, domain.AuthorityUser, "agent-a"), taskID, dirID)
			return err
		})
		if !errors.Is(err, ErrAmbiguousDirective) {
			t.Fatalf("err = %v, want ErrAmbiguousDirective", err)
		}
	})
}

// TestResolveLifecycleTarget_HiddenItemNeverBlocksDirective is SPEC-3.1:
// checking the literal item ID before the directive ID, and returning on
// any failure there, let a hidden item that merely shares an ID string with
// an accessible directive change the result — an existence oracle that also
// blocked an otherwise-authorized Resolve/Unpin. The literal-item lookup
// must never short-circuit the directive-ID lookup; it can only add a
// candidate, never remove the directive's.
func TestResolveLifecycleTarget_HiddenItemNeverBlocksDirective(t *testing.T) {
	const taskID, sharedID = "task", "shared-id"

	// resolve sets up an accessible goal directive at sharedID, optionally
	// inserts a second item invisible to the resolving actor whose actual
	// item ID (not directive ID) is also the literal string sharedID, and
	// returns what ResolveLifecycleTarget(sharedID) resolves to.
	resolve := func(t *testing.T, withHiddenItem bool) (string, error) {
		t.Helper()
		s := memory.New()
		defer s.Close()
		sess := t.Name()
		actor := principal(sess, domain.AuthorityUser)

		var goalID string
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			g := storetest.NewGoal(sess, "goal", tx.NextSeq(), "Ship it")
			g.DirectiveID = sharedID
			goalID = g.ID
			mustInsert(t, tx, g)
			_, err := ReplaceDirective(tx, actor, taskID, sharedID, g.ID, "evt")
			return err
		})
		if err != nil {
			t.Fatalf("setup goal: %v", err)
		}

		if withHiddenItem {
			err = s.Update(ctx, sess, func(tx store.Tx) error {
				hidden := agentScopedItem(sess, sharedID, tx.NextSeq(), "agent-b")
				return tx.InsertItem(hidden)
			})
			if err != nil {
				t.Fatalf("setup hidden item: %v", err)
			}
		}

		var got string
		var resolveErr error
		err = s.View(ctx, sess, func(tx store.ReadTx) error {
			got, resolveErr = ResolveLifecycleTarget(tx, actor, taskID, sharedID)
			return nil
		})
		if err != nil {
			t.Fatalf("view: %v", err)
		}
		if resolveErr == nil && got != goalID {
			t.Fatalf("got %q, want the goal %q", got, goalID)
		}
		return got, resolveErr
	}

	without, withoutErr := resolve(t, false)
	if withoutErr != nil {
		t.Fatalf("without hidden item: unexpected error %v", withoutErr)
	}

	with, withErr := resolve(t, true)
	if withErr != nil {
		t.Fatalf("err = %v, want nil (the hidden item must not block the directive)", withErr)
	}
	if with != without {
		t.Errorf("with hidden item resolved to %q, without resolved to %q; a hidden item must never change the result", with, without)
	}

	t.Run("AccessibleItemAndAccessibleDirectiveCollideAmbiguous", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		sess := t.Name()
		actor := principal(sess, domain.AuthorityUser)

		err := s.Update(ctx, sess, func(tx store.Tx) error {
			g := storetest.NewGoal(sess, "goal", tx.NextSeq(), "Ship it")
			g.DirectiveID = sharedID
			mustInsert(t, tx, g)
			_, err := ReplaceDirective(tx, actor, taskID, sharedID, g.ID, "evt")
			return err
		})
		if err != nil {
			t.Fatalf("setup goal: %v", err)
		}

		// A second, unrelated item whose own item ID (not directive ID) is
		// the same string, and IS accessible to actor.
		err = s.Update(ctx, sess, func(tx store.Tx) error {
			it := taskItem(sess, sharedID, tx.NextSeq(), domain.AuthorityUser)
			return tx.InsertItem(it)
		})
		if err != nil {
			t.Fatalf("setup colliding item: %v", err)
		}

		err = s.View(ctx, sess, func(tx store.ReadTx) error {
			_, err := ResolveLifecycleTarget(tx, actor, taskID, sharedID)
			return err
		})
		if !errors.Is(err, ErrAmbiguousDirective) {
			t.Fatalf("err = %v, want ErrAmbiguousDirective", err)
		}
	})

	t.Run("NoncurrentLiteralItemTreatedAsAbsent", func(t *testing.T) {
		s := memory.New()
		defer s.Close()
		sess := t.Name()
		actor := principal(sess, domain.AuthorityUser)

		// A directive at sharedID, filed normally.
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			g := storetest.NewGoal(sess, "goal", tx.NextSeq(), "Ship it")
			g.DirectiveID = sharedID
			mustInsert(t, tx, g)
			_, err := ReplaceDirective(tx, actor, taskID, sharedID, g.ID, "evt")
			return err
		})
		if err != nil {
			t.Fatalf("setup goal: %v", err)
		}

		// A now-superseded item whose own item ID happens to equal
		// sharedID too: accessible, but not current, so it must not count
		// as a candidate at all, and must not block the directive result.
		var oldID, replacementID string
		err = s.Update(ctx, sess, func(tx store.Tx) error {
			old := taskItem(sess, sharedID, tx.NextSeq(), domain.AuthorityUser)
			replacement := taskItem(sess, "replacement", tx.NextSeq(), domain.AuthorityUser)
			oldID, replacementID = old.ID, replacement.ID
			mustInsert(t, tx, old, replacement)
			_, err := Supersede(tx, actor, replacement.ID, old.ID, "evt2", "")
			return err
		})
		if err != nil {
			t.Fatalf("setup superseded item: %v", err)
		}
		_ = replacementID

		err = s.View(ctx, sess, func(tx store.ReadTx) error {
			if ok, err := IsCurrent(tx, oldID); err != nil || ok {
				t.Fatalf("IsCurrent(old) = %v, %v; want false, nil (test setup invariant)", ok, err)
			}
			got, err := ResolveLifecycleTarget(tx, actor, taskID, sharedID)
			if err != nil {
				return err
			}
			if got == oldID {
				t.Errorf("got the superseded item %q; it must be treated as absent", got)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("err = %v, want nil (the stale item must not block the directive)", err)
		}
	})
}

// -- deep chain -------------------------------------------------------------

// TestSupersessionChain_DeepNoRecursion builds a 10,000-long supersession
// chain and walks it, checking that neither Supersede's cycle-safety nor
// SupersessionChain itself recurses (both are written as iterative walks).
func TestSupersessionChain_DeepNoRecursion(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 10,000-item chain; skipped in -short")
	}
	s := memory.New()
	defer s.Close()
	const sess = "sess-deep"
	const n = 10000
	actor := principal(sess, domain.AuthorityUser)
	ids := make([]string, n)

	err := s.Update(ctx, sess, func(tx store.Tx) error {
		var prevID string
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("chain-%d", i)
			ids[i] = id
			it := taskItem(sess, id, tx.NextSeq(), domain.AuthorityUser)
			if err := tx.InsertItem(it); err != nil {
				return fmt.Errorf("insert %d: %w", i, err)
			}
			if i > 0 {
				if _, err := Supersede(tx, actor, id, prevID, fmt.Sprintf("evt-%d", i), ""); err != nil {
					return fmt.Errorf("supersede %d: %w", i, err)
				}
			}
			prevID = id
		}
		return nil
	})
	if err != nil {
		t.Fatalf("build chain: %v", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		chain, err := SupersessionChain(tx, ids[0])
		if err != nil {
			return err
		}
		if len(chain) != n {
			t.Fatalf("chain length = %d, want %d", len(chain), n)
		}
		if chain[0] != ids[n-1] {
			t.Errorf("chain[0] = %q, want newest %q", chain[0], ids[n-1])
		}
		if chain[n-1] != ids[0] {
			t.Errorf("chain[%d] = %q, want oldest %q", n-1, chain[n-1], ids[0])
		}
		if ok, err := IsCurrent(tx, ids[n-1]); err != nil || !ok {
			t.Errorf("IsCurrent(newest) = %v, %v; want true, nil", ok, err)
		}
		if ok, err := IsCurrent(tx, ids[0]); err != nil || ok {
			t.Errorf("IsCurrent(oldest) = %v, %v; want false, nil", ok, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("view: %v", err)
	}
}
