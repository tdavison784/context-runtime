package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// declareHook stands in for W4's obligation.Service. It records the state it
// observed: the replacement occurrence exists and the prior is retired.
type declareHook struct {
	calls           int
	priorRetired    bool
	source          string
	ref             *domain.ObligationRef
	err             error
	allocatedAtCall bool
}

func (h *declareHook) DeclareForReplacementTx(tx store.Tx, actor domain.Principal, sourceID string, seq uint64) (*domain.ObligationRef, error) {
	h.calls++
	h.source, h.allocatedAtCall = sourceID, tx.Allocated(seq)
	current, err := graph.IsCurrent(tx, "prior")
	h.priorRetired = err == nil && !current
	return h.ref, h.err
}

func seedClaimPin(t *testing.T, mem store.Store) {
	t.Helper()
	if err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
		if _, err := tx.PutTask(storetest.NewTask("s", "task"), 0, storetest.NewLifecycleEvent("s", "created", tx.NextSeq(), domain.TargetTask, "task")); err != nil {
			return err
		}
		it := storetest.NewDirective("s", "prior", "d", tx.NextSeq(), "tests must pass")
		it.Namespace, it.Section = domain.NamespaceDirective, domain.SectionPinned
		if err := tx.InsertItem(it); err != nil {
			return err
		}
		if err := storetest.UncheckedSetCurrentVersion(tx, it.ID); err != nil {
			return err
		}
		_, err := graph.DeclareCreation(tx, it, graph.CreationAcceptance{PolicyVersion: "policy/v1", AcceptedAttributes: []string{"obligation=tests_pass"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestReplaceDirectiveDeclaresClaimThroughW4Hook(t *testing.T) {
	ctx := context.Background()
	user := storetest.NewPrincipal("s", domain.AuthorityUser)
	intent := replaceIntent("r", 1, "tests and lint must pass")
	intent.AcceptedAttributes = []string{"obligation=tests_pass"}

	t.Run("no hook fails closed", func(t *testing.T) {
		mem := memory.New()
		t.Cleanup(func() { mem.Close() })
		seedClaimPin(t, mem)
		s, _ := New(mem, testPolicy())
		if _, err := s.ReplaceDirectiveStandalone(ctx, user, intent); !errors.Is(err, domain.ErrUnsupportedSchema) {
			t.Fatalf("claim replaced without W4: %v", err)
		}
	})
	t.Run("hook declares after retirement", func(t *testing.T) {
		mem := memory.New()
		t.Cleanup(func() { mem.Close() })
		seedClaimPin(t, mem)
		base, _ := New(mem, testPolicy())
		hook := &declareHook{ref: &domain.ObligationRef{SessionID: "s", ObligationID: "o", Version: 2}}
		s := base.WithReplacementObligations(hook)
		other := intent
		other.RequestID = "r-base"
		if _, err := base.ReplaceDirectiveStandalone(ctx, user, other); !errors.Is(err, domain.ErrUnsupportedSchema) {
			t.Fatalf("WithReplacementObligations changed the base service: %v", err)
		}
		out, err := s.ReplaceDirectiveStandalone(ctx, user, intent)
		if err != nil {
			t.Fatal(err)
		}
		if hook.calls != 1 || hook.source != out.Result.Records.IDs[0] || !hook.priorRetired || !hook.allocatedAtCall {
			t.Fatalf("hook: %+v", hook)
		}
		if _, err := s.ReplaceDirectiveStandalone(ctx, user, intent); err != nil || hook.calls != 1 {
			t.Fatalf("replay re-declared: calls %d, %v", hook.calls, err)
		}
	})
	for name, hook := range map[string]*declareHook{
		"hook error":             {err: domain.ErrInvalidRecord},
		"claim declared nothing": {},
	} {
		t.Run(name, func(t *testing.T) {
			mem := memory.New()
			t.Cleanup(func() { mem.Close() })
			seedClaimPin(t, mem)
			base, _ := New(mem, testPolicy())
			if _, err := base.WithReplacementObligations(hook).ReplaceDirectiveStandalone(ctx, user, intent); err == nil {
				t.Fatal("replacement committed without its obligation")
			}
			if err := mem.View(ctx, "s", func(tx store.ReadTx) error {
				if current, _ := graph.IsCurrent(tx, "prior"); !current {
					t.Fatal("failed replacement retired the prior claim")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
