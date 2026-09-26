package sqlite

import (
	"database/sql"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Each record kind has its own table. Keys occupy session_id, id, and subkey;
// every other scalar or nested field occupies one typed column. A presence
// column distinguishes nil pointers from zero-valued nested records, and a
// nil column distinguishes nil byte slices from empty BLOBs. JSON is used
// only for leaf lists (parts, tags, IDs, fingerprints, and usage
// iterations); every string inside one is stored as the hex of its exact
// bytes (lossless.go). No complete record is stored as a second, opaque copy.
type columnRole uint8

const (
	valueColumn columnRole = iota
	presentColumn
	bytesNilColumn
)

type recordColumn struct {
	name string
	path []int
	typ  reflect.Type
	role columnRole
}
type recordSchema struct {
	kind, table, idField, subField  string
	typ                             reflect.Type
	columns                         []recordColumn
	selectSQL, insertSQL, updateSQL string
}

var schemas = makeSchemas()

func makeSchemas() map[string]*recordSchema {
	definitions := []struct {
		kind    string
		record  any
		id, sub string
	}{
		{"item", domain.ContextItem{}, "ID", ""},
		{"relationship", domain.Relationship{}, "ID", ""},
		{"event", domain.EventRecord{}, "EventID", ""},
		{"obligation", domain.ObligationVersion{}, "ObligationID", "Version"},
		{"obligation_transition", domain.ObligationTransition{}, "ID", ""},
		{"grant", domain.MutationGrant{}, "ID", ""},
		{"task", domain.TaskState{}, "TaskID", ""},
		{"lifecycle", domain.LifecycleEvent{}, "ID", ""},
		{"conversation", domain.Conversation{}, "ConversationID", ""},
		{"call", domain.CallRecord{}, "CallID", ""},
		{"attempt", domain.CallAttempt{}, "CallID", "Attempt"},
		{"envelope", domain.EventEnvelope{}, "OccurrenceID", ""},
		{"receipt", receiptRow{}, "OccurrenceID", ""},
		{"receipt_item", receiptItem{}, "OccurrenceID", "Ordinal"},
		{"diagnostic", domain.DiagnosticRecord{}, "ID", ""},
		{"command", domain.LifecycleCommandRecord{}, "ID", ""},
		{"reference", domain.UnresolvedReference{}, "ID", ""},
	}
	out := make(map[string]*recordSchema, len(definitions))
	for _, d := range definitions {
		s := &recordSchema{kind: d.kind, table: "rec_" + d.kind, idField: d.id, subField: d.sub, typ: reflect.TypeOf(d.record)}
		for i := 0; i < s.typ.NumField(); i++ {
			f := s.typ.Field(i)
			if f.Name == "SessionID" || f.Name == s.idField || f.Name == s.subField {
				continue
			}
			s.collect(f.Type, []int{i}, "f_"+snake(f.Name))
		}
		cols := []string{"session_id", "id", "subkey"}
		for _, c := range s.columns {
			cols = append(cols, c.name)
		}
		s.selectSQL = "SELECT " + strings.Join(cols, ",") + " FROM " + s.table
		q := make([]string, len(cols))
		for i := range q {
			q[i] = "?"
		}
		s.insertSQL = "INSERT INTO " + s.table + "(" + strings.Join(cols, ",") + ") VALUES(" + strings.Join(q, ",") + ")"
		sets := make([]string, 0, len(s.columns))
		for _, c := range s.columns {
			sets = append(sets, c.name+"=?")
		}
		s.updateSQL = "UPDATE " + s.table + " SET " + strings.Join(sets, ",") + " WHERE session_id=? AND id=? AND subkey=?"
		out[d.kind] = s
	}
	return out
}

var timeType = reflect.TypeOf(time.Time{})

func (s *recordSchema) collect(typ reflect.Type, path []int, name string) {
	if typ.Kind() == reflect.Pointer {
		s.columns = append(s.columns, recordColumn{name: name + "_present", path: path, typ: typ, role: presentColumn})
		s.collect(typ.Elem(), path, name)
		return
	}
	if typ == timeType {
		s.columns = append(s.columns, recordColumn{name: name, path: path, typ: typ})
		return
	}
	if typ.Kind() == reflect.Struct {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			s.collect(f.Type, append(append([]int(nil), path...), i), name+"_"+snake(f.Name))
		}
		return
	}
	if typ.Kind() == reflect.Slice && typ.Elem().Kind() == reflect.Uint8 {
		s.columns = append(s.columns, recordColumn{name: name + "_nil", path: path, typ: typ, role: bytesNilColumn})
	}
	s.columns = append(s.columns, recordColumn{name: name, path: path, typ: typ})
}
func snake(s string) string {
	s = strings.ReplaceAll(s, "IDs", "Ids")
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		if unicode.IsUpper(c) && i > 0 && (unicode.IsLower(r[i-1]) || unicode.IsDigit(r[i-1]) || unicode.IsUpper(r[i-1]) && i+1 < len(r) && unicode.IsLower(r[i+1])) {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToLower(c))
	}
	return b.String()
}
func (c recordColumn) sqlType() string {
	if c.role != valueColumn {
		return "INTEGER NOT NULL DEFAULT 0"
	}
	t := c.typ
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == timeType {
		return "TEXT"
	}
	if t == partsType {
		return "BLOB" // lossless parts (lossless.go)
	}
	switch t.Kind() {
	case reflect.String:
		return "TEXT"
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "INTEGER"
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return "BLOB"
		}
		return "TEXT" // JSON leaf list.
	}
	panic("unsupported schema field " + t.String())
}

