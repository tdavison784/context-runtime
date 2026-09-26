package sqlite

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func decodeAs[T any](data string) (T, error) {
	var out T
	err := decodeLossless([]byte(data), reflect.ValueOf(&out).Elem())
	return out, err
}

func TestLosslessPartsDecodeIsStrict(t *testing.T) {
	for name, data := range map[string]string{
		"unknown field":   `[{"Type":"74657874","Text":"","MediaType":"","BlobHash":"","BlobSize":0,"Extra":"00"}]`,
		"missing field":   `[{"Type":"74657874","Text":"","MediaType":"","BlobHash":""}]`,
		"null field":      `[{"Type":"74657874","Text":null,"MediaType":"","BlobHash":"","BlobSize":0}]`,
		"uppercase hex":   `[{"Type":"74657874","Text":"FF","MediaType":"","BlobHash":"","BlobSize":0}]`,
		"odd hex":         `[{"Type":"74657874","Text":"f","MediaType":"","BlobHash":"","BlobSize":0}]`,
		"negative size":   `[{"Type":"74657874","Text":"","MediaType":"","BlobHash":"","BlobSize":-1}]`,
		"trailing data":   `[] []`,
		"legacy encoding": `[{"Type":"text","Text":"a","MediaType":"","BlobHash":"","BlobSize":0}]`,
	} {
		if _, err := decodeAs[[]domain.ContentPart](data); err == nil {
			t.Errorf("%s: decode accepted %s", name, data)
		}
	}
}

func TestLosslessStringsRoundTripAndStrictDecode(t *testing.T) {
	for _, ss := range [][]string{nil, {}, {"", "a\xffb", "\x00", "ü"}} {
		b, err := encodeLossless(reflect.ValueOf(ss))
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeAs[[]string](string(b))
		if err != nil || (got == nil) != (ss == nil) || !reflect.DeepEqual(got, ss) && len(ss) != 0 {
			t.Fatalf("round trip of %q = %q, %v", ss, got, err)
		}
	}
	for _, data := range []string{`["t1"]`, `["FF"]`, `["f"]`, `[1]`, `[null]`, `{}`, `[] []`, ``} {
		if _, err := decodeAs[[]string](data); err == nil {
			t.Errorf("decode accepted %q", data)
		}
	}
}

// TestLosslessUsageMatchesPlainJSON pins that usage iterations, which hold no
// strings, kept their pre-0003 plain-JSON form (no migration needed).
func TestLosslessUsageMatchesPlainJSON(t *testing.T) {
	n := int64(5)
	usage := []domain.UsageIteration{{Iteration: 1, InputTokens: &n}}
	b, err := encodeLossless(reflect.ValueOf(usage))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"CacheReadTokens":null,"CacheWriteTokens":null,"InputTokens":5,"Iteration":1,"OutputTokens":null,"ReasoningTokens":null}]`
	if string(b) != want {
		t.Fatalf("usage = %s, want %s", b, want)
	}
	legacy := `[{"Iteration":1,"InputTokens":5,"CacheReadTokens":null,"CacheWriteTokens":null,"OutputTokens":null,"ReasoningTokens":null}]`
	got, err := decodeAs[[]domain.UsageIteration](legacy)
	if err != nil || !reflect.DeepEqual(got, usage) {
		t.Fatalf("legacy usage = %+v, %v", got, err)
	}
}
