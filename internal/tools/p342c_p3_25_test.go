package tools

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// P3-25 (ADR 8 line 1362). The cited TestKeyedWritesDeduplicateReplaceAnd-
// StayPerAgent (keyed_test.go:39) runs on memory.New() only — toolFixture
// builds the world on memory. These are the SQLite halves of its dedup
// semantics through the same real service path, on memory and sqlitetest:
//
//   - an exact restatement (same key, same parts, same support set) files a
//     DUPLICATE occurrence that is noncurrent and never replaces the
//     canonical one;
//   - a support change with byte-identical text is NOT a duplicate: it
//     files a new current version that supersedes the old one;
//   - the support set is exactly the request's citations — the request's
//     own provenance edge never counts as evidence;
//   - another agent's same key is an independent current occurrence.
func TestP3_25_ExactDuplicateVersusSupportChangeOnBothStores(t *testing.T) {
	p24Stores(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		i := seedToolFixture(t, st)
		update(t, st, func(tx store.Tx) error {
			ev := storetest.NewItem("s", "evidence", tx.NextSeq(), "observed postgres")
			ev.Kind = domain.KindEvidence
			return tx.InsertItem(ev)
		})

		first := remember(t, st, s, i, keyed("r1", "db", "postgres"))
		update(t, st, func(tx store.Tx) error {
			it, err := tx.Item(first.ItemID)
			if err != nil || it.Namespace != domain.NamespaceAgentKey || it.DirectiveID != "agent.db" {
				t.Fatalf("keyed item: %+v, %v", it, err)
			}
			return nil
		})
		if first.CanonicalItemID != first.ItemID || first.SupersededItemID != "" || !current(t, st, first.ItemID) {
			t.Fatalf("first filing: %+v", first)
		}

		// Identical restatement: a noncurrent duplicate, never a replacement.
		dup := remember(t, st, s, addToolCall(t, st, i, "t2"), keyed("r2", "db", "postgres"))
		if !dup.Duplicate || dup.CanonicalItemID != first.ItemID || current(t, st, dup.ItemID) || !current(t, st, first.ItemID) {
			t.Fatalf("duplicate: %+v", dup)
		}

		// Changed support is a new version even with identical text.
		supported := remember(t, st, s, addToolCall(t, st, i, "t3"), keyed("r3", "db", "postgres", "evidence"))
		if supported.Duplicate || supported.SupersededItemID != first.ItemID || current(t, st, first.ItemID) || !current(t, st, supported.ItemID) {
			t.Fatalf("support change: %+v", supported)
		}
		update(t, st, func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			a, err := sem.CreationDeclaration(first.ItemID)
			if err != nil {
				return err
			}
			b, err := sem.CreationDeclaration(supported.ItemID)
			if err != nil {
				return err
			}
			if len(a.AcceptedSemantics.SupportIDs) != 0 || len(b.AcceptedSemantics.SupportIDs) != 1 || b.AcceptedSemantics.SupportIDs[0] != "evidence" {
				t.Fatalf("support: request provenance counted as evidence: %+v / %+v", a.AcceptedSemantics.SupportIDs, b.AcceptedSemantics.SupportIDs)
			}
			return nil
		})

		// Another agent's same key is independent.
		b := seedAgentInvocation(t, st, "b")
		other := remember(t, st, s, b, keyed("rb", "db", "postgres"))
		if other.Duplicate || other.SupersededItemID != "" || !current(t, st, other.ItemID) || !current(t, st, supported.ItemID) {
			t.Fatalf("per-agent isolation: %+v", other)
		}
	})
}