// recordTables lists every record table in creation order.
var recordTables = []string{"item", "relationship", "event", "obligation", "obligation_transition", "grant", "task", "lifecycle", "conversation", "call", "attempt",
	"envelope", "receipt", "receipt_item", "diagnostic", "command", "reference"}

// typedColumns is the column layout (name -> declared type) the Go record
// types require of each rec_* table. Migrations are forward-only and never
// edited once committed, so a new or changed record field needs a new
// migration; TestMigratedSchemaMatchesTypes checks that 0001 plus every later
// migration produces exactly this layout.
func typedColumns() map[string]map[string]string {
	out := make(map[string]map[string]string, len(recordTables))
	for _, kind := range recordTables {
		s := schemas[kind]
		cols := map[string]string{"session_id": "TEXT NOT NULL", "id": "TEXT NOT NULL", "subkey": "INTEGER NOT NULL DEFAULT 0"}
		for _, c := range s.columns {
			cols[c.name] = c.sqlType()
		}
		out[s.table] = cols
	}
	return out
}

// tableDDL is the CREATE TABLE statement for a record kind's typed columns,
// used to write the forward migration that introduces a new record kind.
func tableDDL(kind string) string {
	s := schemas[kind]
	var b strings.Builder
	fmt.Fprintf(&b, "CREATE TABLE %s (\n  session_id TEXT NOT NULL,\n  id TEXT NOT NULL,\n  subkey INTEGER NOT NULL DEFAULT 0", s.table)
	for _, c := range s.columns {
		fmt.Fprintf(&b, ",\n  %s %s", c.name, c.sqlType())
	}
	b.WriteString(",\n  PRIMARY KEY (session_id,id,subkey),\n  FOREIGN KEY (session_id) REFERENCES sessions(session_id)\n);\n")
	return b.String()
}

func (s *recordSchema) recordValues(record any) ([]any, error) {
	v := reflect.ValueOf(record)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Type() != s.typ {
		return nil, fmt.Errorf("record %s has type %s, want %s", s.kind, v.Type(), s.typ)
	}
	out := make([]any, 0, len(s.columns)+3)
	out = append(out, v.FieldByName("SessionID").String(), v.FieldByName(s.idField).String())
	if s.subField == "" {
		out = append(out, 0)
	} else {
		out = append(out, v.FieldByName(s.subField).Interface())
	}
	for _, c := range s.columns {
		field, ok := pathValue(v, c.path)
		if c.role == presentColumn {
			if ok && !field.IsNil() {
				out = append(out, 1)
			} else {
				out = append(out, 0)
			}
			continue
		}
		if c.role == bytesNilColumn {
			if !ok || field.IsNil() {
				out = append(out, 1)
			} else {
				out = append(out, 0)
			}
			continue
		}
		if !ok {
			out = append(out, nil)
			continue
		}
		for field.Kind() == reflect.Pointer {
			if field.IsNil() {
				ok = false
				break
			}
			field = field.Elem()
		}
		if !ok {
			out = append(out, nil)
			continue
		}
		val, err := encodeField(field)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", s.kind, c.name, err)
		}
		out = append(out, val)
	}
	return out, nil
}
func pathValue(root reflect.Value, path []int) (reflect.Value, bool) {
	v := root
	for _, i := range path {
		for v.Kind() == reflect.Pointer {
			if v.IsNil() {
				return reflect.Value{}, false
			}
			v = v.Elem()
		}
		v = v.Field(i)
	}
	return v, true
}
func encodeField(v reflect.Value) (any, error) {
	if v.Type() == timeType {
		t := v.Interface().(time.Time)
		if t.IsZero() {
			return "", nil
		}
		return t.Format(time.RFC3339Nano), nil
	}
	switch v.Kind() {
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return v.Bool(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if v.Uint() > uint64(1<<63-1) {
			return nil, fmt.Errorf("%w: unsigned value exceeds SQLite INTEGER", domain.ErrInvalidRecord)
		}
		return int64(v.Uint()), nil
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			if v.IsNil() {
				return []byte{}, nil
			}
			return append([]byte{}, v.Bytes()...), nil
		}
		b, err := encodeLossless(v)
		if err != nil || v.Type() == partsType {
			return b, err // parts occupy a BLOB column (migration 0002)
		}
		return string(b), nil
	}
	return nil, fmt.Errorf("unsupported field type %s", v.Type())
}

