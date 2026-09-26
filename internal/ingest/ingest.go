// Package ingest implements the FR-ING-001 ingestion pipeline over a
// store.Store and internal/graph: authenticated envelope validation,
// idempotency receipts, span snapshots, source-gated directive parsing,
// deterministic classification (policy v1), duplicate/replacement/snapshot
// handling, obligations, read-only lifecycle authorization, turns, and
// atomic persistence (SDD section 6).
//
// Ingest is the outer entry point. Apply is the transaction-scoped core: it
// runs entirely inside one caller-supplied transaction, never calls the
// store re-entrantly, and returns no partial result on failure (M3), so the
// call ledger can later ingest provider output in its own outcome
// transaction.
package ingest

import (
	"context"
	"time"

	"github.com/tdavison784/context-runtime/internal/directive"
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Ingester holds trusted ingestion configuration. The zero value uses
// default limits, random anonymous occurrence IDs, and the wall clock.
type Ingester struct {
	// Limits bound the whole event (D17); zero fields select defaults.
	Limits domain.Limits
	// IDs generates anonymous occurrence IDs; nil means domain.RandomIDs.
	IDs domain.IDGenerator
	// Now stamps CreatedAt, which is audit-only; nil means time.Now.
	Now func() time.Time
	// LookupLimit is the page size of the paged reference lookups and the
	// bound on one exact-key candidate lookup (D17, F1); zero means
	// DefaultLookupLimit. Every lookup is filtered to the source actor
	// inside the store, and a candidate set holds only live items, so the
	// bound is unreachable in routine use; exceeding it still rejects the
	// event (store.ErrLimitExceeded) rather than deciding on a partial set.
	LookupLimit int
}

// DefaultLookupLimit is the default bound on one indexed lookup.
const DefaultLookupLimit = 4096

func (g Ingester) lookupLimit() int {
	if g.LookupLimit > 0 {
		return g.LookupLimit
	}
	return DefaultLookupLimit
}

// Versions are the execution versions this ingester records in every
// receipt (D14, M2).
func (g Ingester) Versions() domain.ExecutionVersions {
	return domain.ExecutionVersions{Parser: directive.ParserVersion, Policy: policy.Version, Limits: g.Limits.Effective()}
}

func (g Ingester) now() time.Time {
	if g.Now != nil {
		return g.Now().UTC()
	}
	return time.Now().UTC()
}

// Ingest validates e for the authenticated principal p and applies it in
// one store transaction for p's session, returning the event's immutable
// receipt. A retry with the same caller EventID and request returns the
// original receipt unchanged; a different request under that EventID fails
// with domain.ErrEventIDConflict. An event without an EventID is never
// idempotent: it gets a fresh anonymous occurrence ID, generated here once,
// outside the transaction callback a store may retry (D14, M3).
//
// Authorization, integrity, ownership, idempotency, and resource failures
// reject the whole event with nothing written; syntax and attribute
// problems, boundary conflicts (R13), and unresolved or mismatched
// lifecycle targets (R7, R14) are diagnostics in the receipt.
func (g Ingester) Ingest(ctx context.Context, s store.Store, p domain.Principal, e domain.Event) (domain.IngestReceipt, error) {
	r, err := g.ingest(ctx, s, p, e)
	return r, sanitize(err)
}

func (g Ingester) ingest(ctx context.Context, s store.Store, p domain.Principal, e domain.Event) (domain.IngestReceipt, error) {
	// Admission (SEC-2.1), from lengths alone and outside any write
	// transaction: the hard ceiling first, then the configured limits. An
	// over-limit event is admitted only as the retry of a known EventID
	// (F3), and only when cheap reads prove it can match: the stored receipt
	// is this principal's and the stored envelope has the same shape
	// (SEC-3.1). A new, anonymous, or mismatched over-limit event is
	// rejected before its payload is copied or hashed.
	if err := checkSizes(e, hardSizes()); err != nil {
		return domain.IngestReceipt{}, err
	}
	if err := checkSizes(e, configuredSizes(g.Limits.Effective())); err != nil {
		if e.EventID == "" || p.Validate() != nil {
			return domain.IngestReceipt{}, err
		}
		var admit error
		if verr := s.View(ctx, p.SessionID, func(tx store.ReadTx) error {
			admit = admitKnownRetry(tx, p, e, err)
			return nil
		}); verr != nil {
			return domain.IngestReceipt{}, verr
		}
		if admit != nil {
			return domain.IngestReceipt{}, admit
		}
	}
	e = e.Clone()
	// Structure and authority only: configured limits apply after the
	// idempotency lookup, to new events (F3).
	if err := e.ValidateFor(p, retryCeiling(g.Limits)); err != nil {
		return domain.IngestReceipt{}, err
	}
	var anonymous string
	if e.EventID == "" {
		ids := g.IDs
		if ids == nil {
			ids = domain.RandomIDs{}
		}
		anonymous = domain.NewAnonymousOccurrenceID(ids)
	}
	var out domain.IngestReceipt
	err := s.Update(ctx, p.SessionID, func(tx store.Tx) error {
		r, err := g.apply(tx, p, e, anonymous)
		if err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return domain.IngestReceipt{}, err
	}
	return out, nil
}

// Apply ingests e for p inside tx (M3). anonymousOccurrence must be a fresh
// anonymous occurrence ID when e has no EventID and must be empty
// otherwise; the caller generates it once per attempt, outside any retried
// transaction callback. Apply revalidates e, so it is safe to call
// directly. It is failure-atomic: an error after its first write poisons
// tx (store.Tx.Poison), so the caller's transaction commits nothing even if
// the error is ignored; an error before any write leaves tx usable.
//
// Every error Ingest and Apply return is a bare public sentinel (or a join
// of them): never text naming an item or other record (R20.1).
func (g Ingester) Apply(tx store.Tx, p domain.Principal, e domain.Event, anonymousOccurrence string) (domain.IngestReceipt, error) {
	r, err := g.apply(tx, p, e, anonymousOccurrence)
	return r, sanitize(err)
}

func (g Ingester) apply(tx store.Tx, p domain.Principal, e domain.Event, anonymousOccurrence string) (domain.IngestReceipt, error) {
	limits := g.Limits.Effective()
	// Admission from lengths alone (SEC-2.1), as in Ingest: over the
	// configured limits, only the retry of a known EventID may proceed, and
	// only it is validated against the retry ceiling (F3).
	if err := checkSizes(e, hardSizes()); err != nil {
		return domain.IngestReceipt{}, err
	}
	validation := limits
	if err := checkSizes(e, configuredSizes(limits)); err != nil {
		if e.EventID == "" || p.Validate() != nil {
			return domain.IngestReceipt{}, err
		}
		if admit := admitKnownRetry(tx, p, e, err); admit != nil {
			return domain.IngestReceipt{}, admit
		}
		validation = retryCeiling(g.Limits)
	}
	e = e.Clone()
	if err := e.ValidateFor(p, validation); err != nil {
		return domain.IngestReceipt{}, err
	}
	if tx.SessionID() != p.SessionID {
		return domain.IngestReceipt{}, domain.ErrInvalidRecord
	}
	occurrence := anonymousOccurrence
	if e.EventID != "" {
		if anonymousOccurrence != "" {
			return domain.IngestReceipt{}, domain.ErrInvalidRecord
		}
		occurrence = domain.CallerOccurrenceID(p.SessionID, e.EventID)
	}
	if !domain.OccurrenceMatchesEvent(p.SessionID, occurrence, e.EventID) {
		return domain.IngestReceipt{}, domain.ErrInvalidRecord
	}
	payload, err := e.PayloadHash(p)
	if err != nil {
		return domain.IngestReceipt{}, err
	}

	// Idempotency first, before any sequence, turn, item, or diagnostic is
	// allocated (D14): a retry returns the original receipt as stored,
	// without reparsing or reading mutable state.
	if e.EventID != "" {
		if r, found, err := lookupReceipt(tx, p, occurrence, e.EventID, payload); found || err != nil {
			return r, err
		}
	}
	// Only a new occurrence is held to the currently configured limits
	// (F3, SPEC-1.7, DUR-1.2): a retry above replayed its receipt whatever
	// the limits are now.
	if err := e.ValidateFor(p, limits); err != nil {
		return domain.IngestReceipt{}, err
	}

	r := &run{g: g, tx: tx, p: p, e: e, limits: limits, occurrence: occurrence, payload: payload, now: g.now()}
	rc, err := r.apply()
	if err != nil {
		// Everything above only read; from here the core has written. Any
		// failure poisons the caller's transaction, so no partial result
		// can commit even if the caller ignores the error, and the EventID
		// stays retryable (DUR-1.3). The poison carries only the public
		// sentinel, never item details (R20.1).
		err = sanitize(err)
		tx.Poison(err)
		return domain.IngestReceipt{}, err
	}
	return rc, nil
}

// lookupReceipt returns the stored receipt of a caller EventID (keyed by its
// derived occurrence) if the request matches it, domain.ErrEventIDConflict
// with no details if it does not, and found=false if the EventID is new. A
// Phase 1 event record with no receipt cannot reproduce its original
// result and is a conflict too.
func lookupReceipt(tx store.Tx, p domain.Principal, occurrence, eventID, payload string) (domain.IngestReceipt, bool, error) {
	r, err := tx.Receipt(occurrence)
	switch {
	case err == nil:
		if r.Principal != p || r.PayloadHash != payload {
			return domain.IngestReceipt{}, true, domain.ErrEventIDConflict
		}
		return r.Clone(), true, nil
	case !isNotFound(err):
		return domain.IngestReceipt{}, true, err
	}
	if _, err := tx.Event(eventID); err == nil {
		return domain.IngestReceipt{}, true, domain.ErrEventIDConflict
	} else if !isNotFound(err) {
		return domain.IngestReceipt{}, true, err
	}
	return domain.IngestReceipt{}, false, nil
}

// retryCeiling returns the version-independent limits an event is validated
// against before its idempotency lookup (F3): every structural and
// authority rule of ValidateFor applies, but resource limits are only a
// fixed hard ceiling (or the configured value, if larger) that bounds
// hashing work. A committed event's retry is therefore never rejected by a
// later, tighter configuration; the configured limits are checked only for
// a new occurrence.
func retryCeiling(configured domain.Limits) domain.Limits {
	c := configured.Effective()
	ceil := func(v *int, hard int) { *v = max(*v, hard) }
	ceil(&c.MaxSpanBytes, 1<<30)
	ceil(&c.MaxItemsPerSpan, 1<<20)
	ceil(&c.MaxAttributes, 1<<10)
	ceil(&c.MaxAttributeBytes, 1<<20)
	ceil(&c.MaxHeadingBytes, 1<<20)
	ceil(&c.MaxSpans, 1<<16)
	ceil(&c.MaxParts, 1<<20)
	ceil(&c.MaxEventBytes, 1<<30)
	ceil(&c.MaxBlobBytes, 1<<30)
	ceil(&c.MaxEventItems, 1<<20)
	ceil(&c.MaxEventDiagnostics, 1<<20)
	ceil(&c.MaxRelationships, 1<<22)
	ceil(&c.MaxReferenceLinks, 1<<22)
	return c
}
