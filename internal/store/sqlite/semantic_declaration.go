package sqlite

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Creation and snapshot declarations (P3-4), the current-pointer CAS
// (P3-3), exact grant reads (P3-5), and semantic change records (P3-36),
// with the memory store's rules (shared storetest cases).

func (s semTx) InsertCreationDeclaration(d domain.CreationDeclaration) error {
	t := s.t
	if err := t.companion(d.SemanticMeta, d.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("creation_declaration", d.ItemID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "creation declaration of item", d.ItemID))
	}
	var prior domain.CreationDeclaration
	if err := t.getWhere("creation_declaration", "f_id=?", &prior, d.ID); err == nil {
		return immutable("creation declaration", d.ID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	it, err := t.loadItem(d.ItemID, false)
	if err != nil {
		return notStored(err, "creation declaration %s: item %s is not stored", d.ID, d.ItemID)
	}
	if d.LegacyKnown {
		if err := it.ValidateSemantic(); err != nil {
			return invalid("creation declaration %s: item %s: %v", d.ID, d.ItemID, err)
		}
		if !creationMatches(it, d.AcceptedSemantics) {
			return invalid("creation declaration %s: semantics disagree with item %s", d.ID, d.ItemID)
		}
		for _, id := range d.AcceptedSemantics.SupportIDs {
			ok, err := t.exists("item", id)
			if err != nil {
				return err
			}
			if !ok {
				return invalid("creation declaration %s: support source %s is not stored", d.ID, id)
			}
		}
	}
	return t.put("creation_declaration", d.ItemID, 0, d, false)
}

// creationMatches reports whether s restates the item's creation fields.
func creationMatches(it domain.ContextItem, s domain.CreationSemantics) bool {
	key, ok := it.CurrentKey()
	return ok && key == s.Key && it.Authority == s.Authority && it.WorkflowID == s.WorkflowID && it.AgentID == s.AgentID &&
		it.Section == s.Section && it.Kind == s.Kind && it.ContentHash == s.ContentHash && it.Generation == s.Generation &&
		it.Retention == s.Retention && it.Residency == s.Residency && samePtr(it.GoalStatus, s.GoalStatus) &&
		it.TaskID == s.OriginTaskID && it.TurnID == s.OriginTurnID && it.CreatedTurn == s.CreatedTurn && samePtr(it.TTLTurns, s.TTLTurns)
}

func samePtr[T comparable](a, b *T) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func (s semRead) CreationDeclaration(itemID string) (domain.CreationDeclaration, error) {
	var d domain.CreationDeclaration
	return d, s.t.get("creation_declaration", itemID, 0, &d)
}

func (s semTx) InsertSnapshotDeclaration(sd domain.SnapshotDeclaration) error {
	t := s.t
	if err := t.companion(sd.SemanticMeta, sd.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("snapshot_declaration", sd.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "snapshot declaration", sd.ID))
	}
	for _, m := range sd.Members {
		var d domain.CreationDeclaration
		if err := t.get("creation_declaration", m.ItemID, 0, &d); err != nil || d.ID != m.DeclarationID || d.Signature != m.Signature || !d.LegacyKnown {
			return notStored(errors.Join(err, domain.ErrNotFound), "snapshot declaration %s: member %s does not name its item's known declaration", sd.ID, m.ItemID)
		}
		it, err := t.loadItem(m.ItemID, false)
		if err != nil {
			return err
		}
		if it.TaskID != sd.TaskID || it.Authority != sd.Authority || it.Access != sd.Access {
			return invalid("snapshot declaration %s: member %s lies outside the snapshot partition", sd.ID, m.ItemID)
		}
	}
	return t.put("snapshot_declaration", sd.ID, 0, sd, false)
}

func (s semRead) SnapshotDeclaration(id string) (domain.SnapshotDeclaration, error) {
	var sd domain.SnapshotDeclaration
	return sd, s.t.get("snapshot_declaration", id, 0, &sd)
}

// SetCurrentVersion is the Phase 3 current-pointer CAS (P3-3); see the
// memory store for the rules.
func (s semTx) SetCurrentVersion(itemID, expectedPriorItemID string) error {
	t := s.t
	it, err := t.Item(itemID)
	if err != nil {
		return err
	}
	if err := it.ValidateSemantic(); err != nil {
		return invalid("item %s: %v", itemID, err)
	}
	key, ok := it.CurrentKey()
	if !ok {
		return invalid("item %s has no current key", itemID)
	}
	if err := key.Validate(); err != nil {
		return invalid("item %s: %v", itemID, err)
	}
	if expectedPriorItemID == itemID {
		return transition("item %s is already its expected prior", itemID)
	}
	for _, c := range []struct{ typ, col string }{{string(domain.RelSupersedes), "f_to_id"}, {string(domain.RelDuplicateOf), "f_from_id"}} {
		var one int
		err := t.conn.QueryRowContext(t.ctx, "SELECT 1 FROM rec_relationship WHERE session_id=? AND f_type=? AND "+c.col+"=? LIMIT 1", t.session, c.typ, itemID).Scan(&one)
		if err == nil {
			return transition("item %s is superseded or a duplicate occurrence", itemID)
		}
		if !isNoRows(err) {
			return err
		}
	}
	cur, err := t.current(key.TaskID, key.ID, key.Access, key.Namespace)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if cur != expectedPriorItemID {
		return fmt.Errorf("current version of %s: pointer names %q, expected %q: %w", key.ID, cur, expectedPriorItemID, domain.ErrVersionConflict)
	}
	return t.SetCurrentVersion(itemID)
}

