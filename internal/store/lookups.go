package store

import (
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Access-filtered ingest lookups (F1: SEC-1.1, SEC-1.2, DUR-1.1, SPEC-1.3).
//
// Each lookup answers one exact-key question for one source actor (Viewer).
// Records the viewer's access boundary does not permit are filtered inside
// the store's query, before any limit or page is applied, so they are never
// counted, returned, or observable, and another principal can neither block
// nor probe the viewer's lookups by padding a key. Candidate and snapshot
// sets contain only live items: an item stops being live when a SUPERSEDES
// edge retires it or a DUPLICATE_OF edge classifies it, and the store drops
// it from these indexes in the same write, so repeated identical content
// does not grow them (DUR-1.1).
//
// A visible match whose stored content fails verification (a legacy row
// 0001 altered; ContentHash no longer matches) is never returned as a
// candidate and never fails the lookup: its ID is reported in
// Lookup.Unverified so the caller can record an integrity diagnostic, while
// reading it directly still fails with domain.ErrIntegrity (DUR-1.4).

// Cursor is a position in (Seq, ID) order. The zero Cursor precedes every
// record.
type Cursor struct {
	Seq uint64
	ID  string
}

// Page selects at most Limit records strictly after After, in (Seq, ID)
// order. Limit must be positive.
type Page struct {
	After Cursor
	Limit int
}

func (p Page) validate() error {
	if p.Limit <= 0 {
		return fmt.Errorf("%w: page limit must be positive", domain.ErrInvalidRecord)
	}
	return nil
}

// Lookup is the result of an item lookup: verified visible matches in (Seq,
// ID) order, the IDs of visible matches that failed verification (excluded
// from Items), and, for a paged lookup, whether matches remain after the
// last returned one (in Items or Unverified); the next page starts after
// Next.
type Lookup struct {
	Items      []domain.ContextItem
	Unverified []string
	More       bool
	Next       Cursor
}

// BlobReferrerFilter asks for the earliest (Seq, ID) verified item that
// references BlobHash, whose access boundary permits Viewer, and that
// contains Within (Within.Within(item.Access)): the one referrer blob-hash
// authorization needs (D19, R5). Missing and inaccessible referrers are
// indistinguishable: both yield an empty Lookup.
type BlobReferrerFilter struct {
	Viewer   domain.Principal
	BlobHash string
	Within   domain.AccessBoundary
}

// Validate checks the viewer, hash, and boundary.
func (f BlobReferrerFilter) Validate() error {
	if err := f.Viewer.Validate(); err != nil {
		return err
	}
	if !domain.ValidHash(f.BlobHash) {
		return fmt.Errorf("%w: blob referrer: malformed hash", domain.ErrInvalidRecord)
	}
	return f.Within.Validate()
}

// CanonicalFilter selects live duplicate candidates (D10, FR-ING-005): items
// whose task, directive section (with DirectiveID, the namespace), directive
// ID, kind, role, authority, exact access boundary, and content hash all
// equal the filter's, visible to Viewer. Because every earlier identical
// item is either the canonical one or classified DUPLICATE_OF it, and a
// directive's replaced versions are retired by SUPERSEDES, a well-behaved
// caller finds at most one candidate per key; more than Limit visible live
// candidates fail with ErrLimitExceeded, which routine ingestion cannot
// reach.
type CanonicalFilter struct {
	Viewer      domain.Principal
	TaskID      string
	Section     domain.DirectiveSection
	DirectiveID string
	Kind        domain.Kind
	Role        domain.ItemRole
	Authority   domain.Authority
	Access      domain.AccessBoundary
	ContentHash string
	Limit       int
}

// Validate checks the filter's viewer, enums, boundary, hash, and limit.
func (f CanonicalFilter) Validate() error {
	if err := f.Viewer.Validate(); err != nil {
		return err
	}
	if f.Limit <= 0 || !domain.ValidHash(f.ContentHash) || !f.Section.Valid() || !f.Kind.Valid() || !f.Role.Valid() || !f.Authority.Valid() {
		return fmt.Errorf("%w: canonical filter: positive limit and valid hash, section, kind, role, and authority required", domain.ErrInvalidRecord)
	}
	return f.Access.Validate()
}

// WorkingFilter selects the current Working snapshot of one partition
// (FR-DIR-007, D11, SEC-1.2): live items of section WORKING in TaskID with
// exactly this authority and access boundary that the current-version map
// names, visible to Viewer. It is an indexed lookup, never a task scan. The
// set is the partition's last snapshot plus live explicit-ID members, so
// more than Limit fail with ErrLimitExceeded only when a partition's own
// visible snapshot exceeds the caller's per-section item limit.
type WorkingFilter struct {
	Viewer    domain.Principal
	TaskID    string
	Authority domain.Authority
	Access    domain.AccessBoundary
	Limit     int
}

// Validate checks the filter's viewer, authority, boundary, and limit.
func (f WorkingFilter) Validate() error {
	if err := f.Viewer.Validate(); err != nil {
		return err
	}
	if f.Limit <= 0 || !f.Authority.Valid() {
		return fmt.Errorf("%w: working filter: positive limit and valid authority required", domain.ErrInvalidRecord)
	}
	return f.Access.Validate()
}

// SourceFilter pages through the live items whose source locator has
// LocatorKey under domain.LocatorRuleVersion and whose access boundary
// permits Viewer (M5, R2). Fan-out is legitimate, so the lookup pages
// rather than fails; the caller bounds the edges it creates.
type SourceFilter struct {
	Viewer     domain.Principal
	LocatorKey string
	Page       Page
}

// Validate checks the viewer, key, and page.
func (f SourceFilter) Validate() error {
	if err := f.Viewer.Validate(); err != nil {
		return err
	}
	if f.LocatorKey == "" || len(f.LocatorKey) > domain.MaxLocatorKeyBytes {
		return fmt.Errorf("%w: source filter: locator key required", domain.ErrInvalidRecord)
	}
	return f.Page.validate()
}

// VisibleReferenceFilter pages through the unresolved references with
// LocatorKey under domain.LocatorRuleVersion whose access boundary permits
// Viewer (M5, R2).
type VisibleReferenceFilter struct {
	Viewer     domain.Principal
	LocatorKey string
	Page       Page
}

// Validate checks the viewer, key, and page.
func (f VisibleReferenceFilter) Validate() error {
	return SourceFilter(f).Validate()
}

// PermittedOwners lists, per owner field, the values a boundary may carry
// and still permit p: empty (no constraint) or p's own ID. Stores use it to
// turn domain.AccessBoundary.Permits into indexed equality lookups.
func PermittedOwners(p domain.Principal) (workflows, tasks, agents []string) {
	return owners(p.WorkflowID), owners(p.TaskID), owners(p.AgentID)
}

func owners(id string) []string {
	if id == "" {
		return []string{""}
	}
	return []string{"", id}
}
