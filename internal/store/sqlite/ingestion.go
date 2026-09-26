package sqlite

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// errIngestionUnimplemented marks the ingestion records until migration
// 0007 lands them in SQLite.
var errIngestionUnimplemented = errors.New("sqlite: ingestion records not implemented yet")

func (t *transaction) Receipt(string) (domain.IngestReceipt, error) {
	return domain.IngestReceipt{}, errIngestionUnimplemented
}
func (t *transaction) Envelope(string) (domain.EventEnvelope, error) {
	return domain.EventEnvelope{}, errIngestionUnimplemented
}
func (t *transaction) Diagnostics(store.DiagnosticFilter) ([]domain.DiagnosticRecord, error) {
	return nil, errIngestionUnimplemented
}
func (t *transaction) LifecycleCommands(store.CommandFilter) ([]domain.LifecycleCommandRecord, error) {
	return nil, errIngestionUnimplemented
}
func (t *transaction) InsertIngestion(domain.EventEnvelope, domain.IngestReceipt) error {
	return errIngestionUnimplemented
}
