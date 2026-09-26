package graph

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// goalLike returns an unfiled OPEN goal identical in every semantic field to
// fileGoal's items.
func goalLike(sess, id, dirID string, seq uint64, text string) domain.ContextItem {
	g := storetest.NewGoal(sess, id, seq, text)
	g.DirectiveID = dirID
	g.Section = domain.SectionGoal
	g.Namespace = domain.NamespaceDirective
	g.Scope = domain.ScopeTask
	g.Access = storetest.DirectiveBoundary(sess)
	return g
}

func TestLinkDuplicate_DirectiveDuplicate(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess, dirID = "sess-dup-ok", "ship"
		actor := principal(sess, domain.AuthorityUser)
		var canonical domain.ContextItem
		update(t, s, sess, func(tx store.Tx) error {
			canonical = fileGoal(t, tx, actor, "g1", dirID, "Ship it")
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			dup := goalLike(sess, "g2", dirID, tx.NextSeq(), "Ship it")
			mustCreate(t, tx, dup)
			rel, err := LinkDuplicate(tx, actor, dup.ID, canonical.ID, "evt-g2", "dedup/v1", "")
			if err != nil {
				return err
			}
			if rel.Type != domain.RelDuplicateOf || rel.FromID != dup.ID || rel.ToID != canonical.ID ||
				rel.Authority != actor.Authority || rel.EventID != "evt-g2" || rel.RuleVersion != "dedup/v1" {
				t.Errorf("relationship = %+v", rel)
			}
			return nil
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			if ok, _ := IsCurrent(tx, "g2"); ok {
				t.Errorf("duplicate is current")
			}
			if ok, _ := IsCurrent(tx, canonical.ID); !ok {
				t.Errorf("canonical is no longer current")
			}
			return nil
		})
		// A duplicate never supersedes (FR-ING-005).
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			_, err := Supersede(tx, actor, "g2", canonical.ID, "evt-late", "")
			return err
		})
		if !errors.Is(err, ErrDuplicateSupersession) {
			t.Errorf("Supersede from a duplicate: err = %v, want ErrDuplicateSupersession", err)
		}
	})
}

// TestLinkDuplicate_RejectsNonDuplicates covers D10's semantic identity: a
// write that differs in authority, boundary, eligibility origin, or any
// effective lifecycle metadata of the canonical's CURRENT state is not a
// duplicate and must go through authorized replacement instead.
func TestLinkDuplicate_RejectsNonDuplicates(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(canonical, dup *domain.ContextItem)
	}{
		{name: "Content", mutate: func(_, d *domain.ContextItem) { setText(d, "Ship it now") }},
		{name: "LowerAuthorityAgainstHigher", mutate: func(c, _ *domain.ContextItem) { c.Authority = domain.AuthoritySystem }},
		{name: "Kind", mutate: func(_, d *domain.ContextItem) {
			d.Kind = domain.KindInstruction
			d.GoalStatus = nil
		}},
		{name: "Retention", mutate: func(_, d *domain.ContextItem) { d.Retention = domain.RetentionHigh }},
		{name: "Generation", mutate: func(_, d *domain.ContextItem) { d.Generation = domain.GenerationPinned }},
		{name: "TTL", mutate: func(_, d *domain.ContextItem) { n := 3; d.TTLTurns, d.CreatedTurn = &n, 1 }},
		// R11: a TURN-bound item from another turn, or a TTL item with a
		// different expiry origin, is not a semantic duplicate.
		{name: "OtherTurnTurnScoped", mutate: func(c, d *domain.ContextItem) {
			for _, it := range []*domain.ContextItem{c, d} {
				it.Scope, it.Access.Scope, it.CreatedTurn = domain.ScopeTurn, domain.ScopeTurn, 1
			}
			d.TurnID, d.CreatedTurn = "turn-2", 2
		}},
		{name: "TTLOrigin", mutate: func(c, d *domain.ContextItem) {
			n := 3
			c.TTLTurns, d.TTLTurns = &n, &n
			c.CreatedTurn, d.CreatedTurn = 1, 2
		}},
		{name: "Section", mutate: func(_, d *domain.ContextItem) { d.Section = domain.SectionPinned }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s store.Store) {
				const sess, dirID = "sess-dup-reject", "ship"
				actor := principal(sess, domain.AuthoritySystem)
				var canonical domain.ContextItem
				update(t, s, sess, func(tx store.Tx) error {
					canonical = goalLike(sess, "g1", dirID, tx.NextSeq(), "Ship it")
					dup := canonical
					if tc.mutate != nil {
						tc.mutate(&canonical, &dup)
					}
					mustCreate(t, tx, canonical)
					_, err := ReplaceDirective(tx, actor, "task", dirID, canonical.ID, "evt-g1")
					return err
				})
				err := s.Update(ctx, sess, func(tx store.Tx) error {
					dup := goalLike(sess, "g2", dirID, tx.NextSeq(), "Ship it")
					if tc.mutate != nil {
						c := canonical
						tc.mutate(&c, &dup)
					}
					mustCreate(t, tx, dup)
					_, err := LinkDuplicate(tx, actor, dup.ID, canonical.ID, "evt-g2", "", "")
					return err
				})
				if !errors.Is(err, ErrNotDuplicate) {
					t.Fatalf("err = %v, want ErrNotDuplicate", err)
				}
			})
		})
	}
}

