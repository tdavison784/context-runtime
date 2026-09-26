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
	// LookupLimit bounds every indexed store lookup ingestion makes (blob
	// referrers, duplicate candidates, reference matches) (D17, R19); zero
	// means DefaultLookupLimit. A lookup matching more records than this
	// rejects the event (store.ErrLimitExceeded) rather than deciding on a
	// partial answer.
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
	e = e.Clone()
	if err := e.ValidateFor(p, g.Limits); err != nil {
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
// directly. On error the caller must abort tx.
//
// Every error Ingest and Apply return is a bare public sentinel (or a join
// of them): never text naming an item or other record (R20.1).
func (g Ingester) Apply(tx store.Tx, p domain.Principal, e domain.Event, anonymousOccurrence string) (domain.IngestReceipt, error) {
	r, err := g.apply(tx, p, e, anonymousOccurrence)
	return r, sanitize(err)
}

func (g Ingester) apply(tx store.Tx, p domain.Principal, e domain.Event, anonymousOccurrence string) (domain.IngestReceipt, error) {
	e = e.Clone()
	limits := g.Limits.Effective()
	if err := e.ValidateFor(p, limits); err != nil {
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

	r := &run{g: g, tx: tx, p: p, e: e, limits: limits, occurrence: occurrence, payload: payload, now: g.now()}
	return r.apply()
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
