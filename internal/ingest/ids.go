package ingest

import (
	"fmt"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// auditID derives the ID of an ingestion audit record of the given kind
// from its identifying parts, in its own versioned hash domain.
func auditID(kind string, parts ...string) string {
	e := domain.NewCanonicalEncoder("context-runtime/ingest/" + kind + "-audit-id/v1")
	for _, p := range parts {
		e.String(p)
	}
	return "evt_" + strings.TrimPrefix(e.Hash(), "sha256:")[:32]
}

// errLimit reports an exceeded whole-event execution limit (D17), which
// rejects the event with nothing written.
func errLimit(name string) error {
	return fmt.Errorf("%w: ingest: event exceeds %s", domain.ErrInvalidRecord, name)
}
