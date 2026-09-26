package sqlite

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Leaf lists (content parts, tags, ID lists, fingerprints, source ranges,
// usage iterations) are stored as lossless JSON (R8, D3; migration 0002
// names this file by its former name, parts.go). encoding/json replaces
// invalid UTF-8 with U+FFFD, which silently changed text, broke stored
// ContentHashes, and mapped distinct IDs such as "a\xffb" and "a\xfeb" to
// one "a�b". The lossless form is JSON in which:
//
//   - every string is the lowercase hex of its exact bytes;
//   - integers are JSON numbers and booleans JSON booleans;
//   - a struct is an object keyed by every exported field name;
//   - a nil slice or pointer is null, other slices are arrays.
//
// Decoding is strict: an unknown or missing field, bad hex, a wrong JSON
// type, or trailing data fails (domain.ErrIntegrity at the caller), so a
// change to a listed type needs a forward migration rather than an invented
// default (M8). Migrations 0002 and 0003 rewrote rows stored before this
// form; usage iterations hold no strings and were already in it.
var partsType = reflect.TypeFor[[]domain.ContentPart]()

func encodeLossless(v reflect.Value) ([]byte, error) {
	tree, err := losslessTree(v)
	if err != nil {
		return nil, err
	}
	return json.Marshal(tree)
}

func losslessTree(v reflect.Value) (any, error) {
	switch v.Kind() {
	case reflect.String:
		return hex.EncodeToString([]byte(v.String())), nil
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint(), nil
	case reflect.Pointer:
		if v.IsNil() {
			return nil, nil
		}
		return losslessTree(v.Elem())
	case reflect.Slice:
		if v.IsNil() {
			return nil, nil
		}
		out := make([]any, v.Len())
		for i := range out {
			x, err := losslessTree(v.Index(i))
			if err != nil {
				return nil, err
			}
			out[i] = x
		}
		return out, nil
	case reflect.Struct:
		out := make(map[string]any, v.NumField())
		for i := 0; i < v.NumField(); i++ {
			f := v.Type().Field(i)
			if !f.IsExported() {
				return nil, fmt.Errorf("unexported field %s.%s", v.Type(), f.Name)
			}
			x, err := losslessTree(v.Field(i))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.Name, err)
			}
			out[f.Name] = x
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported leaf-list type %s", v.Type())
}

// decodeLossless decodes data into the value f points at.
func decodeLossless(data []byte, f reflect.Value) error {
	var raw json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&raw); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data")
	}
	out := reflect.New(f.Type()).Elem()
	if err := decodeTree(raw, out); err != nil {
		return err
	}
	f.Set(out)
	return nil
}

func decodeTree(raw json.RawMessage, v reflect.Value) error {
	null := bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
	switch v.Kind() {
	case reflect.String:
		var h string
		if err := strictUnmarshal(raw, &h); err != nil {
			return err
		}
		b, err := hex.DecodeString(h)
		if err != nil || hex.EncodeToString(b) != h {
			return fmt.Errorf("not lowercase hex")
		}
		v.SetString(string(b))
	case reflect.Bool:
		var b bool
		if err := strictUnmarshal(raw, &b); err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var n int64
		if err := strictUnmarshal(raw, &n); err != nil || v.OverflowInt(n) {
			return fmt.Errorf("invalid integer")
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		var n uint64
		if err := strictUnmarshal(raw, &n); err != nil || v.OverflowUint(n) {
			return fmt.Errorf("invalid unsigned integer")
		}
		v.SetUint(n)
	case reflect.Pointer:
		if null {
			return nil
		}
		p := reflect.New(v.Type().Elem())
		if err := decodeTree(raw, p.Elem()); err != nil {
			return err
		}
		v.Set(p)
	case reflect.Slice:
		if null {
			return nil
		}
		var elems []json.RawMessage
		if err := strictUnmarshal(raw, &elems); err != nil {
			return err
		}
		s := reflect.MakeSlice(v.Type(), len(elems), len(elems))
		for i, e := range elems {
			if err := decodeTree(e, s.Index(i)); err != nil {
				return fmt.Errorf("element %d: %w", i, err)
			}
		}
		v.Set(s)
	case reflect.Struct:
		var obj map[string]json.RawMessage
		if err := strictUnmarshal(raw, &obj); err != nil || obj == nil {
			return fmt.Errorf("want an object")
		}
		if len(obj) != v.NumField() {
			return fmt.Errorf("%d fields, want %d", len(obj), v.NumField())
		}
		for i := 0; i < v.NumField(); i++ {
			name := v.Type().Field(i).Name
			x, ok := obj[name]
			if !ok {
				return fmt.Errorf("missing field %s", name)
			}
			if err := decodeTree(x, v.Field(i)); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	default:
		return fmt.Errorf("unsupported leaf-list type %s", v.Type())
	}
	return nil
}

// strictUnmarshal rejects JSON null for a non-nullable value, so a missing
// value never decodes as a zero.
func strictUnmarshal(raw json.RawMessage, v any) error {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("unexpected null")
	}
	return json.Unmarshal(raw, v)
}
