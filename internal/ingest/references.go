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

// declareReference persists an immutable unresolved reference for a new
// References item whose text is a locator, and links every already
// ingested source it names (M5, R2). The record is kept even when a source
// matched, since every later ingestion of a matching source may add an
// edge; references are never retired. Existing sources come from the
// store's bounded source-key index (R19); more than the lookup limit
// rejects the event (store.ErrLimitExceeded, D17).
func (r *run) declareReference(c unitCtx, ref domain.ContextItem) error {
	key, ok := referenceKey(ref.Parts[0].Text)
	if !ok {
		return nil
	}
	targets, err := r.tx.ItemsBySourceKey(key, r.g.lookupLimit())
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.ID == ref.ID {
			continue
		}
		if err := r.linkReference(c.actor, ref, target); err != nil {
			return err
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

// linkPendingReferences links every stored reference naming a newly
// ingested source (the span's transcript) to it, under both the
// reference's ownership context and this event's authorization (R2): the
// current source actor must see the reference, and everyone who can see the
// reference must already see the new source. Anything else is skipped
// silently, so the event learns nothing about references it cannot see.
// The references come from the store's bounded locator-key index (R19);
// more than the lookup limit rejects the event (store.ErrLimitExceeded,
// D17) rather than linking only some.
func (r *run) linkPendingReferences(actor domain.Principal, target domain.ContextItem) error {
	if target.Source == nil {
		return nil
	}
	key, ok := domain.LocatorKey(target.Source.Kind, target.Source.Locator)
	if !ok {
		return nil
	}
	refs, err := r.tx.UnresolvedReferences(store.ReferenceFilter{LocatorKey: key, RuleVersion: domain.LocatorRuleVersion, Limit: r.g.lookupLimit()})
	if err != nil {
		return err
	}
	for _, ref := range refs {
		it, err := r.tx.Item(ref.ItemID)
		if err != nil {
			return err
		}
		if err := r.linkReference(actor, it, target); err != nil {
			return err
		}
	}
	return nil
}

// linkReference writes one REFERENCES edge if actor can see both ends and
// the reference's boundary is within the target's; otherwise it writes
// nothing and reports nothing.
func (r *run) linkReference(actor domain.Principal, ref, target domain.ContextItem) error {
	if !ref.Access.Permits(actor) || !target.Access.Permits(actor) || !ref.Access.Within(target.Access) {
		return nil
	}
	if r.rels >= r.limits.MaxRelationships {
		return errLimit("MaxRelationships")
	}
	_, err := graph.LinkReference(r.tx, actor, ref.ID, target.ID, r.graphEventID(), domain.LocatorRuleVersion)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		return nil
	}
	if err == nil {
		r.rels++
	}
	return err
}
