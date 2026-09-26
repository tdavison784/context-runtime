package storetest

import (
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// NewUnresolvedReference returns a valid unresolved reference declared by
// item itemID in span 0 of occurrence occurrenceID, private to agent
// "agent" (PrivateBoundary) at USER authority.
func NewUnresolvedReference(sess, occurrenceID string, ordinal int, itemID, locatorKey string, seq uint64) domain.UnresolvedReference {
	return domain.UnresolvedReference{
		ID: domain.UnresolvedReferenceID(sess, occurrenceID, ordinal), SessionID: sess, OccurrenceID: occurrenceID,
		Ordinal: ordinal, ItemID: itemID, LocatorKey: locatorKey, RuleVersion: domain.LocatorRuleVersion,
		Access: PrivateBoundary(sess), Authority: domain.AuthorityUser, Seq: seq,
	}
}

// testUnresolvedReferences checks that unresolved references persist with
// their ownership context (M5, R2) and are found by exact locator key and
// rule version, in (Seq, ID) order, with a bounded read.
func testUnresolvedReferences(t *testing.T, s store.Store) {
	occ := domain.CallerOccurrenceID(sessA, "evt-1")
	var r0, r1, r2 domain.UnresolvedReference
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem(sessA, "ref-item", tx.NextSeq(), "## References\n- go.mod")))
		seq := tx.NextSeq()
		r1 = NewUnresolvedReference(sessA, occ, 1, "ref-item", "loc:a/go.mod\xff", seq)
		r0 = NewUnresolvedReference(sessA, occ, 0, "ref-item", "loc:a/go.mod\xff", seq)
		r2 = NewUnresolvedReference(sessA, occ, 2, "ref-item", "loc:b/go.mod", seq)
		for _, r := range []domain.UnresolvedReference{r1, r0, r2} {
			noErr(t, tx.InsertUnresolvedReference(r))
		}
		got, err := tx.UnresolvedReference(r1.ID)
		noErr(t, err)
		assertEqual(t, "reference inside Update", got, r1)
		return nil
	})
	var later domain.UnresolvedReference
	update(t, s, sessA, func(tx store.Tx) error {
		later = NewUnresolvedReference(sessA, domain.CallerOccurrenceID(sessA, "evt-0"), 0, "ref-item", "loc:a/go.mod\xff", tx.NextSeq())
		return tx.InsertUnresolvedReference(later)
	})
	first, second := r0, r1
	if second.ID < first.ID {
		first, second = second, first
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := tx.UnresolvedReference(r2.ID)
		noErr(t, err)
		assertEqual(t, "reference", got, r2)
		_, err = tx.UnresolvedReference("missing")
		wantErr(t, err, domain.ErrNotFound)

		refs := visibleRefs(t, tx, sessA, "loc:a/go.mod\xff")
		assertEqual(t, "by locator key", refs, []domain.UnresolvedReference{first, second, later})
		// Keys are exact bytes: the lossy spelling matches nothing.
		assertEqual(t, "lossy key", visibleRefs(t, tx, sessA, "loc:a/go.mod\ufffd"), []domain.UnresolvedReference{})
		assertEqual(t, "other key", visibleRefs(t, tx, sessA, "loc:b/go.mod"), []domain.UnresolvedReference{r2})
		return nil
	})
	// A reference under another rule version is stored but never matched.
	update(t, s, sessA, func(tx store.Tx) error {
		old := NewUnresolvedReference(sessA, domain.CallerOccurrenceID(sessA, "evt-9"), 0, "ref-item", "loc:a/go.mod\xff", tx.NextSeq())
		old.RuleVersion = "locator/v0"
		return tx.InsertUnresolvedReference(old)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		assertEqual(t, "after an old-rule reference", visibleRefs(t, tx, sessA, "loc:a/go.mod\xff"), []domain.UnresolvedReference{first, second, later})
		return nil
	})
	view(t, s, sessB, func(tx store.ReadTx) error {
		_, err := tx.UnresolvedReference(r0.ID)
		wantErr(t, err, domain.ErrNotFound)
		assertEqual(t, "another session", visibleRefs(t, tx, sessB, "loc:a/go.mod\xff"), []domain.UnresolvedReference{})
		return nil
	})
}

