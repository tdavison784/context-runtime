package store

import (
	"reflect"
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
