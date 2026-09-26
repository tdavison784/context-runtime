package ingest

import (
	"context"
	"sync"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ledgerStore wraps a store with an in-memory receipt ledger that commits
// exactly when the wrapped transaction commits, so tests exercise the real
// rollback semantics until the stores implement Ledger themselves. Updates
// are serialized so a staged receipt is published before the next
// transaction can look for it.
type ledgerStore struct {
	store.Store
	mu       sync.Mutex
	receipts map[string]map[string]ledgerEntry // session -> caller EventID or occurrence -> entry
}

type ledgerEntry struct {
	env domain.EventEnvelope
	r   domain.IngestReceipt
}

func newLedgerStore(s store.Store) *ledgerStore {
	return &ledgerStore{Store: s, receipts: map[string]map[string]ledgerEntry{}}
}

func (s *ledgerStore) Update(ctx context.Context, sessionID string, fn func(store.Tx) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var staged []ledgerEntry
	err := s.Store.Update(ctx, sessionID, func(tx store.Tx) error {
		staged = nil
		return fn(&ledgerTx{Tx: tx, s: s, session: sessionID, staged: &staged})
	})
	if err != nil {
		return err
	}
	if s.receipts[sessionID] == nil {
		s.receipts[sessionID] = map[string]ledgerEntry{}
	}
	for _, e := range staged {
		s.receipts[sessionID][ledgerKey(e.r)] = e
	}
	return nil
}

// all returns every committed receipt of a session.
func (s *ledgerStore) all(sessionID string) []domain.IngestReceipt {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.IngestReceipt
	for _, e := range s.receipts[sessionID] {
		out = append(out, e.r.Clone())
	}
	return out
}

func ledgerKey(r domain.IngestReceipt) string {
	if r.EventID != "" {
		return "evt:" + r.EventID
	}
	return "occ:" + r.OccurrenceID
}

type ledgerTx struct {
	store.Tx
	s       *ledgerStore
	session string
	staged  *[]ledgerEntry
}

func (t *ledgerTx) Receipt(eventID string) (domain.IngestReceipt, error) {
	for _, e := range *t.staged {
		if e.r.EventID == eventID {
			return e.r.Clone(), nil
		}
	}
	if e, ok := t.s.receipts[t.session]["evt:"+eventID]; ok && eventID != "" {
		return e.r.Clone(), nil
	}
	return domain.IngestReceipt{}, domain.ErrNotFound
}

func (t *ledgerTx) InsertReceipt(env domain.EventEnvelope, r domain.IngestReceipt) error {
	if err := env.Validate(); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	key := ledgerKey(r)
	if _, ok := t.s.receipts[t.session][key]; ok {
		return domain.ErrImmutable
	}
	for _, e := range *t.staged {
		if ledgerKey(e.r) == key {
			return domain.ErrImmutable
		}
	}
	*t.staged = append(*t.staged, ledgerEntry{env: env.Clone(), r: r.Clone()})
	return nil
}
