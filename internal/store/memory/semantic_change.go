package memory

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Semantic change records (P3-36) and the indexed audit read by target.

type lifecycleKey struct {
	kind   domain.TargetKind
	target string
}

// putLifecycle stores an audit event and indexes it by its target.
func (t *tx) putLifecycle(e domain.LifecycleEvent) {
	t.lifecycle.put(e.ID, e)
	t.sem.lcByTarget.add(lifecycleKey{e.TargetKind, e.TargetID}, seqRef{e.Seq, e.ID})
}

func (r semRead) LifecycleByTarget(kind domain.TargetKind, targetID string, p store.Page) (store.ResultPage[domain.LifecycleEvent], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.LifecycleEvent]{}, err
	}
	return page(p, r.r.sem.lcByTarget.after(lifecycleKey{kind, targetID}, cursorRef(p.After)), loadAll(&r.r.lifecycle, ident))
}

// InsertSemanticChange stores an immutable causal change record. Its target
// (an item occurrence or exact obligation version) and its audit event,
// which must audit that same target, are stored in this session; a named
// grant and cause must be stored too.
func (t *semTx) InsertSemanticChange(c domain.SemanticChange) error {
	if err := t.t.companion("semantic change", c.SemanticMeta, c.Validate); err != nil {
		return err
	}
	if t.r.sem.changes.has(c.ID) {
		return immutable("semantic change", c.ID)
	}
	kind, id, ok := t.targetStored(c.Target)
	if !ok {
		return invalid("semantic change %s: target is not stored", c.ID)
	}
	if e, ok := t.r.lifecycle.peek(c.AuditID); !ok || e.TargetKind != kind || e.TargetID != id {
		return invalid("semantic change %s: audit %s is not a stored audit of its target", c.ID, c.AuditID)
	}
	if c.GrantID != "" && !t.r.grants.has(c.GrantID) {
		return invalid("semantic change %s: grant %s is not stored", c.ID, c.GrantID)
	}
	if c.CauseID != "" && !t.causeStored(c.CauseID) {
		return invalid("semantic change %s: cause %s is not a stored causal record", c.ID, c.CauseID)
	}
	t.r.sem.changes.put(c.ID, c)
	t.r.sem.chByTarget.add(c.Target.AuthorizationKey, seqRef{c.Seq, c.ID})
	t.t.sequencedWrite(c.Seq)
	return nil
}

// targetStored resolves an exact grant target to its audit target kind and
// ID, reporting whether it is stored in this session.
func (t *semTx) targetStored(target domain.GrantTarget) (domain.TargetKind, string, bool) {
	switch target.Kind {
	case domain.GrantTargetItem:
		return domain.TargetItem, target.ItemID, t.r.items.has(target.ItemID)
	case domain.GrantTargetObligation:
		return domain.TargetObligation, target.ObligationID, t.r.obligations.has(obligationKey{target.ObligationID, target.Version})
	}
	return "", "", false
}

// SemanticChanges pages a target's changes in (Seq, ID) order, returning
// only those whose access boundary permits viewer, filtered before the limit.
func (r semRead) SemanticChanges(viewer domain.Principal, target domain.GrantTarget, p store.Page) (store.ResultPage[domain.SemanticChange], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.SemanticChange]{}, err
	}
	if err := viewer.Validate(); err != nil {
		return store.ResultPage[domain.SemanticChange]{}, err
	}
	if err := target.Validate(); err != nil {
		return store.ResultPage[domain.SemanticChange]{}, invalid("semantic changes: %v", err)
	}
	return page(p, r.r.sem.chByTarget.after(target.AuthorizationKey, cursorRef(p.After)), func(id string) (domain.SemanticChange, bool) {
		c, ok := r.r.sem.changes.get(id)
		return c, ok && c.Access.Permits(viewer)
	})
}

// causeStored reports whether id names a stored causal record: a
// transition, resource update, observation, audit event, or mutation
// receipt.
func (t *semTx) causeStored(id string) bool {
	s := &t.r.sem
	return t.r.transitions.has(id) || s.res.updates.has(id) || s.res.observations.has(id) || t.r.lifecycle.has(id) || s.mutReceipts.has(id)
}
