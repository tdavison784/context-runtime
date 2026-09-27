package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestNoUncheckedPointerWriteOnPublicTx is SPEC-1.21 (P3-3): the current-version
// pointer is written only through the semantic facet's expected-prior CAS,
// which also refuses duplicate, superseded and unnamespaced items. The
// unconditional SetCurrentVersion(itemID) must not be reachable through the
// public transaction interfaces or the guard.
func TestNoUncheckedPointerWriteOnPublicTx(t *testing.T) {
	for _, typ := range []reflect.Type{
		reflect.TypeOf((*Tx)(nil)).Elem(),
		reflect.TypeOf((*TxBase)(nil)).Elem(),
		reflect.TypeOf(&Guard{}),
	} {
		if m, ok := typ.MethodByName("SetCurrentVersion"); ok && m.Type.NumIn() <= 2 {
			t.Errorf("%s exposes the unchecked pointer write %s", typ, m.Type)
		}
	}
}

// TestUncheckedPointerWriteStaysInFixtures: only the store backends and the
// storetest fixture helper may name the raw pointer write; no production
// package may reach it, even by type assertion (SPEC-1.21).
func TestUncheckedPointerWriteStaysInFixtures(t *testing.T) {
	root := filepath.Join("..", "..")
	allowed := []string{
		filepath.Join("internal", "store", "memory") + string(filepath.Separator),
		filepath.Join("internal", "store", "sqlite") + string(filepath.Separator),
		filepath.Join("internal", "store", "storetest") + string(filepath.Separator),
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && path != root && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, prefix := range allowed {
			if strings.HasPrefix(rel, prefix) {
				return nil
			}
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "UncheckedSetCurrentVersion") {
			t.Errorf("%s reaches the unchecked pointer write; use SemanticTx.SetCurrentVersion", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
