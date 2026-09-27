package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// fileLegacyGoal files g as a pre-upgrade version: current, but with no
// creation declaration (or with an explicitly unknown one), as Phase 2 data
// decodes after migration.
func fileLegacyGoal(t *testing.T, tx store.Tx, actor domain.Principal, id, dirID string, unknown bool) domain.ContextItem {
	t.Helper()
	g := goalLike(actor.SessionID, id, dirID, tx.NextSeq(), "Ship it")
	mustInsert(t, tx, g)
	if unknown {
		sem, err := store.Semantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		d := domain.CreationDeclaration{SemanticMeta: domain.SemanticMeta{ID: "decl-" + id, SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()}, ItemID: id, PolicyVersion: "legacy/unknown"}
		if err := sem.InsertCreationDeclaration(d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ReplaceDirective(tx, actor, g.TaskID, dirID, g.ID, "evt-"+id); err != nil {
		t.Fatalf("file legacy %s: %v", id, err)
	}
	return g
}

// TestIdenticalRestatementOfUnknownIdentityFailsClosed is SPEC-1.3 (P3-4,
// C-1, P3-41): an identical restatement of a pre-upgrade directive whose
// creation identity is unknown must never fall through to replacement. The
// comparison reports ErrUnknownDeclaration instead of "not a duplicate", and
// ReplaceDirective refuses to rebind it, leaving the old version current and
// its SATISFIED obligation unretired. A changed restatement still replaces.
func TestIdenticalRestatementOfUnknownIdentityFailsClosed(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := map[bool]string{false: "undeclared", true: "unknown-declaration"}[unknown]
		t.Run(name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, s store.Store) {
				const sess, dirID = "sess-spec13", "ship"
				actor := principal(sess, domain.AuthoritySystem)
				var legacy domain.ContextItem
				update(t, s, sess, func(tx store.Tx) error {
					legacy = fileLegacyGoal(t, tx, actor, "g1", dirID, unknown)
					o := storetest.NewObligation(sess, "o1", 1, tx.NextSeq(), legacy.ID)
					return tx.InsertObligationVersion(o)
				})
				err := s.Update(ctx, sess, func(tx store.Tx) error {
					fresh := goalLike(sess, "g2", dirID, tx.NextSeq(), "Ship it")
					mustCreate(t, tx, fresh)
					if same, err := SameDirective(tx, fresh, "", legacy); same || !errors.Is(err, ErrUnknownDeclaration) {
						t.Errorf("SameDirective = %v, %v; want ErrUnknownDeclaration, never a silent non-duplicate", same, err)
					}
					_, err := ReplaceDirective(tx, actor, fresh.TaskID, dirID, fresh.ID, "evt-g2")
					return err
				})
				if !errors.Is(err, ErrUnknownDeclaration) || !errors.Is(err, domain.ErrUnsupportedSchema) {
					t.Fatalf("identical restatement: err = %v; want ErrUnknownDeclaration", err)
				}
				view(t, s, sess, func(tx store.ReadTx) error {
					if ok, err := IsCurrent(tx, legacy.ID); err != nil || !ok {
						t.Errorf("legacy version replaced by an identical restatement: %v, %v", ok, err)
					}
					if o := obligation(t, tx, "o1"); !o.Current {
						t.Errorf("legacy obligation retired by an identical restatement")
					}
					return nil
				})
				update(t, s, sess, func(tx store.Tx) error {
					changed := goalLike(sess, "g3", dirID, tx.NextSeq(), "Ship it by Friday")
					mustCreate(t, tx, changed)
					if same, err := SameDirective(tx, changed, "", legacy); same || err != nil {
						t.Errorf("changed restatement: SameDirective = %v, %v; want false, nil", same, err)
					}
					_, err := ReplaceDirective(tx, actor, changed.TaskID, dirID, changed.ID, "evt-g3")
					return err
				})
			})
		})
	}
}

