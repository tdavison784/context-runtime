package graph

import (
	"errors"
	"slices"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestSupersede_RetiresATargetOnlyOnce is the D11 audit-identity finding:
// Supersede's audit record ID was keyed by (session, old target, action,
// event) without the successor, so two retirements of one target inside
// one event collided on the audit ID. A target that is already superseded
// is no longer current state (FR-REL-003) and must not be retired again,
// by the same event or any other; the refusal is explicit, not an
// accidental store immutability error.
func TestSupersede_RetiresATargetOnlyOnce(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d11-audit"
		actor := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			mustInsert(t, tx,
				taskItem(sess, "old", tx.NextSeq(), domain.AuthorityUser),
				taskItem(sess, "n1", tx.NextSeq(), domain.AuthorityUser),
				taskItem(sess, "n2", tx.NextSeq(), domain.AuthorityUser))
			return nil
		})
		for _, eventID := range []string{"evt-same", "evt-other"} {
			err := s.Update(ctx, sess, func(tx store.Tx) error {
				if _, err := Supersede(tx, actor, "n1", "old", "evt-same", ""); err != nil {
					return err
				}
				_, err := Supersede(tx, actor, "n2", "old", eventID, "")
				return err
			})
			if !errors.Is(err, ErrAlreadySuperseded) {
				t.Errorf("second retirement (%s): err = %v, want ErrAlreadySuperseded", eventID, err)
			}
		}
	})
}

// TestSupersede_AuditIdentityNamesSuccessor: the retirement audit record's
// identity is versioned and includes the successor, so the audit IDs of
// retirements by different successors in one event can never alias.
func TestSupersede_AuditIdentityNamesSuccessor(t *testing.T) {
	a := lifecycleEventID("s", "old", "superseded", "evt", "n1")
	b := lifecycleEventID("s", "old", "superseded", "evt", "n2")
	if a == b {
		t.Fatalf("audit IDs for different successors collide: %s", a)
	}
}

// -- D11: the planned Working snapshot edge set -------------------------------

// member returns an unfiled Working task_state item with the given text and
// the directive ID the parser derives for it (FR-DIR-002), so equal text in
// two snapshots yields the same directive ID, exactly as ingestion would.
func member(sess, id string, seq uint64, text string) domain.ContextItem {
	it := workingItem(sess, id, seq, domain.AuthorityUser)
	setText(&it, text)
	it.DirectiveID = domain.DerivedDirectiveID("working", it.ContentHash)
	return it
}

// snapshot inserts members in one transaction and ingests them as one
// Working section.
func snapshot(t *testing.T, s store.Store, actor domain.Principal, eventID string, members ...func(seq uint64) domain.ContextItem) SnapshotResult {
	t.Helper()
	var res SnapshotResult
	update(t, s, actor.SessionID, func(tx store.Tx) error {
		var ids []string
		for _, m := range members {
			it := m(tx.NextSeq())
			mustInsert(t, tx, it)
			ids = append(ids, it.ID)
		}
		var err error
		res, err = SupersedeSnapshot(tx, actor, ids, "task", eventID)
		return err
	})
	return res
}

func m(sess, id, text string) func(uint64) domain.ContextItem {
	return func(seq uint64) domain.ContextItem { return member(sess, id, seq, text) }
}

// edges renders relationships as "from->to" in result order.
func edges(rels []domain.Relationship) []string {
	var out []string
	for _, r := range rels {
		out = append(out, r.FromID+"->"+r.ToID)
	}
	return out
}

// currentSet returns which of ids are current, omitting absent ones.
func currentSet(t *testing.T, s store.Store, sess string, ids ...string) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	view(t, s, sess, func(tx store.ReadTx) error {
		for _, id := range ids {
			ok, err := IsCurrent(tx, id)
			if errors.Is(err, domain.ErrNotFound) {
				continue // not created by this case
			}
			if err != nil {
				return err
			}
			out[id] = ok
		}
		return nil
	})
	return out
}

