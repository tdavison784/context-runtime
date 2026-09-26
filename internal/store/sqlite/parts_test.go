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
