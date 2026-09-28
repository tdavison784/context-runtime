package graph

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestP3_12_DuplicateCreatesNoObligations closes the P3-42 table row
// "duplicates do not create obligations" (ADR 8 :1117). TestD13_
// DuplicateLeavesObligations proves the canonical's obligation survives; the
// MISSING half is that classification as a duplicate creates NO obligation
// anywhere: nothing bound to the duplicate item, no extra version in the
// task, and the canonical's obligation still bound to the canonical alone.
func TestP3_12_DuplicateCreatesNoObligations(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-p3-12-dup"
		actor := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			pinWithObligations(t, tx, actor, "p1", "tests", "o1")
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			dup := newDirective(sess, "p1-dup", "tests", tx.NextSeq(), "All tests must pass p1")
			mustCreate(t, tx, dup)
			_, err := LinkDuplicate(tx, actor, dup.ID, "p1", "evt-dup", "", "")
			return err
		})
		view(t, s, sess, func(tx store.ReadTx) error {
			// The duplicate itself: no obligation version is created for it.
			dupObligations, err := tx.ObligationsBySource("p1-dup", 10)
			if err != nil {
				return err
			}
			if len(dupObligations) != 0 {
				t.Errorf("duplicate created %d obligations, want none: %+v", len(dupObligations), dupObligations)
			}
			// Task-wide: classification added no obligation under any key.
			all, err := tx.Obligations("task")
			if err != nil {
				return err
			}
			if len(all) != 1 || all[0].ObligationID != "o1" {
				t.Errorf("task obligations after duplicate = %d (%s), want exactly the canonical o1", len(all), ids(all))
			}
			// Positive contrast: the canonical keeps its one obligation,
			// still current and still bound to the canonical item alone.
			canonical, err := tx.ObligationsBySource("p1", 10)
			if err != nil {
				return err
			}
			if len(canonical) != 1 || canonical[0].ObligationID != "o1" {
				t.Fatalf("canonical obligations = %v, want exactly o1", ids(canonical))
			}
			o1 := canonical[0]
			if !o1.Current || o1.Status != domain.ObligationUnresolved || o1.SourceItemID != "p1" {
				t.Errorf("o1 = current %v, %s, source %s; want current UNRESOLVED still bound to p1", o1.Current, o1.Status, o1.SourceItemID)
			}
			return nil
		})
	})
}

func ids(obs []domain.ObligationVersion) []string {
	var out []string
	for _, o := range obs {
		out = append(out, o.ObligationID)
	}
	return out
}