// retirementAudits counts "superseded" audit records per target.
func retirementAudits(t *testing.T, s store.Store, sess string) map[string]int {
	t.Helper()
	out := map[string]int{}
	view(t, s, sess, func(tx store.ReadTx) error {
		evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetItem})
		if err != nil {
			return err
		}
		for _, e := range evs {
			if e.Action == "superseded" {
				out[e.TargetID]++
			}
		}
		return nil
	})
	return out
}

// TestSnapshot_T18_RepeatedMemberRetiresAll is the reviewed failure: W1 =
// {a, b} then W2 = {a}. Marking W2's a as DUPLICATE_OF W1's a made the
// snapshot fail with "graph: duplicate item cannot supersede", and simply
// skipping it would leave b current. A changed snapshot creates fresh
// versions: W2's a retires both W1 members, each exactly once, and is the
// only current Working state (T18).
func TestSnapshot_T18_RepeatedMemberRetiresAll(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d11-t18"
		actor := principal(sess, domain.AuthorityUser)
		snapshot(t, s, actor, "evt-w1", m(sess, "a1", "tests: 29 failing"), m(sess, "b1", "editing parser.go"))
		res := snapshot(t, s, actor, "evt-w2", m(sess, "a2", "tests: 29 failing"))

		if got, want := edges(res.Supersedes), []string{"a2->a1", "a2->b1"}; !slices.Equal(got, want) {
			t.Errorf("SUPERSEDES = %v, want %v", got, want)
		}
		if len(res.Duplicates) != 0 {
			t.Errorf("DUPLICATE_OF = %v, want none in a changed snapshot", edges(res.Duplicates))
		}
		cur := currentSet(t, s, sess, "a1", "b1", "a2")
		if cur["a1"] || cur["b1"] || !cur["a2"] {
			t.Errorf("current = %v, want only a2", cur)
		}
		if audits := retirementAudits(t, s, sess); audits["a1"] != 1 || audits["b1"] != 1 {
			t.Errorf("retirement audits = %v, want one each for a1 and b1", audits)
		}
	})
}

// TestSnapshot_IdenticalIsDuplicate: an identical whole snapshot is a
// duplicate snapshot: each member is DUPLICATE_OF its counterpart, nothing
// is superseded, and the prior members stay current.
func TestSnapshot_IdenticalIsDuplicate(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d11-identical"
		actor := principal(sess, domain.AuthorityUser)
		snapshot(t, s, actor, "evt-w1", m(sess, "a1", "A"), m(sess, "b1", "B"))
		res := snapshot(t, s, actor, "evt-w2", m(sess, "a2", "A"), m(sess, "b2", "B"))

		if len(res.Supersedes) != 0 {
			t.Errorf("SUPERSEDES = %v, want none", edges(res.Supersedes))
		}
		if got, want := edges(res.Duplicates), []string{"a2->a1", "b2->b1"}; !slices.Equal(got, want) {
			t.Errorf("DUPLICATE_OF = %v, want %v", got, want)
		}
		cur := currentSet(t, s, sess, "a1", "b1", "a2", "b2")
		if !cur["a1"] || !cur["b1"] || cur["a2"] || cur["b2"] {
			t.Errorf("current = %v, want a1 and b1 only", cur)
		}
	})
}

