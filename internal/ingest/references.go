package ingest

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// referenceKey returns the locator key (domain.LocatorKey, rule
// domain.LocatorRuleVersion) of a References item's text: a URL if it looks
// like one, else a path. The store indexes item sources under the same
// rule, so declaration-time and deferred matching agree exactly.
func referenceKey(text string) (string, bool) {
	if key, ok := domain.LocatorKey(domain.SourceURL, text); ok {
		return key, true
	}
	return domain.LocatorKey(domain.SourcePath, text)
}

// declareReference links a new References item to the item it names by
// ID, if any (F4), then persists an immutable unresolved reference for it
// if its text is a locator and links every already ingested source it names
// (M5, R2). The record is kept even when a source
// matched, since every later ingestion of a matching source may add an
// edge; references are never retired. Existing sources are paged from the
// store's exact-key source index, filtered to the source actor inside the
// query (F1, SEC-1.1), so another principal's sources are never counted;
// linking stops, without failing the event, once the event's relationship
// budget (MaxRelationships, D17) is spent.
func (r *run) declareReference(c unitCtx, ref domain.ContextItem) error {
	if err := r.referenceItemID(c, ref); err != nil {
		return err
	}
	key, ok := referenceKey(ref.Parts[0].Text)
	if !ok {
		return nil
	}
	var after store.Cursor
	for more := true; more; {
		page, err := r.tx.SourceItems(store.SourceFilter{Viewer: c.actor, LocatorKey: key, Page: store.Page{After: after, Limit: r.linkPageLimit()}})
		if err != nil {
			return err
		}
		r.reportUnverified(c.si, c.pi, domain.ByteRange{}, ref.Access, page.Unverified)
		for _, target := range page.Items {
			if target.ID == ref.ID {
				continue
			}
			stop, err := r.linkReference(c.actor, ref, target)
			if err != nil {
				return err
			}
			if stop {
				r.truncatedLinks(c.si, c.pi, ref.SourceRanges[0].Range, ref.Access)
				more = false
				break
			}
		}
		if more {
			more, after = page.More, page.Next
		}
	}
	ordinal := r.refs
	r.refs++
	return r.tx.InsertUnresolvedReference(domain.UnresolvedReference{
		ID:           domain.UnresolvedReferenceID(r.p.SessionID, r.occurrence, ordinal),
		SessionID:    r.p.SessionID,
		OccurrenceID: r.occurrence,
		Ordinal:      ordinal,
		SpanIndex:    c.si,
		ItemID:       ref.ID,
		LocatorKey:   key,
		RuleVersion:  domain.LocatorRuleVersion,
		Access:       ref.Access,
		Authority:    ref.Authority,
		Seq:          r.tx.NextSeq(),
	})
}

// referenceItemID links a References item whose text names an item ID
// (FR-DIR-003, F4, SPEC-1.2) through an exact-key lookup: the named item is
// linked only if the source actor can access it and it is no narrower than
// the reference. A missing and an inaccessible item are indistinguishable:
// nothing is linked and nothing is reported either way.
func (r *run) referenceItemID(c unitCtx, ref domain.ContextItem) error {
	id := ref.Parts[0].Text
	if id == "" || len(id) > maxItemIDBytes || id == ref.ID {
		return nil
	}
	for i := range len(id) {
		if b := id[i]; b <= ' ' || b == 0x7f {
			return nil
		}
	}
	target, err := r.tx.Item(id)
	switch {
	case isNotFound(err) || errors.Is(err, domain.ErrIntegrity):
		// A row failing verification is reported before any access check,
		// so treating it as an error would reveal an item the actor may
		// not see; like a missing one, it is simply not linked.
		return nil
	case err != nil:
		return err
	}
	stop, err := r.linkReference(c.actor, ref, target)
	if err == nil && stop {
		r.truncatedLinks(c.si, c.pi, ref.SourceRanges[0].Range, ref.Access)
	}
	return err
}

// maxItemIDBytes bounds the text tried as an item ID.
const maxItemIDBytes = 256

// linkPendingReferences links every stored reference naming a newly
// ingested source (the span's transcript) to it, under both the
// reference's ownership context and this event's authorization (R2): the
// current source actor must see the reference, and everyone who can see the
// reference must already see the new source. Anything else is skipped
// silently, so the event learns nothing about references it cannot see.
// The references are paged from the store's exact-key index, filtered to
// the source actor inside the query (F1, SEC-1.1); linking stops, without
// failing the event, once the event's relationship budget is spent (D17).
func (r *run) linkPendingReferences(si int, actor domain.Principal, target domain.ContextItem) error {
	if target.Source == nil {
		return nil
	}
	key, ok := domain.LocatorKey(target.Source.Kind, target.Source.Locator)
	if !ok {
		return nil
	}
	var after store.Cursor
	for {
		refs, more, next, err := r.tx.VisibleReferences(store.VisibleReferenceFilter{Viewer: actor, LocatorKey: key, Page: store.Page{After: after, Limit: r.linkPageLimit()}})
		if err != nil {
			return err
		}
		for _, ref := range refs {
			it, err := r.tx.Item(ref.ItemID)
			switch {
			case errors.Is(err, domain.ErrIntegrity):
				r.reportUnverified(si, 0, domain.ByteRange{}, target.Access, []string{ref.ItemID})
				continue
			case err != nil:
				return err
			}
			stop, err := r.linkReference(actor, it, target)
			if err != nil {
				return err
			}
			if stop {
				r.truncatedLinks(si, 0, domain.ByteRange{}, target.Access)
				return nil
			}
		}
		if !more {
			return nil
		}
		after = next
	}
}

// linkReference writes one REFERENCES edge if actor can see both ends and
// the reference's boundary is within the target's; otherwise it writes
// nothing and reports nothing.
func (r *run) linkReference(actor domain.Principal, ref, target domain.ContextItem) (stop bool, err error) {
	// A candidate that could never be linked is skipped before the budget
	// is consulted, so truncation is reported only when a linkable link is
	// actually dropped (DUR-2.2).
	if !ref.Access.Permits(actor) || !target.Access.Permits(actor) || !ref.Access.Within(target.Access) {
		return false, nil
	}
	// Optional links spend the event's own MaxReferenceLinks budget, never
	// MaxRelationships (ruling 1); the receipt records it with the limits.
	if r.refLinks >= r.limits.MaxReferenceLinks {
		return true, nil
	}
	_, err = graph.LinkReference(r.tx, actor, ref.ID, target.ID, r.graphEventID(), domain.LocatorRuleVersion)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		return false, nil
	}
	if err == nil {
		r.refLinks++
	}
	return false, err
}

// linkPageLimit sizes one reference-lookup page by what the event can
// still use (DUR-2.1): the remaining MaxReferenceLinks budget plus one, so
// a dropped linkable candidate is still seen and reported as truncation,
// and never more than LookupLimit. A page therefore never loads thousands
// of items to write a handful of edges.
func (r *run) linkPageLimit() int {
	return max(1, min(r.g.lookupLimit(), r.limits.MaxReferenceLinks-r.refLinks+1))
}

// truncatedLinks records that an event stopped adding optional REFERENCES
// edges at its budget (ruling 1), on the span and range where it stopped,
// readable at access.
func (r *run) truncatedLinks(si, pi int, rng domain.ByteRange, access domain.AccessBoundary) {
	r.diags.add(domain.Diagnostic{SpanIndex: si, PartIndex: pi, Code: domain.ReferenceLinksTruncated,
		Reason: domain.ReasonReferenceLinksTruncated, Range: rng}, access)
}
