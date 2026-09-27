package lifecycle

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

func observe(it domain.ContextItem, current domain.ItemCurrentness) domain.ObservedItemState {
	// Lifecycle mutation does not perform model admission or prove complete
	// representation lifetime. Preserve that uncertainty explicitly in its audit.
	return (domain.ObservedItemState{Source: domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash}, Version: it.Version,
		Currentness: current, GoalStatus: it.GoalStatus, Generation: it.Generation, Residency: it.Residency, Authority: it.Authority, Expiry: domain.ExpiryUnknown}).Clone()
}

func (e itemEffect) result() domain.ItemMutationResult {
	return domain.ItemMutationResult{ItemID: e.before.ID, BeforeVersion: e.before.Version, AfterVersion: e.after.Version,
		Before: observe(e.before, e.current), After: observe(e.after, e.current), AuditID: e.audit.ID, ExplicitProtectedRemoval: e.protectedRemoval}
}

func recordEffect(tx store.Tx, sem store.SemanticTx, e itemEffect) error {
	current := e.current
	return sem.InsertSemanticChange(domain.SemanticChange{
		SemanticMeta: domain.SemanticMeta{ID: changeID(e.audit.ID), SessionID: e.before.SessionID, Seq: tx.NextSeq(), SchemaVersion: domain.SemanticSchemaV1},
		Target:       domain.ItemGrantTarget(e.before.SessionID, e.before.ID), SourceAuthority: e.before.Authority, Actor: e.audit.Actor,
		Access: e.before.Access, Action: domain.Action(e.audit.Action), BeforeRevision: e.before.Version, AfterRevision: e.after.Version,
		BeforeStatus: e.audit.From, AfterStatus: e.audit.To, BeforeCurrentness: current, AfterCurrentness: current, AuditID: e.audit.ID, GrantID: e.audit.GrantID})
}

func changeID(auditID string) string {
	return "change_" + domain.NewCanonicalEncoder("context-runtime/lifecycle-change/v1").String(auditID).Hash()
}
