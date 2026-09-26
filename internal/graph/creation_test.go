package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"testing"
)

type creationTx struct {
	store.Tx
	backend *creationBackend
}
type creationBackend struct {
	store.SemanticTx
	inserted domain.CreationDeclaration
}

func (t *creationTx) SemanticTransaction() (store.SemanticTx, error) { return t.backend, nil }
func (b *creationBackend) InsertCreationDeclaration(d domain.CreationDeclaration) error {
	b.inserted = d.Clone()
	return d.Validate()
}

func TestDeclareCreationUsesStoredDefaultsAndCopiesAcceptedInputs(t *testing.T) {
	s := memory.New()
	defer s.Close()
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		item := goalLike("s", "goal", "ship", tx.NextSeq(), "Ship it")
		item.Namespace = domain.NamespaceDirective
		mustInsert(t, tx, item)
		backend := &creationBackend{}
		wrapped := &creationTx{Tx: tx, backend: backend}
		accepted := CreationAcceptance{PolicyVersion: "declaration/1", AcceptedAttributes: []string{"scope=task", "kind=goal"}}
		item.Generation = domain.GenerationPinned // this supplied row is not authoritative
		d, err := DeclareCreation(wrapped, item, accepted)
		if err != nil {
			return err
		}
		if d.AcceptedSemantics.Generation == domain.GenerationPinned || d.AcceptedSemantics.AcceptedAttributes[0] != "kind=goal" || d.Seq <= item.Seq {
			t.Fatal("declaration borrowed supplied metadata or lost allocation")
		}
		accepted.AcceptedAttributes[0] = "changed"
		d.AcceptedSemantics.AcceptedAttributes[0] = "changed-result"
		if backend.inserted.Validate() != nil {
			t.Fatal("declaration aliases caller or return value")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(ctx, "s", func(tx store.Tx) error {
		_, err := DeclareCreation(&creationTx{Tx: tx, backend: &creationBackend{}}, domain.ContextItem{ID: "goal"}, CreationAcceptance{PolicyVersion: "declaration/1"})
		return err
	}); err == nil {
		t.Fatal("late declaration manufactured creation identity")
	}
}