// TestSnapshot_ChangedSnapshots covers partial overlap, reordering, and a
// strict superset: any change makes every member a fresh version and
// retires every prior member exactly once, preferring the same-ID member
// as the retiring edge, else the first member in source order.
func TestSnapshot_ChangedSnapshots(t *testing.T) {
	cases := []struct {
		name      string
		w2        []func(uint64) domain.ContextItem
		wantEdges []string
		current   []string
	}{
		{
			name:      "PartialOverlap",
			w2:        []func(uint64) domain.ContextItem{m("sess", "b2", "B"), m("sess", "c2", "C")},
			wantEdges: []string{"b2->a1", "b2->b1"},
			current:   []string{"b2", "c2"},
		},
		{
			name:      "Reordered",
			w2:        []func(uint64) domain.ContextItem{m("sess", "b2", "B"), m("sess", "a2", "A")},
			wantEdges: []string{"a2->a1", "b2->b1"},
			current:   []string{"a2", "b2"},
		},
		{
			name:      "Superset",
			w2:        []func(uint64) domain.ContextItem{m("sess", "a2", "A"), m("sess", "b2", "B"), m("sess", "c2", "C")},
			wantEdges: []string{"a2->a1", "b2->b1"},
			current:   []string{"a2", "b2", "c2"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s store.Store) {
				actor := principal("sess", domain.AuthorityUser)
				snapshot(t, s, actor, "evt-w1", m("sess", "a1", "A"), m("sess", "b1", "B"))
				res := snapshot(t, s, actor, "evt-w2", tc.w2...)
				if got := edges(res.Supersedes); !slices.Equal(got, tc.wantEdges) {
					t.Errorf("SUPERSEDES = %v, want %v", got, tc.wantEdges)
				}
				if len(res.Duplicates) != 0 {
					t.Errorf("DUPLICATE_OF = %v, want none", edges(res.Duplicates))
				}
				cur := currentSet(t, s, "sess", "a1", "b1", "a2", "b2", "c2")
				for id, ok := range cur {
					if ok != slices.Contains(tc.current, id) {
						t.Errorf("IsCurrent(%s) = %v", id, ok)
					}
				}
				for id, n := range retirementAudits(t, s, "sess") {
					if n != 1 {
						t.Errorf("%s retired %d times", id, n)
					}
				}
			})
		})
	}
}

// TestSnapshot_ExplicitIDComposes: a Working member with an explicit ID
// also supersedes by ID (FR-DIR-007). When the ID's current version is a
// prior member of the same snapshot partition, the ID edge IS its snapshot
// retirement (one edge, one audit); when it is another section's item at
// the same boundary, it is retired too, under the same authorization.
func TestSnapshot_ExplicitIDComposes(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d11-explicit"
		actor := principal(sess, domain.AuthorityUser)
		withID := func(id, dirID, text string) func(uint64) domain.ContextItem {
			return func(seq uint64) domain.ContextItem {
				it := member(sess, id, seq, text)
				it.DirectiveID = dirID
				return it
			}
		}
		update(t, s, sess, func(tx store.Tx) error {
			pin := storetest.NewDirective(sess, "pin-x", "x", tx.NextSeq(), "pinned x")
			mustInsert(t, tx, pin)
			_, err := ReplaceDirective(tx, actor, "task", "x", pin.ID, "evt-pin")
			return err
		})
		snapshot(t, s, actor, "evt-w1", withID("s1", "status", "v1"), m(sess, "o1", "other"))
		res := snapshot(t, s, actor, "evt-w2", m(sess, "n2", "new"), withID("s2", "status", "v2"), withID("x2", "x", "working x"))

		if got, want := edges(res.Supersedes), []string{"x2->pin-x", "s2->s1", "n2->o1"}; !slices.Equal(got, want) {
			t.Errorf("SUPERSEDES = %v, want %v", got, want)
		}
		for id, n := range retirementAudits(t, s, sess) {
			if n != 1 {
				t.Errorf("%s retired %d times", id, n)
			}
		}
		view(t, s, sess, func(tx store.ReadTx) error {
			for dirID, want := range map[string]string{"status": "s2", "x": "x2"} {
				if got, err := ResolveLifecycleTarget(tx, actor, "task", dirID); err != nil || got != want {
					t.Errorf("Resolve(%s) = %q, %v; want %s", dirID, got, err, want)
				}
			}
			return nil
		})
	})
}