// TestIdenticalSnapshotOverUnknownIdentityIsDuplicate is the approved G5
// residual rule (SPEC-2.9): Working snapshot members carry no attributes, so
// an identical snapshot over pre-upgrade members with unknown identity is a
// DUPLICATE_OF restatement: nothing is replaced, re-filed or rebound, and it
// never errors. A changed snapshot still supersedes normally.
func TestIdenticalSnapshotOverUnknownIdentityIsDuplicate(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-spec29-snapshot"
		actor := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			a, b := member(sess, "a0", tx.NextSeq(), "A"), member(sess, "b0", tx.NextSeq(), "B")
			mustInsert(t, tx, a, b)
			mustFile(t, tx, a, b)
			return nil
		})
		res := snapshot(t, s, actor, "evt-same", m(sess, "a1", "A"), m(sess, "b1", "B"))
		if got, want := edges(res.Duplicates), []string{"a1->a0", "b1->b0"}; len(res.Supersedes) != 0 || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
			t.Fatalf("identical snapshot: supersedes %v duplicates %v; want duplicates %v", edges(res.Supersedes), got, want)
		}
		cur := currentSet(t, s, sess, "a0", "b0", "a1", "b1")
		if !cur["a0"] || !cur["b0"] || cur["a1"] || cur["b1"] {
			t.Fatalf("identical snapshot re-filed or replaced legacy members: %v", cur)
		}
		res = snapshot(t, s, actor, "evt-changed", m(sess, "a2", "A"), m(sess, "b2", "B changed"))
		if len(res.Duplicates) != 0 || len(res.Supersedes) != 2 {
			t.Fatalf("changed snapshot: supersedes %v duplicates %v", edges(res.Supersedes), edges(res.Duplicates))
		}
	})
}

// TestIdenticalAgentKeyOverUnknownIdentityIsDuplicate: a tool-written agent
// key is attribute-free, so an identical write over the owner's pre-upgrade
// key with unknown identity links DUPLICATE_OF and never replaces it (the
// replacement backstop refuses); a write that cites new support is a real
// new version and replaces it (SPEC-2.10, C-19).
func TestIdenticalAgentKeyOverUnknownIdentityIsDuplicate(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-spec210"
		agent := principalWithAgent(sess, domain.AuthorityAgent, "agent")
		var legacy domain.ContextItem
		update(t, s, sess, func(tx store.Tx) error {
			legacy = legacyAgentKey(sess, "old", "status", tx.NextSeq())
			setText(&legacy, "status: idle")
			mustInsert(t, tx, legacy)
			mustFile(t, tx, legacy)
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			fresh := agentDirective(sess, "same", legacy.DirectiveID, tx.NextSeq())
			setText(&fresh, "status: idle")
			mustCreate(t, tx, fresh)
			if same, err := SameDirective(tx, fresh, "", legacy); !same || err != nil {
				t.Errorf("SameDirective = %v, %v; want an attribute-free duplicate", same, err)
			}
			_, err := LinkDuplicate(tx, agent, fresh.ID, legacy.ID, "evt-same", "", "")
			return err
		})
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			fresh := agentDirective(sess, "same-2", legacy.DirectiveID, tx.NextSeq())
			setText(&fresh, "status: idle")
			mustCreate(t, tx, fresh)
			_, err := ReplaceDirective(tx, agent, fresh.TaskID, fresh.DirectiveID, fresh.ID, "evt-replace-same")
			return err
		})
		if !errors.Is(err, ErrUnknownDeclaration) {
			t.Fatalf("identical agent-key replacement over unknown identity: err = %v; want refused", err)
		}
		update(t, s, sess, func(tx store.Tx) error {
			evidence := taskItem(sess, "log", tx.NextSeq(), domain.AuthorityUser)
			evidence.Kind = domain.KindEvidence
			mustInsert(t, tx, evidence)
			cited := agentDirective(sess, "cited", legacy.DirectiveID, tx.NextSeq())
			setText(&cited, "status: idle")
			mustInsert(t, tx, cited)
			if _, err := DeclareCreation(tx, cited, CreationAcceptance{PolicyVersion: testDeclarationPolicy, SupportIDs: []string{evidence.ID}}); err != nil {
				return err
			}
			if same, err := SameDirective(tx, cited, "", legacy); same || err != nil {
				t.Errorf("SameDirective with new support = %v, %v; want a distinct version", same, err)
			}
			prev, err := ReplaceDirective(tx, agent, cited.TaskID, cited.DirectiveID, cited.ID, "evt-cited")
			if err == nil && prev != legacy.ID {
				t.Errorf("replaced %q, want the legacy key", prev)
			}
			return err
		})
	})
}