// visibleRefs returns every reference with key that a task-"task",
// agent-"agent" principal of sess can see, paging through them.
func visibleRefs(t *testing.T, tx store.ReadTx, sess, key string) []domain.UnresolvedReference {
	t.Helper()
	out := []domain.UnresolvedReference{}
	f := store.VisibleReferenceFilter{Viewer: NewPrincipal(sess, domain.AuthorityUser), LocatorKey: key, Page: store.Page{Limit: 2}}
	for {
		refs, more, next, err := tx.VisibleReferences(f)
		noErr(t, err)
		out = append(out, refs...)
		if !more {
			return out
		}
		f.Page.After = next
	}
}

func testUnresolvedReferenceInsertRules(t *testing.T, s store.Store) {
	occ := domain.CallerOccurrenceID(sessA, "evt-1")
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem(sessA, "ref-item", tx.NextSeq(), "refs")))
		return tx.InsertUnresolvedReference(NewUnresolvedReference(sessA, occ, 0, "ref-item", "k", tx.NextSeq()))
	})
	cases := []struct {
		name   string
		mutate func(r *domain.UnresolvedReference)
		want   error
	}{
		{"valid", func(*domain.UnresolvedReference) {}, nil},
		{"seq not allocated", func(r *domain.UnresolvedReference) { r.Seq = 1 }, domain.ErrInvalidRecord},
		{"declaring item missing", func(r *domain.UnresolvedReference) { r.ItemID = "missing" }, domain.ErrInvalidRecord},
		{"invalid record", func(r *domain.UnresolvedReference) { r.RuleVersion = "" }, domain.ErrInvalidRecord},
		{"agent authority", func(r *domain.UnresolvedReference) { r.Authority = domain.AuthorityAgent }, domain.ErrInvalidRecord},
		{"foreign session", func(r *domain.UnresolvedReference) {
			*r = NewUnresolvedReference(sessB, domain.CallerOccurrenceID(sessB, "evt-1"), 1, "ref-item", "k", r.Seq)
		}, domain.ErrInvalidRecord},
		{"ID reused", func(r *domain.UnresolvedReference) {
			*r = NewUnresolvedReference(sessA, occ, 0, "ref-item", "k2", r.Seq)
		}, domain.ErrImmutable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := s.Update(ctx, sessA, func(tx store.Tx) error {
				r := NewUnresolvedReference(sessA, occ, 1, "ref-item", "k", tx.NextSeq())
				c.mutate(&r)
				err := tx.InsertUnresolvedReference(r)
				if c.want == nil {
					noErr(t, err)
				} else {
					wantErr(t, err, c.want)
				}
				return errRollback
			})
			wantErr(t, err, errRollback)
		})
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		if refs := visibleRefs(t, tx, sessA, "k"); len(refs) != 1 {
			t.Errorf("references after rolled-back inserts = %d, want 1", len(refs))
		}
		return nil
	})
}

func testUnresolvedReferencesAcrossRestart(t *testing.T, open Opener) {
	s := openDurable(t, open)
	var want domain.UnresolvedReference
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem(sessA, "ref-item", tx.NextSeq(), "refs")))
		want = NewUnresolvedReference(sessA, domain.NewAnonymousOccurrenceID(anonymousIDs), 0, "ref-item", "loc:a/\xfe", tx.NextSeq())
		return tx.InsertUnresolvedReference(want)
	})
	s = reopen(t, s, open)
	view(t, s, sessA, func(tx store.ReadTx) error {
		assertEqual(t, "reference after restart", visibleRefs(t, tx, sessA, want.LocatorKey), []domain.UnresolvedReference{want})
		return nil
	})
}
