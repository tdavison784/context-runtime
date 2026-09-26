package store

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// ErrPoisoned is the poison error of Tx.Poison(nil).
var ErrPoisoned = errors.New("store: transaction poisoned")

// Guard implements Tx.Poison over a store's TxBase (DUR-1.3). Every write
// method checks the poison first; reads, NextSeq, and Allocated pass
// through. Stores call NewGuard in Update and, after fn returns, roll back
// and return Poisoned() when it is set. A conformance test calls every
// write method of Tx on a poisoned Guard, so a write added to TxBase
// without an override here fails it.
type Guard struct {
	TxBase
	err   error
	wrote bool
}

var _ Tx = (*Guard)(nil)

// NewGuard wraps tx.
func NewGuard(tx TxBase) *Guard { return &Guard{TxBase: tx} }

// Poison implements Tx.
func (g *Guard) Poison(err error) {
	if g.err != nil {
		return
	}
	if err == nil {
		err = ErrPoisoned
	}
	g.err = err
}

// Poisoned returns the poison error, or nil.
func (g *Guard) Poisoned() error { return g.err }

func (g *Guard) InsertEvent(e domain.EventRecord) (domain.EventRecord, bool, error) {
	if g.err != nil {
		return domain.EventRecord{}, false, g.err
	}
	return g.TxBase.InsertEvent(e)
}

func (g *Guard) InsertIngestion(env domain.EventEnvelope, r domain.IngestReceipt) error {
	if g.err != nil {
		return g.err
	}
	return g.TxBase.InsertIngestion(env, r)
}

func (g *Guard) InsertUnresolvedReference(r domain.UnresolvedReference) error {
	if g.err != nil {
		return g.err
	}
	return g.TxBase.InsertUnresolvedReference(r)
}

func (g *Guard) InsertItem(it domain.ContextItem) error {
	if g.err != nil {
		return g.err
	}
	return g.TxBase.InsertItem(it)
}

func (g *Guard) UpdateItem(id string, expectedVersion uint64, change domain.ItemChange, event domain.LifecycleEvent) (domain.ContextItem, error) {
	if g.err != nil {
		return domain.ContextItem{}, g.err
	}
	return g.TxBase.UpdateItem(id, expectedVersion, change, event)
}

func (g *Guard) InsertRelationship(r domain.Relationship) error {
	if g.err != nil {
		return g.err
	}
	return g.TxBase.InsertRelationship(r)
}

func (g *Guard) SetCurrentVersion(itemID string) error {
	if g.err != nil {
		return g.err
	}
	return g.TxBase.SetCurrentVersion(itemID)
}

func (g *Guard) InsertBlob(b domain.Blob) error {
	if g.err != nil {
		return g.err
	}
	return g.TxBase.InsertBlob(b)
}

func (g *Guard) InsertObligationVersion(o domain.ObligationVersion) error {
	if g.err != nil {
		return g.err
	}
	return g.TxBase.InsertObligationVersion(o)
}

func (g *Guard) UpdateObligationVersion(o domain.ObligationVersion, expectedRevision uint64) (domain.ObligationVersion, error) {
	if g.err != nil {
		return domain.ObligationVersion{}, g.err
	}
	return g.TxBase.UpdateObligationVersion(o, expectedRevision)
}

func (g *Guard) RetireObligationVersion(obligationID string, version, expectedRevision uint64, event domain.LifecycleEvent) (domain.ObligationVersion, error) {
	if g.err != nil {
		return domain.ObligationVersion{}, g.err
	}
	return g.TxBase.RetireObligationVersion(obligationID, version, expectedRevision, event)
}

func (g *Guard) AppendObligationTransition(t domain.ObligationTransition, expectedRevision uint64) (domain.ObligationVersion, error) {
	if g.err != nil {
		return domain.ObligationVersion{}, g.err
	}
	return g.TxBase.AppendObligationTransition(t, expectedRevision)
}

func (g *Guard) InsertGrant(gr domain.MutationGrant) error {
	if g.err != nil {
		return g.err
	}
	return g.TxBase.InsertGrant(gr)
}

func (g *Guard) RevokeGrant(id string, event domain.LifecycleEvent) (domain.MutationGrant, error) {
	if g.err != nil {
		return domain.MutationGrant{}, g.err
	}
	return g.TxBase.RevokeGrant(id, event)
}

func (g *Guard) PutTask(t domain.TaskState, expectedVersion uint64, event domain.LifecycleEvent) (domain.TaskState, error) {
	if g.err != nil {
		return domain.TaskState{}, g.err
	}
	return g.TxBase.PutTask(t, expectedVersion, event)
}

func (g *Guard) AppendLifecycleEvent(e domain.LifecycleEvent) error {
	if g.err != nil {
		return g.err
	}
	return g.TxBase.AppendLifecycleEvent(e)
}

func (g *Guard) PutConversation(c domain.Conversation, expectedRevision uint64) (domain.Conversation, error) {
	if g.err != nil {
		return domain.Conversation{}, g.err
	}
	return g.TxBase.PutConversation(c, expectedRevision)
}

func (g *Guard) InsertCall(c domain.CallRecord) error {
	if g.err != nil {
		return g.err
	}
	return g.TxBase.InsertCall(c)
}

func (g *Guard) UpdateCall(c domain.CallRecord, expectedRevision uint64) (domain.CallRecord, error) {
	if g.err != nil {
		return domain.CallRecord{}, g.err
	}
	return g.TxBase.UpdateCall(c, expectedRevision)
}

func (g *Guard) PutCallAttempt(a domain.CallAttempt) error {
	if g.err != nil {
		return g.err
	}
	return g.TxBase.PutCallAttempt(a)
}

// noteWrite enforces P3-1 even when a caller ignores a later write error.
func (g *Guard) noteWrite(err error) {
	if err == nil {
		g.wrote = true
	} else if g.wrote {
		g.Poison(err)
	}
}