// TestSnapshot_MultipleSectionsOneEvent: sections are processed in source
// order within one event; the second retires the first's members without
// audit-ID collisions.
func TestSnapshot_MultipleSectionsOneEvent(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d11-multi"
		actor := principal(sess, domain.AuthorityUser)
		snapshot(t, s, actor, "evt-0", m(sess, "a0", "A"), m(sess, "b0", "B"))
		update(t, s, sess, func(tx store.Tx) error {
			first := []domain.ContextItem{member(sess, "a1", tx.NextSeq(), "A1"), member(sess, "b1", tx.NextSeq(), "B1")}
			mustInsert(t, tx, first...)
			if _, err := SupersedeSnapshot(tx, actor, []string{"a1", "b1"}, "task", "evt-1"); err != nil {
				return err
			}
			second := member(sess, "c1", tx.NextSeq(), "C1")
			mustInsert(t, tx, second)
			res, err := SupersedeSnapshot(tx, actor, []string{"c1"}, "task", "evt-1")
			if err != nil {
				return err
			}
			if got, want := edges(res.Supersedes), []string{"c1->a1", "c1->b1"}; !slices.Equal(got, want) {
				t.Errorf("second section SUPERSEDES = %v, want %v", got, want)
			}
			return nil
		})
		cur := currentSet(t, s, sess, "a0", "b0", "a1", "b1", "c1")
		for id, ok := range cur {
			if ok != (id == "c1") {
				t.Errorf("IsCurrent(%s) = %v", id, ok)
			}
		}
	})
}

// TestSnapshot_PartitionsByBoundaryAndAuthority: a snapshot retires only
// Working items of its own exact authority and boundary; a mixed-boundary
// section is one snapshot per boundary, and a boundary with no new member
// is untouched.
func TestSnapshot_PartitionsByBoundaryAndAuthority(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d11-partition"
		actor := principal(sess, domain.AuthorityUser)
		turnScoped := func(id, text string) func(uint64) domain.ContextItem {
			return func(seq uint64) domain.ContextItem {
				it := member(sess, id, seq, text)
				it.Scope = domain.ScopeTurn
				it.Access.Scope = domain.ScopeTurn
				return it
			}
		}
		snapshot(t, s, principal(sess, domain.AuthoritySystem), "evt-sys", func(seq uint64) domain.ContextItem {
			it := member(sess, "sys1", seq, "system state")
			it.Authority = domain.AuthoritySystem
			return it
		})
		snapshot(t, s, actor, "evt-w1", m(sess, "a1", "A"), turnScoped("t1", "T"))
		res := snapshot(t, s, actor, "evt-w2", m(sess, "a2", "A2"))
		if got, want := edges(res.Supersedes), []string{"a2->a1"}; !slices.Equal(got, want) {
			t.Errorf("SUPERSEDES = %v, want %v", got, want)
		}
		cur := currentSet(t, s, sess, "sys1", "t1", "a2")
		if !cur["sys1"] || !cur["t1"] || !cur["a2"] {
			t.Errorf("current = %v, want sys1, t1, a2 all current", cur)
		}
	})
}

