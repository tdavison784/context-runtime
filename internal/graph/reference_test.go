package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// referenceItem returns a TASK-scoped reference item.
func referenceItem(sess, id string, seq uint64) domain.ContextItem {
	it := taskItem(sess, id, seq, domain.AuthorityUser)
	it.Kind = domain.KindReference
	return it
}

// TestLinkReference_M5: a REFERENCES edge runs from a reference item to a
// target both endpoints of which the actor can access, and only when every
// principal who can see the reference can also see the target, so an edge
// never discloses narrower evidence through a broader reference (M5, R2).
func TestLinkReference_M5(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-ref"
		user := principal(sess, domain.AuthorityUser)
		update(t, s, sess, func(tx store.Tx) error {
			mustInsert(t, tx,
				referenceItem(sess, "ref", tx.NextSeq()),
				taskItem(sess, "doc", tx.NextSeq(), domain.AuthorityUser),
				agentScopedItem(sess, "private-doc", tx.NextSeq(), "agent"),
				taskItem(sess, "not-a-ref", tx.NextSeq(), domain.AuthorityUser))
			return nil
		})
		update(t, s, sess, func(tx store.Tx) error {
			rel, err := LinkReference(tx, user, "ref", "doc", "evt", "reference-locator/v1")
			if err != nil {
				return err
			}
			if rel.Type != domain.RelReferences || rel.FromID != "ref" || rel.ToID != "doc" || rel.RuleVersion != "reference-locator/v1" {
				t.Errorf("relationship = %+v", rel)
			}
			return nil
		})
		cases := map[string]struct {
			from, to string
			want     error
		}{
			"NarrowerTarget": {"ref", "private-doc", domain.ErrInvalidAuthorityPromotion},
			"NotAReference":  {"not-a-ref", "doc", domain.ErrInvalidRecord},
			"MissingTarget":  {"ref", "missing", domain.ErrNotFound},
		}
		for name, c := range cases {
			err := s.Update(ctx, sess, func(tx store.Tx) error {
				_, err := LinkReference(tx, user, c.from, c.to, "evt2", "reference-locator/v1")
				return err
			})
			if !errors.Is(err, c.want) {
				t.Errorf("%s: err = %v, want %v", name, err, c.want)
			}
		}
		// An actor that cannot see the target gets the bare not-found.
		other := principalWithAgent(sess, domain.AuthorityUser, "other")
		err := s.Update(ctx, sess, func(tx store.Tx) error {
			_, err := LinkReference(tx, other, "ref", "private-doc", "evt3", "reference-locator/v1")
			return err
		})
		if err != domain.ErrNotFound {
			t.Errorf("hidden target: err = %v, want bare ErrNotFound", err)
		}
	})
}
