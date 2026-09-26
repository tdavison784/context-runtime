package graph

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"testing"
)

type resolutionOnlyTx struct{ store.ReadTx }

func (resolutionOnlyTx) Grants() ([]domain.MutationGrant, error) {
	panic("resolution inspected grants")
}
func (resolutionOnlyTx) LastSeq() uint64 { panic("resolution predicted sequence") }

func TestLifecycleResolutionDoesNotAuthorizeOrPredictSequence(t *testing.T) {
	s := memory.New()
	defer s.Close()
	lifecycleFixture(t, s, "s")
	if err := s.View(ctx, "s", func(tx store.ReadTx) error {
		reader := resolutionOnlyTx{tx}
		got, err := ResolveLifecycleCommand(reader, principal("s", domain.AuthoritySystem), "task", command(domain.LifecycleResolve, "sys-goal", domain.AuthorityUser))
		if err != nil || got.ResolvedItemID != "sys-goal-1" || got.SourceActor.Authority != domain.AuthorityUser || got.GrantID != "" {
			t.Fatalf("resolution conflated authorization: %+v %v", got, err)
		}
		got, err = ResolveLifecycleCommand(reader, principal("s", domain.AuthorityUser), "task", command(domain.LifecycleUnpin, "user-goal", domain.AuthorityUser))
		if !errors.Is(err, ErrLifecycleTargetMismatch) || got.ResolvedItemID != "user-goal-1" || got.TargetVersion != 1 {
			t.Fatalf("mismatch lost accessible details: %+v %v", got, err)
		}
		_, err = ResolveLifecycleCommand(reader, principal("s", domain.AuthorityUser), "task", command(domain.LifecycleResolve, "absent", domain.AuthorityUser))
		if !errors.Is(err, domain.ErrNotFound) {
			t.Fatal(err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
