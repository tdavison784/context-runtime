package ingest

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Ledger is what ingestion needs from a transaction beyond store.Tx: the
// immutable event envelope and receipt (D14) with the diagnostic and
// lifecycle-command records it carries (D16, D1). It is declared here, by
// its consumer, until the stores implement it on store.Tx itself; the
// method set is the proposal to p2-store.
type Ledger interface {
	// Receipt returns the receipt of the event with caller EventID eventID
	// in this session, or domain.ErrNotFound. It is read before any
	// sequence number is allocated.
	Receipt(eventID string) (domain.IngestReceipt, error)
	// InsertReceipt persists env and r atomically with the transaction's
	// other writes, including every DiagnosticRecord and
	// LifecycleCommandRecord in r. Both are immutable; reusing a caller
	// EventID or an occurrence ID fails with domain.ErrImmutable.
	InsertReceipt(env domain.EventEnvelope, r domain.IngestReceipt) error
}

// Tx is the transaction ingestion runs in: a store transaction that also
// keeps receipts.
type Tx interface {
	store.Tx
	Ledger
}

// ErrLedgerUnsupported reports a store whose transactions do not keep
// receipts: ingestion never runs without its idempotency ledger.
var ErrLedgerUnsupported = errors.New("ingest: store transaction does not implement the receipt ledger")
