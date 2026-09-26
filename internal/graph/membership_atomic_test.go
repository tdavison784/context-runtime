package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

var errMembershipWrite = errors.New("injected membership write failure")

type membershipFaultTx struct {
	store.Tx
	sem store.SemanticTx
}

func (tx membershipFaultTx) SemanticTransaction() (store.SemanticTx, error) { return tx.sem, nil }

type membershipFaultWriter struct {
	store.SemanticTx
	failAt, writes int
}

func (f *membershipFaultWriter) after(err error) error {
	if err != nil {
		return err
	}
	f.writes++
	if f.writes == f.failAt {
		return errMembershipWrite
	}
	return nil
}

func (f *membershipFaultWriter) InsertLogicalExchange(x domain.LogicalExchange) error {
	return f.after(f.SemanticTx.InsertLogicalExchange(x))
}

func (f *membershipFaultWriter) PutConversationMembership(s domain.ConversationMembershipState, revision uint64) (domain.ConversationMembershipState, error) {
	got, err := f.SemanticTx.PutConversationMembership(s, revision)
	return got, f.after(err)
}

func (f *membershipFaultWriter) InsertMutationReceipt(r domain.MutationReceipt) error {
	return f.after(f.SemanticTx.InsertMutationReceipt(r))
}

func TestMembershipRegistrationPoisonsEveryPartialWrite(t *testing.T) {
	for failAt := 1; failAt <= 3; failAt++ {
		s, service, actor, intent := membershipTestStore(t)
		var before uint64
		err := s.Update(ctx, "s", func(tx store.Tx) error {
			before = tx.LastSeq()
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			fault := &membershipFaultWriter{SemanticTx: sem, failAt: failAt}
			if _, err = service.RegisterExchange(membershipFaultTx{tx, fault}, actor, intent); !errors.Is(err, errMembershipWrite) {
				t.Fatalf("write %d: %v", failAt, err)
			}
			return nil // The application ignores the service failure.
		})
		if !errors.Is(err, errMembershipWrite) {
			t.Fatalf("write %d committed: %v", failAt, err)
		}
		update(t, s, "s", func(tx store.Tx) error {
			if tx.LastSeq() != before {
				t.Fatal("partial semantic sequence committed")
			}
			sem, _ := store.Semantic(tx)
			if _, err := sem.MutationReceipt(domain.MutationMembership, intent.RequestID); !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("receipt survived", err)
			}
			if _, err := sem.ConversationMembership(domain.ConversationIDFor(actor.TaskID, actor.AgentID)); !errors.Is(err, domain.ErrNotFound) {
				t.Fatal("membership survived", err)
			}
			result, err := service.RegisterExchange(tx, actor, intent)
			if err != nil {
				return err
			}
			x, err := sem.LogicalExchange(result.IDs[0])
			if err != nil || x.Ordinal != 1 {
				t.Fatalf("retry consumed ordinal: %+v, %v", x, err)
			}
			return nil
		})
	}
}

func TestMembershipRegistrationRejectsUntrustedStaleAndPrivateIntents(t *testing.T) {
	for _, invalid := range []string{"authority", "owner", "workflow", "turn", "revision"} {
		t.Run(invalid, func(t *testing.T) {
			s, service, actor, intent := membershipTestStore(t)
			want := domain.ErrInvalidAuthorityPromotion
			switch invalid {
			case "authority":
				actor.Authority = domain.AuthorityAgent
			case "owner":
				actor.AgentID = "other"
			case "workflow":
				actor.WorkflowID, intent.Principal.WorkflowID, want = "other", "other", domain.ErrNotFound
			case "turn":
				intent.Turn, want = 2, domain.ErrInvalidTransition
			case "revision":
				intent.ExpectedMembershipRevision, want = 1, domain.ErrVersionConflict
			}
			err := s.Update(ctx, "s", func(tx store.Tx) error { _, err := service.RegisterExchange(tx, actor, intent); return err })
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
			view(t, s, "s", func(tx store.ReadTx) error {
				sem, _ := store.ReadSemantic(tx)
				if _, err := sem.MutationReceipt(domain.MutationMembership, intent.RequestID); !errors.Is(err, domain.ErrNotFound) {
					t.Fatal("rejected request recorded", err)
				}
				return nil
			})
		})
	}
}
