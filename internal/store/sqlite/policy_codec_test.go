package sqlite

import (
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// fakeRow scans stored column values back, as a query row would.
type fakeRow []any

func (r fakeRow) Scan(dest ...any) error {
	for i := range dest {
		*(dest[i].(*any)) = r[i]
	}
	return nil
}

// TestPolicyGCTriggersRoundTrip checks that the recorded Phase 3 policy's
// GC-trigger set (migration 0028) survives the envelope and receipt
// encodings exactly, and that an absent policy stays absent (P3-38/39).
func TestPolicyGCTriggersRoundTrip(t *testing.T) {
	policy := &domain.Phase3Policy{Version: "p", GCTriggers: domain.DefaultGCTriggers()}
	for _, tc := range []struct {
		kind   string
		record any
	}{
		{"envelope", domain.EventEnvelope{SessionID: "s", OccurrenceID: "o", SemanticPolicy: policy}},
		{"envelope", domain.EventEnvelope{SessionID: "s", OccurrenceID: "o"}},
		{"receipt", receiptRow{SessionID: "s", OccurrenceID: "o", Versions: domain.ExecutionVersions{Semantic: policy}}},
		{"receipt", receiptRow{SessionID: "s", OccurrenceID: "o"}},
	} {
		s := schemas[tc.kind]
		values, err := s.recordValues(tc.record)
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.scan(fakeRow(values))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Interface(), tc.record) {
			t.Errorf("%s round trip:\n got  %+v\n want %+v", tc.kind, got.Interface(), tc.record)
		}
	}
}
