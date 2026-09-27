package memory

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// grantKey indexes a grant under one exact (action, target) it names: a
// typed target's canonical authorization key, or legacyItemKey of an item
// ID in a legacy TargetIDs list (P3-5).
type grantKey struct {
	action domain.Action
	target string
}

// legacyItemKey is the index key of a legacy occurrence grant's raw item
// ID. It is never a canonical authorization key (those are hashes), so the
// two forms cannot alias.
func legacyItemKey(itemID string) string { return "legacy-item:" + itemID }

// indexGrant adds g under every exact target it names. A legacy grant's
// TargetIDs are indexed as item occurrences only: a stable obligation ID
// never authorizes an exact obligation version, which GrantsFor enforces by
// consulting legacy entries for item targets alone.
func (t *tx) indexGrant(g domain.MutationGrant) {
	ref := seqRef{g.IssuedSeq, g.ID}
	for _, target := range g.Targets {
		t.sem.grantIdx.add(grantKey{g.Action, target.AuthorizationKey}, ref)
	}
	for _, id := range g.TargetIDs {
		t.sem.grantIdx.add(grantKey{g.Action, legacyItemKey(id)}, ref)
	}
}

// GrantsFor returns every grant, live or not, that names action on exactly
// target, in (IssuedSeq, ID) order. More than limit matches fail with
// store.ErrLimitExceeded, so an authorization check never sees a partial
// set. Liveness at a sequence is the caller's check.
func (r semRead) GrantsFor(action domain.Action, target domain.GrantTarget, limit int) ([]domain.MutationGrant, error) {
	return r.grantsFor(action, target, limit, func(domain.MutationGrant) bool { return true })
}

// LiveGrantsFor is GrantsFor over the grants in force at seq only, so
// dead history never counts toward limit (G2, SEC-1.5, DUR-1.4).
func (r semRead) LiveGrantsFor(action domain.Action, target domain.GrantTarget, seq uint64, limit int) ([]domain.MutationGrant, error) {
	return r.grantsFor(action, target, limit, func(g domain.MutationGrant) bool { return store.GrantLiveAt(g, seq) })
}

func (r semRead) grantsFor(action domain.Action, target domain.GrantTarget, limit int, keep func(domain.MutationGrant) bool) ([]domain.MutationGrant, error) {
	if err := r.r.check(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, invalid("grant lookup limit must be positive")
	}
	if err := target.Validate(); err != nil {
		return nil, invalid("grant lookup: %v", err)
	}
	if target.SessionID != r.r.sessionID {
		return nil, invalid("grant lookup: target belongs to another session")
	}
	keys := []grantKey{{action, target.AuthorizationKey}}
	if target.Kind == domain.GrantTargetItem && action.ValidForTarget(domain.GrantTargetItem) {
		keys = append(keys, grantKey{action, legacyItemKey(target.ItemID)})
	}
	var out []domain.MutationGrant
	for ref := range mergeAfter(&r.r.sem.grantIdx, keys, seqRef{}) {
		g, ok := r.r.grants.get(ref.id)
		if !ok {
			return nil, domain.ErrIntegrity
		}
		if !keep(g) {
			continue
		}
		if len(out) == limit {
			return nil, store.ErrLimitExceeded
		}
		out = append(out, g)
	}
	return out, nil
}
