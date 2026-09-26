package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
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