type rowScanner interface{ Scan(...any) error }

func (s *recordSchema) scan(row rowScanner) (reflect.Value, error) {
	raw := make([]any, len(s.columns)+3)
	ptrs := make([]any, len(raw))
	for i := range raw {
		ptrs[i] = &raw[i]
	}
	if err := row.Scan(ptrs...); err != nil {
		return reflect.Value{}, err
	}
	v := reflect.New(s.typ).Elem()
	v.FieldByName("SessionID").SetString(asString(raw[0]))
	v.FieldByName(s.idField).SetString(asString(raw[1]))
	if s.subField != "" {
		f := v.FieldByName(s.subField)
		if f.Kind() == reflect.Int {
			f.SetInt(asInt(raw[2]))
		} else {
			f.SetUint(uint64(asInt(raw[2])))
		}
	}
	nilBytes := make(map[string]bool)
	for i, c := range s.columns {
		x := raw[i+3]
		if c.role == bytesNilColumn {
			nilBytes[strings.TrimSuffix(c.name, "_nil")] = asInt(x) != 0
			continue
		}
		if c.role == presentColumn {
			if asInt(x) != 0 {
				f, ok := pathValue(v, c.path)
				if !ok {
					return reflect.Value{}, fmt.Errorf("%w: missing parent for %s", domain.ErrIntegrity, c.name)
				}
				f.Set(reflect.New(f.Type().Elem()))
			}
			continue
		}
		if x == nil {
			if c.typ.Kind() == reflect.Slice && c.typ.Elem().Kind() == reflect.Uint8 && !nilBytes[c.name] {
				x = []byte{}
			} else {
				continue
			}
		}
		f, ok := pathValue(v, c.path)
		if !ok {
			continue
		}
		for f.Kind() == reflect.Pointer {
			if f.IsNil() {
				break
			}
			f = f.Elem()
		}
		if f.Kind() == reflect.Pointer {
			continue
		}
		if err := decodeField(f, x, nilBytes[c.name]); err != nil {
			return reflect.Value{}, fmt.Errorf("%w: %s.%s: %v", domain.ErrIntegrity, s.kind, c.name, err)
		}
	}
	return v, nil
}
func asString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}
func asInt(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case bool:
		if x {
			return 1
		}
	case []byte:
		n, _ := strconv.ParseInt(string(x), 10, 64)
		return n
	}
	return 0
}
func decodeField(f reflect.Value, x any, bytesNil bool) error {
	if f.Type() == timeType {
		text := asString(x)
		if text == "" {
			return nil
		}
		t, err := time.Parse(time.RFC3339Nano, text)
		if err != nil {
			return err
		}
		f.Set(reflect.ValueOf(t))
		return nil
	}
	switch f.Kind() {
	case reflect.String:
		f.SetString(asString(x))
	case reflect.Bool:
		f.SetBool(asInt(x) != 0)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		f.SetInt(asInt(x))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		f.SetUint(uint64(asInt(x)))
	case reflect.Slice:
		if f.Type().Elem().Kind() == reflect.Uint8 {
			if bytesNil {
				f.SetZero()
			} else {
				b := []byte(asString(x))
				f.SetBytes(append([]byte{}, b...))
			}
			return nil
		}
		return decodeLossless([]byte(asString(x)), f)
	default:
		return fmt.Errorf("unsupported field type %s", f.Type())
	}
	return nil
}

func schemaFor(kind string) (*recordSchema, error) {
	s := schemas[kind]
	if s == nil {
		return nil, fmt.Errorf("unknown record kind %q", kind)
	}
	return s, nil
}

// compile-time use of sql.Row's Scan signature.
var _ rowScanner = (*sql.Row)(nil)
