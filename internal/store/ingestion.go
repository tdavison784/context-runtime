package store

import (
	"fmt"
	"reflect"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// ValidateIngestion is the structural half of InsertIngestion's contract,
// shared by implementations: env and receipt validate, belong to session,
// and describe the same request. Checks against stored state (sequence
// allocation, stored items, link and command targets, occurrence reuse)
// remain each store's job.
func ValidateIngestion(session string, env domain.EventEnvelope, r domain.IngestReceipt) error {
	if err := env.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if env.SessionID != session || r.SessionID != session {
		return fmt.Errorf("%w: ingestion belongs to another session", domain.ErrInvalidRecord)
	}
	if env.OccurrenceID != r.OccurrenceID || env.EventID != r.EventID || env.PayloadHash != r.PayloadHash || env.Principal != r.Principal {
		return fmt.Errorf("%w: envelope and receipt describe different requests", domain.ErrInvalidRecord)
	}
	return nil
}

// ReceiptItemMatches reports whether a stored item equals the receipt's
// snapshot of it, treating nil and empty slices alike.
func ReceiptItemMatches(stored, snapshot domain.ContextItem) bool {
	norm := func(it domain.ContextItem) domain.ContextItem {
		it = it.Clone()
		if len(it.Tags) == 0 {
			it.Tags = nil
		}
		if len(it.SourceRanges) == 0 {
			it.SourceRanges = nil
		}
		for i := range it.SourceRanges {
			if len(it.SourceRanges[i].Slices) == 0 {
				it.SourceRanges[i].Slices = nil
			}
		}
		it.CreatedAt = it.CreatedAt.UTC()
		return it
	}
	return reflect.DeepEqual(norm(stored), norm(snapshot))
}