// TestLinkDuplicate_RestatementAfterResolveStaysResolved is P3-4/C-1: an
// identical restatement compares immutable creation declarations, never the
// canonical's current goal status. It is a noncurrent DUPLICATE_OF audit
// occurrence and must not reopen, replace or re-file the resolved goal;
// reopening requires an authorized ReplaceDirective.
func TestLinkDuplicate_RestatementAfterResolveStaysResolved(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess, dirID = "sess-dup-resolved", "ship"
		actor := principal(sess, domain.AuthoritySystem)
		update(t, s, sess, func(tx store.Tx) error {
			fileGoal(t, tx, actor, "g1", dirID, "Ship it")
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			resolved := domain.GoalResolved
			_, err := tx.UpdateItem("g1", 1, domain.ItemChange{GoalStatus: &resolved},
				storetest.NewItemEvent(sess, "resolve-g1", tx.NextSeq(), "g1"))
			return err
		})
		update(t, s, sess, func(tx store.Tx) error {
			dup := goalLike(sess, "g2", dirID, tx.NextSeq(), "Ship it")
			mustCreate(t, tx, dup)
			_, err := LinkDuplicate(tx, actor, dup.ID, "g1", "evt-g2", "", "")
			return err
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			g1, err := tx.Item("g1")
			if err != nil {
				return err
			}
			if g1.GoalStatus == nil || *g1.GoalStatus != domain.GoalResolved {
				t.Errorf("canonical goal status = %v, want RESOLVED (restatement never reopens)", g1.GoalStatus)
			}
			for id, want := range map[string]bool{"g1": true, "g2": false} {
				if ok, err := IsCurrent(tx, id); err != nil || ok != want {
					t.Errorf("IsCurrent(%s) = %v, %v; want %v", id, ok, err, want)
				}
			}
			return nil
		})
	})
}

