package sqlite

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Content parts are stored losslessly (R8, D3). JSON strings cannot carry
// arbitrary bytes: encoding/json replaces invalid UTF-8 with U+FFFD, which
// silently changed text and broke the stored ContentHash. The parts column
// therefore holds a JSON array of objects keyed by ContentPart field name in
// which every string field is the lowercase hex of its exact bytes and every
// integer field is a JSON number. Migration 0002 rewrites rows written by
// 0001 into this form. Decoding is strict: an unknown or missing field, bad
// hex, or trailing data fails with domain.ErrIntegrity, so a later change to
// ContentPart needs a forward migration rather than an invented default.
var partsType = reflect.TypeFor[[]domain.ContentPart]()

func encodeLosslessParts(parts []domain.ContentPart) ([]byte, error) {
	out := make([]map[string]any, len(parts))
	for i, p := range parts {
		v := reflect.ValueOf(p)
		obj := make(map[string]any, v.NumField())
		for j := 0; j < v.NumField(); j++ {
			f := v.Field(j)
			name := v.Type().Field(j).Name
			switch f.Kind() {
			case reflect.String:
				obj[name] = hex.EncodeToString([]byte(f.String()))
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				if f.Uint() > 1<<63-1 {
					return nil, fmt.Errorf("%w: part %d %s exceeds SQLite INTEGER", domain.ErrInvalidRecord, i, name)
				}
				obj[name] = f.Uint()
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				obj[name] = f.Int()
			case reflect.Bool:
				obj[name] = f.Bool()
			default:
				return nil, fmt.Errorf("unsupported content part field %s", name)
			}
		}
		out[i] = obj
	}
	return json.Marshal(out)
}

func decodeLosslessParts(data []byte) ([]domain.ContentPart, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var raw []map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after parts")
	}
	if raw == nil {
		return nil, nil
	}
	typ := partsType.Elem()
	out := make([]domain.ContentPart, len(raw))
	for i, obj := range raw {
		if len(obj) != typ.NumField() {
			return nil, fmt.Errorf("part %d has %d fields, want %d", i, len(obj), typ.NumField())
		}
		v := reflect.ValueOf(&out[i]).Elem()
		for j := 0; j < typ.NumField(); j++ {
			name := typ.Field(j).Name
			msg, ok := obj[name]
			if !ok {
				return nil, fmt.Errorf("part %d is missing %s", i, name)
			}
			f := v.Field(j)
			switch f.Kind() {
			case reflect.String:
				var h string
				if err := json.Unmarshal(msg, &h); err != nil {
					return nil, fmt.Errorf("part %d %s: %v", i, name, err)
				}
				b, err := hex.DecodeString(h)
				if err != nil || hex.EncodeToString(b) != h {
					return nil, fmt.Errorf("part %d %s: not lowercase hex", i, name)
				}
				f.SetString(string(b))
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				var n uint64
				if err := json.Unmarshal(msg, &n); err != nil || f.OverflowUint(n) {
					return nil, fmt.Errorf("part %d %s: invalid unsigned integer", i, name)
				}
				f.SetUint(n)
			case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
				var n int64
				if err := json.Unmarshal(msg, &n); err != nil || f.OverflowInt(n) {
					return nil, fmt.Errorf("part %d %s: invalid integer", i, name)
				}
				f.SetInt(n)
			case reflect.Bool:
				var b bool
				if err := json.Unmarshal(msg, &b); err != nil {
					return nil, fmt.Errorf("part %d %s: invalid boolean", i, name)
				}
				f.SetBool(b)
			default:
				return nil, fmt.Errorf("unsupported content part field %s", name)
			}
		}
	}
	return out, nil
}
