package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// pinWithObligations files pin (dirID) and inserts one UNRESOLVED
// obligation version per ID bound to it.
func pinWithObligations(t *testing.T, tx store.Tx, actor domain.Principal, pinID, dirID string, obligationIDs ...string) {
	t.Helper()
	pin := storetest.NewDirective(actor.SessionID, pinID, dirID, tx.NextSeq(), "All tests must pass "+pinID)
	mustInsert(t, tx, pin)
	if _, err := ReplaceDirective(tx, actor, "task", dirID, pin.ID, "evt-"+pinID); err != nil {
		t.Fatalf("file %s: %v", pinID, err)
	}
	for _, id := range obligationIDs {
		if err := tx.InsertObligationVersion(storetest.NewObligation(actor.SessionID, id, 1, tx.NextSeq(), pin.ID)); err != nil {
			t.Fatalf("InsertObligationVersion(%s): %v", id, err)
		}
	}
}

func obligation(t *testing.T, tx store.ReadTx, id string) domain.ObligationVersion {
	t.Helper()
	o, err := tx.Obligation(id)
	if err != nil {
		t.Fatalf("Obligation(%s): %v", id, err)
	}
	return o
}

// TestD13_ReplacementRetiresBoundObligations is D13/FR-OBL-006: replacing a
// source directive retires, in the same transaction, every current
// obligation version bound to the retired source, including when the
// replacement declares no obligation, preserving status, evidence, and
// transition history, with an audit record per retirement. Obligations of
// other sources are untouched, and no replacement version is invented.
func TestD13_ReplacementRetiresBoundObligations(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d13"
		actor := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			pinWithObligations(t, tx, actor, "p1", "tests", "o1", "o2")
			pinWithObligations(t, tx, actor, "q1", "other", "o3")
			tr := storetest.NewTransition(sess, "tr-1", "o2", 1, tx.NextSeq(), domain.ObligationUnresolved, domain.ObligationSatisfied)
			_, err := tx.AppendObligationTransition(tr, 1)
			return err
		})
		var replaceSeq uint64
		update(t, s, sess, func(tx store.Tx) error {
			p2 := storetest.NewDirective(sess, "p2", "tests", tx.NextSeq(), "Replacement without obligation")
			mustInsert(t, tx, p2)
			_, err := ReplaceDirective(tx, actor, "task", "tests", p2.ID, "evt-p2")
			replaceSeq = tx.LastSeq()
			return err
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			for _, id := range []string{"o1", "o2"} {
				o := obligation(t, tx, id)
				if o.Current || o.RetiredSeq == 0 || o.RetiredSeq > replaceSeq || o.Version != 1 {
					t.Errorf("%s = current %v, retired %d, version %d; want retired v1 in the replacement transaction", id, o.Current, o.RetiredSeq, o.Version)
				}
			}
			if o := obligation(t, tx, "o1"); o.Status != domain.ObligationUnresolved {
				t.Errorf("o1 status = %s, want UNRESOLVED preserved", o.Status)
			}
			if o := obligation(t, tx, "o2"); o.Status != domain.ObligationSatisfied || len(o.EvidenceIDs) == 0 {
				t.Errorf("o2 = %s %v, want SATISFIED with its evidence preserved", o.Status, o.EvidenceIDs)
			}
			if trs, err := tx.ObligationTransitions("o2"); err != nil || len(trs) != 1 {
				t.Errorf("o2 transitions = %d, %v; want history preserved", len(trs), err)
			}
			if o := obligation(t, tx, "o3"); !o.Current {
				t.Errorf("o3 of another source was retired")
			}
			evs, err := tx.LifecycleEvents(store.LifecycleFilter{TargetKind: domain.TargetObligation})
			if err != nil {
				return err
			}
			retired := map[string]int{}
			for _, e := range evs {
				if e.Action == "retired" && e.To == "p2" && e.Actor == actor && e.EventID == "evt-p2" {
					retired[e.TargetID]++
				}
			}
			if retired["o1"] != 1 || retired["o2"] != 1 || len(retired) != 2 {
				t.Errorf("retirement audits = %v, want one each for o1 and o2", retired)
			}
			return nil
		})
	})
}

// TestD13_SnapshotIDReplacementRetiresObligations: a Working member that
// replaces a Pinned directive by ID changes section away from Pinned; the
// pin's obligation is retired in the same transaction too.
func TestD13_SnapshotIDReplacementRetiresObligations(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d13-snapshot"
		actor := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			pinWithObligations(t, tx, actor, "p1", "x", "o1")
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			w := member(sess, "wx", tx.NextSeq(), "working x")
			w.DirectiveID = "x"
			mustInsert(t, tx, w)
			_, err := SupersedeSnapshot(tx, actor, []string{"wx"}, "task", "evt-w")
			return err
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			if obligation(t, tx, "o1").Current {
				t.Errorf("obligation of the replaced pin is still current")
			}
			return nil
		})
	})
}

// TestD13_DuplicateLeavesObligations: a duplicate replaces nothing, so the
// canonical source's obligation stays current (D13).
func TestD13_DuplicateLeavesObligations(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d13-dup"
		actor := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			pinWithObligations(t, tx, actor, "p1", "tests", "o1")
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			dup := storetest.NewDirective(sess, "p1-dup", "tests", tx.NextSeq(), "All tests must pass p1")
			mustInsert(t, tx, dup)
			_, err := LinkDuplicate(tx, actor, dup.ID, "p1", "evt-dup", "", "")
			return err
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			if !obligation(t, tx, "o1").Current {
				t.Errorf("a duplicate retired the canonical source's obligation")
			}
			return nil
		})
	})
}

// TestD13_UnauthorizedIndirectRetirementAbortsReplacement: retiring a bound
// obligation is an indirect effect that must itself be authorized
// (FR-AUTH-001). If the actor lacks authority over the obligation's source
// authority, the whole replacement fails and nothing changes.
func TestD13_UnauthorizedIndirectRetirementAbortsReplacement(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-d13-authz"
		actor := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			pinWithObligations(t, tx, actor, "p1", "tests")
			o := storetest.NewObligation(sess, "o-sys", 1, tx.NextSeq(), "p1")
			o.SourceAuthority = domain.AuthoritySystem
			return tx.InsertObligationVersion(o)
		})
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			p2 := storetest.NewDirective(sess, "p2", "tests", tx.NextSeq(), "Replacement")
			mustInsert(t, tx, p2)
			_, err := ReplaceDirective(tx, actor, "task", "tests", p2.ID, "evt-p2")
			return err
		})
		if !errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
			t.Fatalf("err = %v, want ErrInvalidAuthorityPromotion", err)
		}
		view(t, s, sess, func(tx store.ReadTx) error {
			if !obligation(t, tx, "o-sys").Current {
				t.Errorf("obligation retired by an aborted replacement")
			}
			if ok, _ := IsCurrent(tx, "p1"); !ok {
				t.Errorf("p1 retired by an aborted replacement")
			}
			return nil
		})
	})
}
