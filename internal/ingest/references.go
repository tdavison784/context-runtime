package ingest

import (
	"errors"
	"path"
	"strings"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ReferenceRuleVersion names locator identity rule v1 (M5, R2). A locator
// key is purely lexical; nothing is fetched, opened, or resolved:
//
//   - a URL (scheme "://" rest, scheme = ALPHA *(ALPHA / DIGIT / "+" / "-"
//     / ".")) keys as "url:" + its exact bytes;
//   - anything else is a path: it must be relative and must not escape its
//     base after lexical cleaning (path.Clean), and keys as "path:" + the
//     cleaned path, so "./docs//a.md" and "docs/a.md" match;
//   - empty text, whitespace or control bytes, backslashes, absolute paths,
//     and escaping paths are not locators and never link.
//
// The repository/resource namespace and base directory are both the
// session's single default in v1: the embedding API supplies no namespace
// on SourceRef yet, so two repositories ingested into one session would
// share path keys. That is a documented v1 limit, not a guess.
const ReferenceRuleVersion = "reference-locator/v1"

// LocatorKey returns the rule-v1 key of a locator of the given kind, or
// ok=false when it is not a linkable locator.
func LocatorKey(kind domain.SourceKind, locator string) (string, bool) {
	if locator == "" || len(locator) > domain.MaxLocatorKeyBytes-5 {
		return "", false
	}
	for i := range len(locator) {
		if c := locator[i]; c <= ' ' || c == 0x7f || c == '\\' {
			return "", false
		}
	}
	switch kind {
	case domain.SourceURL:
		if !isURL(locator) {
			return "", false
		}
		return "url:" + locator, true
	case domain.SourcePath:
		if strings.HasPrefix(locator, "/") || isURL(locator) {
			return "", false
		}
		clean := path.Clean(locator)
		if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
			return "", false
		}
		return "path:" + clean, true
	}
	return "", false
}

// referenceKey returns the key of a References item's text: a URL if it
// looks like one, else a path.
func referenceKey(text string) (string, bool) {
	if isURL(text) {
		return LocatorKey(domain.SourceURL, text)
	}
	return LocatorKey(domain.SourcePath, text)
}

func isURL(s string) bool {
	scheme, _, ok := strings.Cut(s, "://")
	if !ok || scheme == "" {
		return false
	}
	for i := range len(scheme) {
		c := scheme[i]
		alpha := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
		if !alpha && (i == 0 || !(c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.')) {
			return false
		}
	}
	return true
}

// declareReference persists an immutable unresolved reference for a new
// References item whose text is a locator, and links every already
// ingested source it names (M5, R2). The record is kept even when a source
// matched, since every later ingestion of a matching source may add an
// edge; references are never retired.
func (r *run) declareReference(c unitCtx, ref domain.ContextItem) error {
	key, ok := referenceKey(ref.Parts[0].Text)
	if !ok {
		return nil
	}
	items, err := r.tx.Items(store.ItemFilter{})
	if err != nil {
		return err
	}
	for _, target := range items {
		if target.Source == nil || target.ID == ref.ID {
			continue
		}
		if k, ok := LocatorKey(target.Source.Kind, target.Source.Locator); ok && k == key {
			if err := r.linkReference(c.actor, ref, target); err != nil {
				return err
			}
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
		RuleVersion:  ReferenceRuleVersion,
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
	key, ok := LocatorKey(target.Source.Kind, target.Source.Locator)
	if !ok {
		return nil
	}
	refs, err := r.tx.UnresolvedReferences(store.ReferenceFilter{LocatorKey: key, RuleVersion: ReferenceRuleVersion, Limit: r.g.lookupLimit()})
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
	_, err := graph.LinkReference(r.tx, actor, ref.ID, target.ID, r.graphEventID(), ReferenceRuleVersion)
	if errors.Is(err, domain.ErrNotFound) || errors.Is(err, domain.ErrInvalidAuthorityPromotion) {
		return nil
	}
	if err == nil {
		r.rels++
	}
	return err
}
