package graph

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
		cur, err := tx.CurrentDirective(taskID, dirID)
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

	t.Run("ToolAndRetrievedContentNeitherSupersedesTheOther", func(t *testing.T) {
		for _, dir := range []struct {
			name       string
			newA, oldA domain.Authority
			actorA     domain.Authority
		}{
			{"ToolOverRetrieved", domain.AuthorityTool, domain.AuthorityRetrievedContent, domain.AuthorityTool},
			{"RetrievedOverTool", domain.AuthorityRetrievedContent, domain.AuthorityTool, domain.AuthorityRetrievedContent},
		} {
			t.Run(dir.name, func(t *testing.T) {
				s := memory.New()
				defer s.Close()
				sess := "sess-authz-2-" + dir.name
				err := s.Update(ctx, sess, func(tx store.Tx) error {
					oldItem := taskItem(sess, "old", tx.NextSeq(), dir.oldA)
					newItem := taskItem(sess, "new", tx.NextSeq(), dir.newA)
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
		cur, err := tx.CurrentDirective(taskID, dirID)
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

// -- FR-DIR-007-style multi-item snapshot supersession --------------------

func TestSupersede_MultiItemSnapshot_FRDIR007(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess = "sess-dir007"
	actor := principal(sess, domain.AuthorityUser)

	err := s.Update(ctx, sess, func(tx store.Tx) error {
		w1a := taskItem(sess, "w1a", tx.NextSeq(), domain.AuthorityUser)
		w1b := taskItem(sess, "w1b", tx.NextSeq(), domain.AuthorityUser)
		w2 := taskItem(sess, "w2", tx.NextSeq(), domain.AuthorityUser)
		mustInsert(t, tx, w1a, w1b, w2)

		if _, err := Supersede(tx, actor, w2.ID, w1a.ID, "evt-w2", ""); err != nil {
			return err
		}
		_, err := Supersede(tx, actor, w2.ID, w1b.ID, "evt-w2", "")
		return err
	})
	if err != nil {
		t.Fatalf("snapshot supersession: %v", err)
	}

	err = s.View(ctx, sess, func(tx store.ReadTx) error {
		for _, id := range []string{"w1a", "w1b"} {
			if ok, err := IsCurrent(tx, id); err != nil || ok {
				t.Errorf("IsCurrent(%s) = %v, %v; want false, nil", id, ok, err)
			}
		}
		if ok, err := IsCurrent(tx, "w2"); err != nil || !ok {
			t.Errorf("IsCurrent(w2) = %v, %v; want true, nil", ok, err)
		}
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes, FromID: "w2"})
		if err != nil {
			return err
		}
		if len(rels) != 2 {
			t.Errorf("SUPERSEDES edges from w2 = %d, want 2", len(rels))
		}
		// The chain containing w1a also contains its sibling w1b, since
		// both were superseded by the same snapshot item w2.
		chain, err := SupersessionChain(tx, "w1a")
		if err != nil {
			return err
		}
		if len(chain) != 3 || chain[0] != "w2" {
			t.Errorf("SupersessionChain(w1a) = %v, want newest-first [w2 <w1a,w1b in some order>]", chain)
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
		s1 := taskItem(sess, "src1", tx.NextSeq(), domain.AuthorityUser)
		s2 := taskItem(sess, "src2", tx.NextSeq(), domain.AuthorityUser)
		derivedID, s1ID, s2ID = derived.ID, s1.ID, s2.ID
		mustInsert(t, tx, derived, s1, s2)

		cov := &domain.Coverage{ConversationID: "conv", FromSeq: 1, ToSeq: 2}
		rels, err := LinkDerived(tx, actor, derived.ID, []string{s1.ID, s2.ID}, cov, "evt-link")
		if err != nil {
			return err
		}
		if len(rels) != 2 {
			t.Errorf("LinkDerived returned %d relationships, want 2", len(rels))
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

func TestLinkDerived_InaccessibleSourceWritesNothing(t *testing.T) {
	s := memory.New()
	defer s.Close()
	const sess = "sess-t17-evidence"
	actor := principalWithAgent(sess, domain.AuthorityAgent, "agent-a")

	var derivedID, visibleID, hiddenID string
	err := s.Update(ctx, sess, func(tx store.Tx) error {
		derived := taskItem(sess, "note", tx.NextSeq(), domain.AuthorityAgent)
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
