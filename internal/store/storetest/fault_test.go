package storetest_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// writeMethods lists the methods of iface not in read, minus skip.
func writeMethods(iface, read reflect.Type, skip ...string) []string {
	var out []string
	for i := 0; i < iface.NumMethod(); i++ {
		name := iface.Method(i).Name
		if _, isRead := read.MethodByName(name); isRead {
			continue
		}
		skipped := false
		for _, s := range skip {
			skipped = skipped || s == name
		}
		if !skipped {
			out = append(out, name)
		}
	}
	return out
}

// call invokes method on v with zero arguments and returns its error.
func call(v reflect.Value, method string) error {
	m := v.MethodByName(method)
	args := make([]reflect.Value, m.Type().NumIn())
	for i := range args {
		args[i] = reflect.Zero(m.Type().In(i))
	}
	out := m.Call(args)
	err, _ := out[len(out)-1].Interface().(error)
	return err
}

// TestFaultWrappersComplete fails when a write method of store.Tx or of the
// semantic facet reaches the store through a FaultStore failing its first
// write, i.e. when a new write is added without an interceptor.
func TestFaultWrappersComplete(t *testing.T) {
	txType, readType := reflect.TypeFor[store.Tx](), reflect.TypeFor[store.ReadTx]()
	semType, semRead := reflect.TypeFor[store.SemanticTx](), reflect.TypeFor[store.SemanticReader]()
	legacy := writeMethods(txType, readType, "NextSeq", "Allocated", "Poison")
	semantic := writeMethods(semType, semRead, "Poison")
	if len(legacy) == 0 || len(semantic) == 0 {
		t.Fatal("no write methods found")
	}
	check := func(name string, invoke func(tx store.Tx) error) {
		fs := &storetest.FaultStore{Store: memory.New(), FailAt: 1}
		err := fs.Update(context.Background(), "s", func(tx store.Tx) error { return invoke(tx) })
		if !errors.Is(err, storetest.ErrInjected) {
			t.Errorf("%s is not intercepted: error = %v", name, err)
		}
	}
	for _, name := range legacy {
		check(name, func(tx store.Tx) error { return call(reflect.ValueOf(tx), name) })
	}
	for _, name := range semantic {
		check("semantic "+name, func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			return call(reflect.ValueOf(sem), name)
		})
	}
	// Reads through the facet keep working inside a FaultStore Update.
	fs := &storetest.FaultStore{Store: memory.New()}
	if err := fs.Update(context.Background(), "s", func(tx store.Tx) error {
		_, err := store.ReadSemantic(tx)
		return err
	}); err != nil {
		t.Errorf("ReadSemantic inside a FaultStore: %v", err)
	}
}
