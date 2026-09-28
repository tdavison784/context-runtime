package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

type commandExecutor interface {
	Resolve(store.Tx, domain.Principal, domain.ItemMutationIntent, uint64) (LifecycleOutcome, error)
	Unpin(store.Tx, domain.Principal, domain.ItemMutationIntent, uint64) (LifecycleOutcome, error)
}

var _ commandExecutor = (*Service)(nil)

func TestExecutorFailureReturnsNoReceiptAndPoisonsEvent(t *testing.T) {
	for _, unpin := range []bool{false, true} {
		mem := memory.New()
		t.Cleanup(func() { mem.Close() })
		s, _ := New(mem, testPolicy())
		execute := s.Resolve
		if unpin {
			execute = s.Unpin
		}
		p := storetest.NewPrincipal("s", domain.AuthorityUser)
		err := mem.Update(context.Background(), "s", func(tx store.Tx) error {
			if err := tx.InsertItem(storetest.NewItem("s", "earlier", tx.NextSeq(), "rolled back")); err != nil {
				return err
			}
			out, err := execute(legacyOnly{tx}, p, domain.ItemMutationIntent{RequestID: "r", ItemID: "earlier", ExpectedVersion: 1}, tx.NextSeq())
			if err != domain.ErrUnsupportedSchema || out.MutationReceiptID != "" || out.GrantID != "" || out.Result.ItemID != "" {
				t.Fatalf("failure published outcome: %+v %v", out, err)
			}
			return nil
		})
		if !errors.Is(err, domain.ErrUnsupportedSchema) {
			t.Fatalf("ignored failure committed: %v", err)
		}
	}
}
