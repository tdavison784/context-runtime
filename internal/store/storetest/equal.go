package storetest

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

var timeType = reflect.TypeFor[time.Time]()

// assertEqual fails unless got and want are semantically equal: deep
// equality where times compare with time.Equal and nil and empty slices are
// equal, so stores may normalize either without failing the suite.
func assertEqual(t *testing.T, what string, got, want any) {
	t.Helper()
	if !equal(reflect.ValueOf(got), reflect.ValueOf(want)) {
		t.Fatalf("%s mismatch:\n got  %s\n want %s", what, show(got), show(want))
	}
}

func show(v any) string { return fmt.Sprintf("%+v", deref(reflect.ValueOf(v))) }

// deref renders pointers by value so mismatches are readable.
func deref(v reflect.Value) any {
	switch v.Kind() {
	case reflect.Invalid:
		return nil
	case reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		return deref(v.Elem())
	case reflect.Slice:
		out := make([]any, v.Len())
		for i := range out {
			out[i] = deref(v.Index(i))
		}
		return out
	case reflect.Struct:
		if v.Type() == timeType {
			return v.Interface()
		}
		out := make(map[string]any, v.NumField())
		for i := range v.NumField() {
			if v.Type().Field(i).IsExported() {
				out[v.Type().Field(i).Name] = deref(v.Field(i))
			}
		}
		return out
	}
	if v.CanInterface() {
		return v.Interface()
	}
	return v.String()
}

func equal(a, b reflect.Value) bool {
	if !a.IsValid() || !b.IsValid() {
		return a.IsValid() == b.IsValid()
	}
	if a.Type() != b.Type() {
		return false
	}
	switch a.Kind() {
	case reflect.Pointer:
		if a.IsNil() || b.IsNil() {
			return a.IsNil() == b.IsNil()
		}
		return equal(a.Elem(), b.Elem())
	case reflect.Slice:
		if a.Len() != b.Len() {
			return false
		}
		for i := range a.Len() {
			if !equal(a.Index(i), b.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Struct:
		if a.Type() == timeType {
			return a.Interface().(time.Time).Equal(b.Interface().(time.Time))
		}
		for i := range a.NumField() {
			if !equal(a.Field(i), b.Field(i)) {
				return false
			}
		}
		return true
	case reflect.Map, reflect.Interface, reflect.Func, reflect.Chan:
		panic("storetest: unsupported kind " + a.Kind().String())
	}
	return a.Equal(b)
}
