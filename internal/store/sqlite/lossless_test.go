package sqlite

import "testing"

func TestLosslessPartsDecodeIsStrict(t *testing.T) {
	for name, data := range map[string]string{
		"unknown field":   `[{"Type":"74657874","Text":"","MediaType":"","BlobHash":"","BlobSize":0,"Extra":"00"}]`,
		"missing field":   `[{"Type":"74657874","Text":"","MediaType":"","BlobHash":""}]`,
		"uppercase hex":   `[{"Type":"74657874","Text":"FF","MediaType":"","BlobHash":"","BlobSize":0}]`,
		"odd hex":         `[{"Type":"74657874","Text":"f","MediaType":"","BlobHash":"","BlobSize":0}]`,
		"negative size":   `[{"Type":"74657874","Text":"","MediaType":"","BlobHash":"","BlobSize":-1}]`,
		"trailing data":   `[] []`,
		"legacy encoding": `[{"Type":"text","Text":"a","MediaType":"","BlobHash":"","BlobSize":0}]`,
	} {
		if _, err := decodeLosslessParts([]byte(data)); err == nil {
			t.Errorf("%s: decode accepted %s", name, data)
		}
	}
}

func TestLosslessStringsRoundTripAndStrictDecode(t *testing.T) {
	for _, ss := range [][]string{nil, {}, {"", "a\xffb", "\x00", "ü"}} {
		b, err := encodeLosslessStrings(ss)
		if err != nil {
			t.Fatal(err)
		}
		got, err := decodeLosslessStrings(b)
		if err != nil || (got == nil) != (ss == nil) || len(got) != len(ss) {
			t.Fatalf("round trip of %q = %q, %v", ss, got, err)
		}
		for i := range ss {
			if got[i] != ss[i] {
				t.Errorf("element %d = %q, want %q", i, got[i], ss[i])
			}
		}
	}
	for _, data := range []string{`["t1"]`, `["FF"]`, `["f"]`, `[1]`, `{}`, `[] []`, ``} {
		if _, err := decodeLosslessStrings([]byte(data)); err == nil {
			t.Errorf("decode accepted %q", data)
		}
	}
}