// TestSnapshot_RejectsBeforeWriting: malformed or unauthorized snapshots
// fail before any edge, audit, or map write, so the caller's transaction
// rolls back to exactly the prior state.
func TestSnapshot_RejectsBeforeWriting(t *testing.T) {
	cases := []struct {
		name    string
		run     func(t *testing.T, tx store.Tx, actor domain.Principal) error
		wantErr error
	}{
		{
			name: "RepeatedDirectiveID",
			run: func(t *testing.T, tx store.Tx, actor domain.Principal) error {
				a, b := member("sess", "r1", tx.NextSeq(), "same"), member("sess", "r2", tx.NextSeq(), "same")
				mustInsert(t, tx, a, b)
				_, err := SupersedeSnapshot(tx, actor, []string{"r1", "r2"}, "task", "evt")
				return err
			},
			wantErr: ErrSnapshotMemberConflict,
		},
		{
			name: "RepeatedMemberID",
			run: func(t *testing.T, tx store.Tx, actor domain.Principal) error {
				mustInsert(t, tx, member("sess", "r1", tx.NextSeq(), "x"))
				_, err := SupersedeSnapshot(tx, actor, []string{"r1", "r1"}, "task", "evt")
				return err
			},
			wantErr: ErrSnapshotMemberConflict,
		},
		{
			name: "MixedAuthority",
			run: func(t *testing.T, tx store.Tx, _ domain.Principal) error {
				a := member("sess", "u", tx.NextSeq(), "u")
				b := member("sess", "h", tx.NextSeq(), "h")
				b.Authority = domain.AuthorityHarness
				mustInsert(t, tx, a, b)
				_, err := SupersedeSnapshot(tx, principal("sess", domain.AuthorityHarness), []string{"u", "h"}, "task", "evt")
				return err
			},
			wantErr: ErrSnapshotMemberConflict,
		},
		{
			name: "PreClassifiedDuplicate",
			run: func(t *testing.T, tx store.Tx, actor domain.Principal) error {
				d := member("sess", "d", tx.NextSeq(), "A")
				mustInsert(t, tx, d)
				rawDuplicateOf(t, tx, "d", "a1")
				_, err := SupersedeSnapshot(tx, actor, []string{"d"}, "task", "evt")
				return err
			},
			wantErr: ErrDuplicateSupersession,
		},
		{
			name: "NotCreatedInThisTransaction",
			run: func(t *testing.T, tx store.Tx, actor domain.Principal) error {
				_, err := SupersedeSnapshot(tx, actor, []string{"old-unfiled"}, "task", "evt")
				return err
			},
			wantErr: ErrSnapshotNotAtCreation,
		},
		{
			name: "LowerAuthorityIDReplacement",
			run: func(t *testing.T, tx store.Tx, actor domain.Principal) error {
				it := member("sess", "low", tx.NextSeq(), "low")
				it.DirectiveID = "sys-status"
				mustInsert(t, tx, it)
				_, err := SupersedeSnapshot(tx, actor, []string{"low"}, "task", "evt")
				return err
			},
			wantErr: domain.ErrInvalidAuthorityPromotion,
		},
		{
			name: "VisibleBoundaryChangeThroughIDReuse",
			run: func(t *testing.T, tx store.Tx, actor domain.Principal) error {
				it := member("sess", "turn-a", tx.NextSeq(), "A")
				it.Scope = domain.ScopeTurn
				it.Access.Scope = domain.ScopeTurn
				mustInsert(t, tx, it)
				_, err := SupersedeSnapshot(tx, actor, []string{"turn-a"}, "task", "evt")
				return err
			},
			wantErr: ErrBoundaryConflict,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s store.Store) {
				actor := principal("sess", domain.AuthorityUser)
				snapshot(t, s, actor, "evt-w1", m("sess", "a1", "A"), m("sess", "b1", "B"))
				update(t, s, "sess", func(tx store.Tx) error {
					mustInsert(t, tx, member("sess", "old-unfiled", tx.NextSeq(), "unfiled"))
					sys := member("sess", "sys", tx.NextSeq(), "system")
					sys.Authority = domain.AuthoritySystem
					sys.DirectiveID = "sys-status"
					mustInsert(t, tx, sys)
					mustFile(t, tx, sys)
					return nil
				})
				var before uint64
				view(t, s, "sess", func(tx store.ReadTx) error { before = tx.LastSeq(); return nil })
				err := s.Update(ctx, "sess", func(tx store.Tx) error {
					err := tc.run(t, tx, actor)
					if err != nil {
						// Nothing but the test's own inserts may precede
						// the failure.
						rels, rerr := tx.Relationships(store.RelationshipFilter{Type: domain.RelSupersedes})
						if rerr != nil {
							return rerr
						}
						for _, r := range rels {
							if tx.Allocated(r.Seq) {
								t.Errorf("SUPERSEDES %s->%s written before failure", r.FromID, r.ToID)
							}
						}
					}
					return err
				})
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				view(t, s, "sess", func(tx store.ReadTx) error {
					if tx.LastSeq() != before {
						t.Errorf("LastSeq = %d, want %d (rolled back)", tx.LastSeq(), before)
					}
					return nil
				})
				cur := currentSet(t, s, "sess", "a1", "b1")
				if !cur["a1"] || !cur["b1"] {
					t.Errorf("prior snapshot disturbed: %v", cur)
				}
			})
		})
	}
}