// TestLinkDuplicate_CannotRetireExistingItems: because a DUPLICATE_OF edge
// makes a directive item non-current, it would be a suppression back door
// (M7) if it could be attached to an item after its creation, to an item
// that is already a current version, or against a canonical that is not
// itself current.
func TestLinkDuplicate_CannotRetireExistingItems(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-dup-backdoor"
		actor := principal(sess, domain.AuthorityUser)
		var a, b domain.ContextItem
		update(t, s, sess, func(tx store.Tx) error {
			a = fileGoal(t, tx, actor, "a", "da", "Same text")
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			b = goalLike(sess, "b", "da", tx.NextSeq(), "Same text")
			mustCreate(t, tx, b)
			return nil
		})
		// Not the creation transaction of b.
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			_, err := LinkDuplicate(tx, actor, b.ID, a.ID, "evt-late", "", "")
			return err
		})
		if !errors.Is(err, ErrDuplicateNotAtCreation) {
			t.Errorf("late link: err = %v, want ErrDuplicateNotAtCreation", err)
		}

		// An item already filed as the current version cannot then be
		// classified a duplicate of anything, even in its own transaction.
		err = s.Update(ctx, sess, func(tx store.Tx) error {
			c := goalLike(sess, "c", "dc", tx.NextSeq(), "Same text")
			c2 := goalLike(sess, "c2", "dc", tx.NextSeq(), "Same text")
			mustCreate(t, tx, c, c2)
			if _, err := ReplaceDirective(tx, actor, "task", "dc", c2.ID, "evt-c"); err != nil {
				return err
			}
			_, err := LinkDuplicate(tx, actor, c2.ID, c.ID, "evt-c", "", "")
			return err
		})
		if !errors.Is(err, ErrNotDuplicate) {
			t.Errorf("link a current version: err = %v, want ErrNotDuplicate", err)
		}

		// A retired canonical cannot stand in for a new write.
		update(t, s, sess, func(tx store.Tx) error {
			next := goalLike(sess, "a-next", "da", tx.NextSeq(), "Replacement")
			mustCreate(t, tx, next)
			_, err := ReplaceDirective(tx, actor, "task", "da", next.ID, "evt-next")
			return err
		})
		err = s.Update(ctx, sess, func(tx store.Tx) error {
			d := goalLike(sess, "d", "da", tx.NextSeq(), "Same text")
			mustCreate(t, tx, d)
			_, err := LinkDuplicate(tx, actor, d.ID, a.ID, "evt-d", "", "")
			return err
		})
		if !errors.Is(err, ErrNotDuplicate) {
			t.Errorf("stale canonical: err = %v, want ErrNotDuplicate", err)
		}

		// An inaccessible canonical is indistinguishable from a missing one.
		// A failed link poisons its transaction (P3-1), so each probe runs
		// in its own.
		for n, canonical := range []string{"hidden", "missing"} {
			err = s.Update(ctx, sess, func(tx store.Tx) error {
				hidden := agentScopedItem(sess, fmt.Sprintf("hidden-%d", n), tx.NextSeq(), "agent-b")
				d := agentScopedItem(sess, fmt.Sprintf("mine-%d", n), tx.NextSeq(), "agent")
				mustInsert(t, tx, hidden, d)
				target := canonical
				if canonical == "hidden" {
					target = hidden.ID
				}
				_, err := LinkDuplicate(tx, actor, d.ID, target, "evt-h", "", "")
				if err != domain.ErrNotFound {
					t.Errorf("%s canonical: err = %v, want bare ErrNotFound", canonical, err)
				}
				return err
			})
			if !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("%s canonical: commit err = %v, want ErrNotFound", canonical, err)
			}
		}
	})
}

// TestLinkDuplicate_NonDirectiveIsDetectionOnly: an ordinary repeated
// message is linked DUPLICATE_OF for detection, but remains current pending
// input with its own turn (D10).
func TestLinkDuplicate_NonDirectiveIsDetectionOnly(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-dup-plain"
		actor := principal(sess, domain.AuthorityUser)
		msg := func(id, turn string, seq uint64) domain.ContextItem {
			it := taskItem(sess, id, seq, domain.AuthorityUser)
			it.Kind = domain.KindUserMessage
			it.TurnID = turn
			setText(&it, "yes")
			return it
		}
		update(t, s, sess, func(tx store.Tx) error {
			mustInsert(t, tx, msg("m1", "turn-1", tx.NextSeq()))
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			m2 := msg("m2", "turn-2", tx.NextSeq())
			mustInsert(t, tx, m2)
			_, err := LinkDuplicate(tx, actor, m2.ID, "m1", "evt-m2", "", "")
			return err
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			if ok, _ := IsCurrent(tx, "m2"); !ok {
				t.Errorf("non-directive duplicate must stay current")
			}
			return nil
		})
		// Different authority is never deduplicated.
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			m3 := msg("m3", "turn-2", tx.NextSeq())
			m3.Authority = domain.AuthorityAgent
			m3.Kind = domain.KindAssistantMessage
			mustInsert(t, tx, m3)
			_, err := LinkDuplicate(tx, principal(sess, domain.AuthorityAgent), m3.ID, "m1", "evt-m3", "", "")
			return err
		})
		if !errors.Is(err, ErrNotDuplicate) {
			t.Errorf("cross-authority: err = %v, want ErrNotDuplicate", err)
		}
	})
}

