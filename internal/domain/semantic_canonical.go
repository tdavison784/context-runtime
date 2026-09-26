package domain

import (
	"bytes"
	"reflect"
	"slices"
)

// CanonicalSemanticArguments is the frozen semantic-arguments/v1 schema.
// Types, field names/order, pointer presence, and ordered slice presence are
// encoded. canonical:"set" slices are sorted unique and nil equals empty.
// Request structs encoded here are frozen: any field change requires a new
// request schema. Maps, interfaces, floats, private fields, and time are rejected.
// maxBytes bounds work and allocation before values are copied into the encoder.
func CanonicalSemanticArguments(value any, maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, ErrResourceLimit
	}
	e := NewCanonicalEncoder("context-runtime/semantic-arguments/v1")
	if err := encodeSemanticValue(e, reflect.ValueOf(value), maxBytes, 0); err != nil {
		return nil, err
	}
	if len(e.buf) > maxBytes {
		return nil, ErrResourceLimit
	}
	return e.Encoded(), nil
}
func canonicalRoom(e *CanonicalEncoder, limit, n int) error {
	if n < 0 || len(e.buf) > limit || n > limit-len(e.buf) {
		return ErrResourceLimit
	}
	return nil
}
func encodeSemanticValue(e *CanonicalEncoder, v reflect.Value, limit, depth int) error {
	if !v.IsValid() || depth > 32 {
		return invalid("semantic encoding: unsupported value or depth")
	}
	name := v.Type().String()
	if err := canonicalRoom(e, limit, len(name)+20); err != nil {
		return err
	}
	e.String(name)
	switch v.Kind() {
	case reflect.String:
		if err := canonicalRoom(e, limit, v.Len()+10); err != nil {
			return err
		}
		e.String(v.String())
	case reflect.Bool:
		e.Uint(boolUint(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		e.Int(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		e.Uint(v.Uint())
	case reflect.Pointer:
		e.Uint(boolUint(!v.IsNil()))
		if !v.IsNil() {
			return encodeSemanticValue(e, v.Elem(), limit, depth+1)
		}
	case reflect.Struct:
		e.Uint(uint64(v.NumField()))
		for n := 0; n < v.NumField(); n++ {
			f := v.Type().Field(n)
			if !f.IsExported() {
				return invalid("semantic encoding: private field")
			}
			if err := canonicalRoom(e, limit, len(f.Name)+10); err != nil {
				return err
			}
			e.String(f.Name)
			var err error
			if f.Tag.Get("canonical") == "set" {
				err = encodeSemanticSet(e, v.Field(n), limit, depth+1)
			} else {
				err = encodeSemanticValue(e, v.Field(n), limit, depth+1)
			}
			if err != nil {
				return err
			}
		}
	case reflect.Slice:
		if err := canonicalRoom(e, limit, v.Len()+10); err != nil {
			return err
		}
		e.Uint(boolUint(!v.IsNil())).Uint(uint64(v.Len()))
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if err := canonicalRoom(e, limit, v.Len()+10); err != nil {
				return err
			}
			e.Bytes(v.Bytes())
			break
		}
		for n := 0; n < v.Len(); n++ {
			if err := encodeSemanticValue(e, v.Index(n), limit, depth+1); err != nil {
				return err
			}
		}
	default:
		return invalid("semantic encoding: unsupported type")
	}
	return canonicalRoom(e, limit, 0)
}
func encodeSemanticSet(e *CanonicalEncoder, v reflect.Value, limit, depth int) error {
	if v.Kind() != reflect.Slice {
		return invalid("semantic encoding: set must be a slice")
	}
	if err := canonicalRoom(e, limit, v.Len()+10); err != nil {
		return err
	}
	members := make([][]byte, 0, v.Len())
	remaining := limit - len(e.buf) - 10
	for n := 0; n < v.Len(); n++ {
		member := &CanonicalEncoder{}
		if err := encodeSemanticValue(member, v.Index(n), remaining, depth+1); err != nil {
			return err
		}
		remaining -= len(member.buf) + 10
		if remaining < 0 {
			return ErrResourceLimit
		}
		members = append(members, member.Encoded())
	}
	slices.SortFunc(members, bytes.Compare)
	e.Uint(uint64(len(members)))
	for n, m := range members {
		if n > 0 && bytes.Equal(m, members[n-1]) {
			return invalid("semantic encoding: duplicate set member")
		}
		e.Bytes(m)
	}
	return canonicalRoom(e, limit, 0)
}
