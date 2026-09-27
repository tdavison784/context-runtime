package ingest

import (
	"errors"
	"reflect"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Regression tests for PR #6 review round 1 (ingest findings).

// TestEmptyOperationStreamRejected_G4 is SEC-1.11/SPEC-1.2 (G4, P3-34/40):
// an event with spans and an empty, non-nil Operations stream is malformed
// and aborts with nothing written, rather than committing an envelope whose
// spans were never ingested. The nil form still ingests every span, and it
// is not locked out by the rejected empty form (nil-vs-empty round trip).
func TestEmptyOperationStreamRejected_G4(t *testing.T) {
	semanticStores(t, func(t *testing.T, f *fixture) {
		user := principal(domain.AuthorityUser)
		e := userEvent("g4-empty-ops", "hello there", false)
		e.Operations = []domain.SemanticOperation{}
		f.requireAtomic(domain.ErrInvalidRecord, func() error {
			_, err := f.ingest(user, e)
			return err
		})
		e.Operations = nil
		r := f.mustIngest(user, e)
		if len(r.ItemIDs()) != 1 {
			t.Fatalf("nil stream ingested %d items, want 1", len(r.ItemIDs()))
		}
		again, err := f.ingest(user, e)
		if err != nil || !reflect.DeepEqual(again, r) {
			t.Fatalf("nil-stream retry = %v", err)
		}
		e.Operations = []domain.SemanticOperation{}
		if _, err := f.ingest(user, e); !errors.Is(err, domain.ErrInvalidRecord) && !errors.Is(err, domain.ErrEventIDConflict) {
			t.Fatalf("empty-stream retry of the nil form = %v, want rejection", err)
		}
	})
}