// grantTargetKeys are the lookup_grant_target keys of a grant (migration
// 0021): typed targets by canonical authorization key, legacy TargetIDs by
// item ID, each as the hex of the stored string.
func typedGrantKey(authorizationKey string) string {
	return "typed:" + hex.EncodeToString([]byte(authorizationKey))
}
func legacyGrantKey(itemID string) string { return "legacy-item:" + hex.EncodeToString([]byte(itemID)) }

// indexGrant adds g under every exact target it names.
func (t *transaction) indexGrant(g domain.MutationGrant) error {
	var keys []string
	for _, target := range g.Targets {
		keys = append(keys, typedGrantKey(target.AuthorizationKey))
	}
	for _, id := range g.TargetIDs {
		keys = append(keys, legacyGrantKey(id))
	}
	for _, k := range keys {
		if _, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_grant_target(session_id,action,target_key,issued_seq,grant_id) VALUES(?,?,?,?,?)",
			t.session, string(g.Action), k, g.IssuedSeq, g.ID); err != nil {
			return err
		}
	}
	return nil
}

// GrantsFor returns every grant naming action on exactly target, in
// (IssuedSeq, ID) order, or store.ErrLimitExceeded beyond limit.
func (s semRead) GrantsFor(action domain.Action, target domain.GrantTarget, limit int) ([]domain.MutationGrant, error) {
	t := s.t
	if limit <= 0 {
		return nil, invalid("grant lookup limit must be positive")
	}
	if err := target.Validate(); err != nil {
		return nil, invalid("grant lookup: %v", err)
	}
	if target.SessionID != t.session {
		return nil, invalid("grant lookup: target belongs to another session")
	}
	keys := []any{typedGrantKey(target.AuthorizationKey)}
	if target.Kind == domain.GrantTargetItem && action.ValidForTarget(domain.GrantTargetItem) {
		keys = append(keys, legacyGrantKey(target.ItemID))
	} else {
		keys = append(keys, keys[0])
	}
	rows, err := t.query("SELECT grant_id FROM lookup_grant_target WHERE session_id=? AND action=? AND target_key IN (?,?) ORDER BY issued_seq, grant_id LIMIT ?",
		t.session, string(action), keys[0], keys[1], limit+1)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return nil, err
	}
	if len(ids) > limit {
		return nil, store.ErrLimitExceeded
	}
	out := make([]domain.MutationGrant, 0, len(ids))
	for _, id := range ids {
		g, err := t.Grant(id)
		if err != nil {
			return nil, fmt.Errorf("%w: grant index names missing grant %s", domain.ErrIntegrity, id)
		}
		out = append(out, g)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// --- Semantic changes and audit reads by target ---

func (s semRead) LifecycleByTarget(kind domain.TargetKind, targetID string, p store.Page) (store.ResultPage[domain.LifecycleEvent], error) {
	return pageQuery[domain.LifecycleEvent](s.t, "lifecycle", "f_target_kind=? AND f_target_id=?", []any{string(kind), targetID}, "f_seq", p, false, nil)
}

func (s semTx) InsertSemanticChange(c domain.SemanticChange) error {
	t := s.t
	if err := t.companion(c.SemanticMeta, c.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("semantic_change", c.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "semantic change", c.ID))
	}
	var kind domain.TargetKind
	var id string
	var stored bool
	var err error
	switch c.Target.Kind {
	case domain.GrantTargetItem:
		kind, id = domain.TargetItem, c.Target.ItemID
		stored, err = t.exists("item", id)
	case domain.GrantTargetObligation:
		kind, id = domain.TargetObligation, c.Target.ObligationID
		var one int
		err = t.conn.QueryRowContext(t.ctx, "SELECT 1 FROM rec_obligation WHERE session_id=? AND id=? AND subkey=?", t.session, id, c.Target.Version).Scan(&one)
		stored = err == nil
		if isNoRows(err) {
			err = nil
		}
	}
	if err != nil {
		return err
	}
	if !stored {
		return invalid("semantic change %s: target is not stored", c.ID)
	}
	var e domain.LifecycleEvent
	if err := t.get("lifecycle", c.AuditID, 0, &e); err != nil || e.TargetKind != kind || e.TargetID != id {
		return notStored(errors.Join(err, domain.ErrNotFound), "semantic change %s: audit %s is not a stored audit of its target", c.ID, c.AuditID)
	}
	if c.GrantID != "" {
		ok, err := t.exists("grant", c.GrantID)
		if err != nil || !ok {
			return errors.Join(err, invalidIf(!ok, "semantic change %s: grant %s is not stored", c.ID, c.GrantID))
		}
	}
	if c.CauseID != "" {
		stored := false
		for _, kind := range []string{"obligation_transition", "resource_update", "observation", "lifecycle", "mutation_receipt"} {
			ok, err := t.exists(kind, c.CauseID)
			if err != nil {
				return err
			}
			stored = stored || ok
		}
		if !stored {
			return invalid("semantic change %s: cause %s is not a stored causal record", c.ID, c.CauseID)
		}
	}
	return t.put("semantic_change", c.ID, 0, c, false)
}

func (s semRead) SemanticChanges(viewer domain.Principal, target domain.GrantTarget, p store.Page) (store.ResultPage[domain.SemanticChange], error) {
	if err := viewer.Validate(); err != nil {
		return store.ResultPage[domain.SemanticChange]{}, err
	}
	if err := target.Validate(); err != nil {
		return store.ResultPage[domain.SemanticChange]{}, invalid("semantic changes: %v", err)
	}
	return pageQuery(s.t, "semantic_change", "f_target_authorization_key=?", []any{target.AuthorizationKey}, "f_seq", p, false, func(c domain.SemanticChange) (bool, error) {
		return c.Access.Permits(viewer), nil
	})
}
