package store

import (
	"errors"
	"github.com/tdavison784/context-runtime/internal/domain"
	"reflect"
	"testing"
)

type guardBackend struct {
	TxBase
	failure error
}

func (b *guardBackend) InsertItem(domain.ContextItem) error { return b.failure }
func TestGuardPoisonsIgnoredErrorAfterSuccessfulWrite(t *testing.T) {
	failure := errors.New("injected")
	b := &guardBackend{}
	g := NewGuard(b)
	if err := g.InsertItem(domain.ContextItem{}); err != nil {
		t.Fatal(err)
	}
	b.failure = failure
	_ = g.InsertItem(domain.ContextItem{})
	if !errors.Is(g.Poisoned(), failure) {
		t.Fatal("ignored error did not poison")
	}
}
func TestSemanticGuardBlocksEveryWrite(t *testing.T) {
	failure := errors.New("poison")
	g := &semanticGuard{base: NewGuard(nil)}
	g.Poison(failure)
	typ := reflect.TypeFor[SemanticWriter]()
	value := reflect.ValueOf(g)
	for n := 0; n < typ.NumMethod(); n++ {
		m := typ.Method(n)
		args := make([]reflect.Value, m.Type.NumIn())
		for i := range args {
			args[i] = reflect.Zero(m.Type.In(i))
		}
		out := value.MethodByName(m.Name).Call(args)
		if !errors.Is(out[len(out)-1].Interface().(error), failure) {
			t.Fatalf("%s bypassed poison", m.Name)
		}
	}
}
func TestSemanticFacetIsFailClosedWithoutBackend(t *testing.T) {
	if _, err := Semantic(NewGuard(&guardBackend{})); !errors.Is(err, domain.ErrUnsupportedSchema) {
		t.Fatal(err)
	}
}
