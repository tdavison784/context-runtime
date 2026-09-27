package graph

import (
	"errors"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

const (
	// maxEvidenceProjectionDepth bounds the projection-of-projection chain
	// followed to an evidence source; a deeper chain fails closed.
	maxEvidenceProjectionDepth = 4
	// maxEvidenceMemberships bounds the exchange memberships read for one
	// tool result; an item named by more fails closed.
	maxEvidenceMemberships = 64
)

// qualifiesAsEvidenceSupport decides whether item may be cited as evidence
// SUPPORT (SEC-1.3, FR-DOM-006). Beyond the structural domain rule, a
// projection qualifies only through its exact source, and a TOOL tool_result
// transcript only with trusted provenance: a TOOL_RESULT exchange membership
// (registered by a HARNESS/SYSTEM dispatcher) naming its exact content, or
// ingestion by a HARNESS/SYSTEM principal. An AGENT can therefore never mint
// support by ingesting a TOOL event or re-projecting its own content.
func qualifiesAsEvidenceSupport(tx store.ReadTx, item domain.ContextItem) (bool, error) {
	for depth := 0; ; depth++ {
		if !item.QualifiesAsEvidenceSupport() {
			return false, nil
		}
		switch item.Role {
		case domain.RoleProjection:
			if depth >= maxEvidenceProjectionDepth {
				return false, nil
			}
			source, ok, err := projectionSource(tx, item)
			if err != nil || !ok {
				return false, err
			}
			item = source
		case domain.RoleTranscript:
			return trustedToolResult(tx, item)
		default:
			return true, nil
		}
	}
}

func projectionSource(tx store.ReadTx, item domain.ContextItem) (domain.ContextItem, bool, error) {
	r, err := store.ReadSemantic(tx)
	if err != nil {
		return domain.ContextItem{}, false, err
	}
	p, err := r.ProjectionByItem(item.ID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ContextItem{}, false, nil
	}
	if err != nil {
		return domain.ContextItem{}, false, err
	}
	if p.ItemID != item.ID || p.SessionID != item.SessionID {
		return domain.ContextItem{}, false, domain.ErrIntegrity
	}
	source, err := tx.Item(p.Source.ItemID)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ContextItem{}, false, nil
	}
	if err != nil {
		return domain.ContextItem{}, false, err
	}
	if source.SessionID != item.SessionID || source.ContentHash != p.Source.ContentHash {
		return domain.ContextItem{}, false, domain.ErrIntegrity
	}
	return source, true, nil
}

func trustedToolResult(tx store.ReadTx, item domain.ContextItem) (bool, error) {
	r, err := store.ReadSemantic(tx)
	if err != nil {
		return false, err
	}
	page, err := r.MembershipsByItem(item.ID, store.Page{Limit: maxEvidenceMemberships})
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return false, err
	}
	for _, m := range page.Records {
		if m.Role == domain.MemberToolResult && m.Source.ItemID == item.ID && m.Source.ContentHash == item.ContentHash {
			return true, nil
		}
	}
	if item.EventID == "" {
		return false, nil
	}
	receipt, err := tx.Receipt(domain.CallerOccurrenceID(item.SessionID, item.EventID))
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	trusted := receipt.Principal.Authority == domain.AuthorityHarness || receipt.Principal.Authority == domain.AuthoritySystem
	return trusted && receipt.SessionID == item.SessionID && slices.ContainsFunc(receipt.Items, func(it domain.ContextItem) bool {
		return it.ID == item.ID && it.ContentHash == item.ContentHash
	}), nil
}
