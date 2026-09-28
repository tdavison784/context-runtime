package tools

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
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

// TestP3_25_FreshImmutableIDsAndOneCurrentVersion: a keyed write never
// recycles an ID and never edits history (P3-25 — the cited
// TestAgentKeyAuthorityUsesNamespaceAndExactOwner asserts namespace/owner
// authorization only, nothing of IDs or versions; the adjacent
// TestKeyedWritesDeduplicateReplaceAndStayPerAgent checks pairwise
// currentness after each write but never that IDs are fresh per invocation,
// that earlier records stay byte-identical, or that the key holds EXACTLY
// one current version). The full lifecycle of one key: a first filing, the
// exact retry of its request (the same record, replayed), a new invocation
// restating it (a fresh ID, a noncurrent duplicate), a new invocation with
// new text (a fresh ID, a replacement), and a duplicate of THAT current (a
// fresh ID again). After every write the key has exactly one current
// version — counted both through IsCurrent over every filed ID and through
// the CurrentVersions listing — and every earlier record reads back
// byte-identical to the moment it was filed: replacement writes a new
// record, it never rewrites the old one.
func TestP3_25_FreshImmutableIDsAndOneCurrentVersion(t *testing.T) {
	p24Stores(t, func(t *testing.T, st store.Store) {
		s := testService(t)
		i := seedToolFixture(t, st)

		snapshot := func(id string) domain.ContextItem {
			var it domain.ContextItem
			if err := st.View(testContext, "s", func(tx store.ReadTx) error {
				var err error
				it, err = tx.Item(id)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return it
		}
		// exactlyOneCurrent: among ids exactly want is current, and the
		// key's current-version listing names exactly it.
		exactlyOneCurrent := func(want string, ids ...string) {
			t.Helper()
			if err := st.View(testContext, "s", func(tx store.ReadTx) error {
				n := 0
				for _, id := range ids {
					ok, err := graph.IsCurrent(tx, id)
					if err != nil {
						return err
					}
					if ok {
						n++
						if id != want {
							t.Errorf("current version = %s, want %s", id, want)
						}
					}
				}
				if n != 1 {
					t.Errorf("%d of %d filed IDs are current, want exactly 1", n, len(ids))
				}
				cur, err := graph.CurrentVersions(tx, i.Principal, i.Principal.TaskID, domain.NamespaceAgentKey, domain.AgentKeyID("db"))
				if err != nil || len(cur) != 1 || cur[0].ID != want {
					t.Errorf("CurrentVersions = %d records (%v), want exactly [%s]", len(cur), err, want)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		immutable := func(id string, was domain.ContextItem) {
			t.Helper()
			if now := snapshot(id); !reflect.DeepEqual(now, was) {
				t.Errorf("%s mutated by a later write:\nwas %+v\nnow %+v", id, was, now)
			}
		}

		// The first filing: fresh ID, current, canonical of itself.
		first := remember(t, st, s, i, keyed("r1", "db", "postgres"))
		if first.ItemID == "" || first.Duplicate || first.CanonicalItemID != first.ItemID || first.SupersededItemID != "" {
			t.Fatalf("first filing: %+v", first)
		}
		wasFirst := snapshot(first.ItemID)
		exactlyOneCurrent(first.ItemID, first.ItemID)

		// The exact retry replays the same record: fresh per invocation,
		// stable per request.
		var replay domain.KeyedWriteResult
		update(t, st, func(tx store.Tx) error {
			r, err := s.Remember(tx, dispatcher(i), Request[domain.KeyedWriteIntent]{i, keyed("r1", "db", "postgres")}, tx.NextSeq())
			if err == nil {
				replay = *r.Keyed
			}
			return err
		})
		if replay.ItemID != first.ItemID || replay.Duplicate || replay.CanonicalItemID != first.CanonicalItemID || replay.SupersededItemID != first.SupersededItemID {
			t.Fatalf("exact retry = %+v, want the same record %+v", replay, first)
		}
		exactlyOneCurrent(first.ItemID, first.ItemID)
		immutable(first.ItemID, wasFirst)

		// A new invocation restating the same text: a FRESH ID, a noncurrent
		// duplicate of the untouched canonical.
		dup := remember(t, st, s, addToolCall(t, st, i, "t2"), keyed("r2", "db", "postgres"))
		ids := []string{first.ItemID, dup.ItemID}
		if dup.ItemID == first.ItemID {
			t.Fatalf("restatement recycled %s", first.ItemID)
		}
		if !dup.Duplicate || dup.CanonicalItemID != first.ItemID || dup.SupersededItemID != "" {
			t.Fatalf("duplicate filing: %+v", dup)
		}
		exactlyOneCurrent(first.ItemID, ids...)
		immutable(first.ItemID, wasFirst)
		wasDup := snapshot(dup.ItemID)

		// A new invocation with new text: a FRESH ID that replaces; history
		// stays byte-identical.
		second := remember(t, st, s, addToolCall(t, st, i, "t3"), keyed("r3", "db", "postgres is fast"))
		ids = append(ids, second.ItemID)
		if second.Duplicate || second.ItemID == first.ItemID || second.ItemID == dup.ItemID ||
			second.SupersededItemID != first.ItemID || second.CanonicalItemID != second.ItemID {
			t.Fatalf("replacement: %+v", second)
		}
		exactlyOneCurrent(second.ItemID, ids...)
		immutable(first.ItemID, wasFirst)
		immutable(dup.ItemID, wasDup)
		wasSecond := snapshot(second.ItemID)

		// A duplicate of the NEW current: the chain never forks a second
		// current version and never edits a filed record.
		dup2 := remember(t, st, s, addToolCall(t, st, i, "t4"), keyed("r4", "db", "postgres is fast"))
		ids = append(ids, dup2.ItemID)
		if !dup2.Duplicate || dup2.CanonicalItemID != second.ItemID || dup2.ItemID == dup.ItemID || dup2.ItemID == first.ItemID {
			t.Fatalf("duplicate of the new current: %+v", dup2)
		}
		exactlyOneCurrent(second.ItemID, ids...)
		immutable(first.ItemID, wasFirst)
		immutable(dup.ItemID, wasDup)
		immutable(second.ItemID, wasSecond)

		// The chain's four records are four distinct fresh IDs; none was
		// ever rewritten.
		seen := map[string]bool{}
		for _, id := range ids {
			if id == "" || seen[id] {
				t.Fatalf("IDs not fresh and distinct: %v", ids)
			}
			seen[id] = true
		}
	})
}