// setText replaces an item's content with one text part, keeping its hash
// and size consistent.
func setText(it *domain.ContextItem, text string) {
	it.Parts = []domain.ContentPart{{Type: domain.PartText, MediaType: "text/plain", Text: text}}
	it.ContentHash = domain.ContentHash(it.Parts)
	it.SemanticBytes = domain.SemanticBytes(it.Parts)
}

// TestLinkDuplicate_RestatedAcrossTurns (R11): a TASK-scoped directive with
// no TTL restated verbatim in a later turn is a duplicate: its turn origin
// governs no eligibility, so restating it must not mint a fresh version
// (which would retire and reset its obligation).
func TestLinkDuplicate_RestatedAcrossTurns(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-dup-restate"
		actor := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			g := goalLike(sess, "g1", "ship", tx.NextSeq(), "Ship it")
			g.CreatedTurn = 1
			mustCreate(t, tx, g)
			_, err := ReplaceDirective(tx, actor, "task", "ship", g.ID, "evt-1")
			return err
		})
		update(t, s, sess, func(tx store.Tx) error {
			g := goalLike(sess, "g2", "ship", tx.NextSeq(), "Ship it")
			g.TurnID, g.CreatedTurn = "turn-5", 5
			mustCreate(t, tx, g)
			_, err := LinkDuplicate(tx, actor, g.ID, "g1", "evt-5", "", "")
			return err
		})
	})
}

// TestLinkDuplicate_ComparesObligationClaim is SPEC-1.12 (R11): the one
// duplicate comparison includes the obligation declaration, so a Pinned
// item is never DUPLICATE_OF a canonical whose declared obligation differs.
// The declaration is the persisted creation declaration's obligation hash;
// the legacy claim argument is not authority and cannot make a match (P3-4).
func TestLinkDuplicate_ComparesObligationClaim(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-dup-claim"
		actor := principal(sess, domain.AuthorityUser)
		declared := domain.HashBytes([]byte("tests_pass/1"))
		update(t, s, sess, func(tx store.Tx) error {
			p := newDirective(sess, "p1", "tests", tx.NextSeq(), "All tests pass")
			mustCreateWith(t, tx, CreationAcceptance{PolicyVersion: testDeclarationPolicy, ObligationDeclarationHash: declared}, p)
			_, err := ReplaceDirective(tx, actor, "task", "tests", p.ID, "evt-p1")
			return err
		})
		for i, c := range []struct {
			obligation, claim string
			want              error
		}{
			{domain.HashBytes([]byte("other_claim/1")), "tests_pass", ErrNotDuplicate},
			{"", "tests_pass", ErrNotDuplicate},
			{declared, "", nil},
		} {
			err := s.Update(ctx, sess, func(tx store.Tx) error {
				d := newDirective(sess, fmt.Sprintf("d%d", i), "tests", tx.NextSeq(), "All tests pass")
				mustCreateWith(t, tx, CreationAcceptance{PolicyVersion: testDeclarationPolicy, ObligationDeclarationHash: c.obligation}, d)
				_, err := LinkDuplicate(tx, actor, d.ID, "p1", "evt-d", "", c.claim)
				return err
			})
			if !errors.Is(err, c.want) {
				t.Errorf("declaration %d: err = %v, want %v", i, err, c.want)
			}
		}
	})
}
