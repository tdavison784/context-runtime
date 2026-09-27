package obligation

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// appendTransition applies one obligation status transition with its audit
// event and immutable semantic change record (P3-36), atomically: history,
// detail, status and proof caches (store CAS), then a TargetObligation audit
// and a SemanticChange naming the exact version, its source authority, the
// actual actor, old and new revision and status, the cause record, and any
// live grant. Later materialization reads these records rather than
// reconstructing requirement state from receipts or raw text.
func appendTransition(tx store.Tx, sem store.SemanticTx, o domain.ObligationVersion, t domain.ObligationTransition, d domain.TransitionDetail, expected uint64) (domain.ObligationVersion, error) {
	after, err := sem.AppendSemanticObligationTransition(t, d, expected)
	if err != nil {
		return domain.ObligationVersion{}, err
	}
	ev := domain.LifecycleEvent{
		ID:         recordID("oev_", "transition", t.ID),
		SessionID:  t.SessionID,
		Seq:        t.Seq,
		TargetKind: domain.TargetObligation,
		TargetID:   t.ObligationID,
		Action:     string(t.Action),
		From:       string(t.From),
		To:         string(t.To),
		Actor:      t.Actor,
		GrantID:    t.GrantID,
		EventID:    t.RequestID,
		Reason:     string(t.Cause),
	}
	if err := tx.AppendLifecycleEvent(ev); err != nil {
		return domain.ObligationVersion{}, err
	}
	// The cause names a stored record (P3-36): the resource update or
	// rejecting observation of a runtime consequence, the evaluated
	// observation of a matcher step, or else the transition itself.
	cause := t.ID
	switch {
	case t.CauseRecordID != "":
		cause = t.CauseRecordID
	case t.Cause == domain.CauseMatcher || t.Cause == domain.CauseProofRefresh:
		cause = t.RequestID // the observation that was evaluated
	}
	ref := domain.ObligationRef{SessionID: t.SessionID, ObligationID: t.ObligationID, Version: t.Version}
	err = sem.InsertSemanticChange(domain.SemanticChange{
		SemanticMeta:      domain.SemanticMeta{ID: recordID("chg_", "obligation-transition", t.ID), SessionID: t.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: t.Seq},
		Target:            ref.Target(),
		SourceAuthority:   o.SourceAuthority,
		Actor:             t.Actor,
		Access:            o.Access,
		Action:            t.Action,
		Cause:             t.Cause,
		BeforeRevision:    expected,
		AfterRevision:     after.Revision,
		BeforeStatus:      string(t.From),
		AfterStatus:       string(t.To),
		BeforeCurrentness: domain.ItemCurrent,
		AfterCurrentness:  domain.ItemCurrent,
		AuditID:           ev.ID,
		CauseID:           cause,
		GrantID:           t.GrantID,
	})
	if err != nil {
		return domain.ObligationVersion{}, err
	}
	return after, nil
}
