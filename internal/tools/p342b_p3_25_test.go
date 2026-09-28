package tools

import (
	"errors"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_25_CrossSessionEvidenceIsRefusedUniformlyAndAtomically: evidence
// of another session never supports a keyed write, and the refusal is
// atomic and information-blind (P3-25 — the cited
// TestKeyedWriteCitationFailuresAreUniformAndAtomic probes missing and
// other-agent items on the memory backend only, never another session's,
// and never SQLite). A genuine evidence item of session "other" is seeded
// through that session's own transaction; citing it from session "s" —
// alone, or mixed with this session's valid evidence — is the same closed
// not-found the missing-item case gets: the writer cannot distinguish
// "absent" from "someone else's", and nothing of the attempt commits (no
// sequence, no item, no receipt). The store layer underneath refuses to
// even hold a cross-session item inside this session's transaction. The
// identical request citing only this session's evidence succeeds, so the
// refusals blocked nothing but the cross-session citation. Both stores.
func TestP3_25_CrossSessionEvidenceIsRefusedUniformlyAndAtomically(t *testing.T) {
	p24Stores(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		i := seedToolFixture(t, st)

		// This session's public evidence.
		update(t, st, func(tx store.Tx) error {
			pub := storetest.NewItem("s", "evidence", tx.NextSeq(), "public evidence")
			pub.Kind = domain.KindEvidence
			return tx.InsertItem(pub)
		})
		// A genuine evidence item of another session, seeded through that
		// session's own transaction.
		if err := st.Update(testContext, "other", func(tx store.Tx) error {
			foreign := storetest.NewItem("other", "foreign-evidence", tx.NextSeq(), "another session's evidence")
			foreign.Kind = domain.KindEvidence
			return tx.InsertItem(foreign)
		}); err != nil {
			t.Fatalf("seed foreign session: %v", err)
		}
		// The store layer: this session's transaction will not even hold a
		// record claiming another session.
		update(t, st, func(tx store.Tx) error {
			smuggled := storetest.NewItem("other", "smuggled", tx.NextSeq(), "claims another session")
			smuggled.Kind = domain.KindEvidence
			if err := tx.InsertItem(smuggled); err == nil {
				return fmt.Errorf("the store held a cross-session item")
			}
			return nil
		})

		var texts []string
		for _, evidence := range [][]string{
			{"foreign-evidence"},
			{"evidence", "foreign-evidence"},
			{"foreign-evidence", "evidence"},
		} {
			var before uint64
			err := FixedError(st.Update(testContext, "s", func(tx store.Tx) error {
				before = tx.LastSeq()
				_, err := s.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, keyed("p25-x", "db", "postgres", evidence...)}, tx.NextSeq())
				return err
			}))
			texts = append(texts, err.Error())
			update(t, st, func(tx store.Tx) error {
				if tx.LastSeq() != before {
					t.Fatal("cross-session citation failure wrote state")
				}
				if _, err := tx.Item("p25-x"); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("cross-session citation wrote an item: %v", err)
				}
				sem, _ := store.Semantic(tx)
				if _, err := sem.MutationReceipt(domain.MutationTool, "p25-x"); !errors.Is(err, domain.ErrNotFound) {
					t.Fatalf("cross-session citation persisted a receipt: %v", err)
				}
				return nil
			})
		}
		for _, text := range texts {
			if text != domain.ToolErrorNotFound.Message() {
				t.Fatalf("cross-session citation errors differ from the missing-item refusal: %q", texts)
			}
		}

		// The same request shape citing only this session's evidence lands.
		r := remember(t, st, s, i, keyed("p25-ok", "db", "postgres", "evidence"))
		if r.ItemID == "" {
			t.Fatalf("in-session citation refused: %+v", r)
		}
	})
}
