package memory

import (
	"github.com/tdavison784/context-runtime/internal/domain"
)

// Immutable creation and Working snapshot declarations (P3-4). A known
// creation declaration must restate its stored item's own creation fields
// exactly, so a caller cannot declare an identity the item never had; the
// accepted attributes, obligation declaration and support set come from the
// service, and each support source must be stored.

func (t *semTx) InsertCreationDeclaration(d domain.CreationDeclaration) error {
	if err := t.t.companion("creation declaration", d.SemanticMeta, d.Validate); err != nil {
		return err
	}
	if t.r.sem.decls.has(d.ItemID) || t.r.sem.declIDs.has(d.ID) {
		return immutable("creation declaration", d.ID)
	}
	it, ok := t.r.items.peek(d.ItemID)
	if !ok {
		return invalid("creation declaration %s: item %s is not stored", d.ID, d.ItemID)
	}
	if d.LegacyKnown {
		if err := it.ValidateSemantic(); err != nil {
			return invalid("creation declaration %s: item %s: %v", d.ID, d.ItemID, err)
		}
		if !creationMatches(it, d.AcceptedSemantics) {
			return invalid("creation declaration %s: semantics disagree with item %s", d.ID, d.ItemID)
		}
		for _, id := range d.AcceptedSemantics.SupportIDs {
			if !t.r.items.has(id) {
				return invalid("creation declaration %s: support source %s is not stored", d.ID, id)
			}
		}
	}
	t.r.sem.decls.put(d.ItemID, d)
	t.r.sem.declIDs.put(d.ID, d.ItemID)
	t.t.sequencedWrite(d.Seq)
	return nil
}

// creationMatches reports whether s restates the item's creation fields: its
// current key, authority, owners, section, kind, content, creation
// defaults, and eligibility origin.
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

func (r semRead) CreationDeclaration(itemID string) (domain.CreationDeclaration, error) {
	if err := r.r.check(); err != nil {
		return domain.CreationDeclaration{}, err
	}
	d, ok := r.r.sem.decls.get(itemID)
	if !ok {
		return d, notFound("creation declaration of item", itemID)
	}
	return d, nil
}

// InsertSnapshotDeclaration stores an ordered Working snapshot identity.
// Each member names its item's stored creation declaration and that
// declaration's signature, and every member item lies in the snapshot's
// task, authority, and access partition.
func (t *semTx) InsertSnapshotDeclaration(sd domain.SnapshotDeclaration) error {
	if err := t.t.companion("snapshot declaration", sd.SemanticMeta, sd.Validate); err != nil {
		return err
	}
	if t.r.sem.snapshots.has(sd.ID) {
		return immutable("snapshot declaration", sd.ID)
	}
	for _, m := range sd.Members {
		d, ok := t.r.sem.decls.peek(m.ItemID)
		if !ok || d.ID != m.DeclarationID || d.Signature != m.Signature || !d.LegacyKnown {
			return invalid("snapshot declaration %s: member %s does not name its item's known declaration", sd.ID, m.ItemID)
		}
		it, _ := t.r.items.peek(m.ItemID)
		if it.TaskID != sd.TaskID || it.Authority != sd.Authority || it.Access != sd.Access {
			return invalid("snapshot declaration %s: member %s lies outside the snapshot partition", sd.ID, m.ItemID)
		}
	}
	t.r.sem.snapshots.put(sd.ID, sd)
	t.t.sequencedWrite(sd.Seq)
	return nil
}

func (r semRead) SnapshotDeclaration(id string) (domain.SnapshotDeclaration, error) {
	if err := r.r.check(); err != nil {
		return domain.SnapshotDeclaration{}, err
	}
	sd, ok := r.r.sem.snapshots.get(id)
	if !ok {
		return sd, notFound("snapshot declaration", id)
	}
	return sd, nil
}
