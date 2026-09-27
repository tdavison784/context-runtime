package obligation

// Test store: W2's real backend with per-write failure injection on the
// semantic facet (P3-1). Every semantic write — the services' and graph's —
// goes through store.Semantic(tx), so the k-th one can be made to fail; the
// failure poisons the transaction exactly as Guard poisons an ignored
// failed write.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
	"github.com/tdavison784/context-runtime/internal/store/sqlite/sqlitetest"
)

var errInjected = errors.New("teststore: injected write failure")

// backendFactory builds the real store; tests default to W2's memory backend
// and the SQLite suite swaps in W2's SQLite backend.
var backendFactory = func(t *testing.T) store.Store { return memory.New() }

// sqliteBackend opens a private copy of W2's migrated template, so each store
// skips replaying every migration (the dominant cost under -race).
func sqliteBackend(t *testing.T) store.Store {
	t.Helper()
	return sqlitetest.Open(t)
}

type testStore struct {
	store.Store
	// failAt, when positive, fails the failAt-th semantic write of the next
	// Update with errInjected.
	failAt atomic.Int64
}

func newTestStore(t *testing.T) *testStore {
	t.Helper()
	return &testStore{Store: backendFactory(t)}
}

func (f *testStore) Update(ctx context.Context, session string, fn func(store.Tx) error) error {
	failAt := int(f.failAt.Swap(0))
	if failAt == 0 {
		return f.Store.Update(ctx, session, fn)
	}
	return f.Store.Update(ctx, session, func(tx store.Tx) error {
		return fn(&faultTx{Tx: tx, failAt: failAt})
	})
}

type faultTx struct {
	store.Tx
	failAt, writes int
}

func (t *faultTx) SemanticTransaction() (store.SemanticTx, error) {
	sem, err := store.Semantic(t.Tx)
	if err != nil {
		return nil, err
	}
	return &faultSem{SemanticTx: sem, tx: t}, nil
}

// SemanticReadBackend keeps store.ReadSemantic working on the wrapper: an
// embedded interface promotes only store.Tx's own methods.
func (t *faultTx) SemanticReadBackend() store.SemanticReader {
	r, err := store.ReadSemantic(t.Tx)
	if err != nil {
		return nil
	}
	return r
}

type faultSem struct {
	store.SemanticTx
	tx *faultTx
}

// fault counts one write and reports whether it is the injected failure.
func (s *faultSem) fault() error {
	s.tx.writes++
	if s.tx.writes == s.tx.failAt {
		s.SemanticTx.Poison(errInjected)
		return errInjected
	}
	return nil
}

func (s *faultSem) InsertObligationDeclaration(v domain.ObligationDeclaration) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.InsertObligationDeclaration(v)
}
func (s *faultSem) InsertApplicabilityProof(p domain.ApplicabilityProof, d []domain.ProofDependency) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.InsertApplicabilityProof(p, d)
}
func (s *faultSem) InsertAssertion(v domain.AssertionRecord) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.InsertAssertion(v)
}
func (s *faultSem) AppendSemanticObligationTransition(t domain.ObligationTransition, d domain.TransitionDetail, e uint64) (domain.ObligationVersion, error) {
	if err := s.fault(); err != nil {
		return domain.ObligationVersion{}, err
	}
	return s.SemanticTx.AppendSemanticObligationTransition(t, d, e)
}
func (s *faultSem) SetObligationMaterialization(r domain.ObligationRef, d bool, e uint64, ev domain.LifecycleEvent) (domain.ObligationVersion, error) {
	if err := s.fault(); err != nil {
		return domain.ObligationVersion{}, err
	}
	return s.SemanticTx.SetObligationMaterialization(r, d, e, ev)
}
func (s *faultSem) InsertResourceBinding(v domain.ResourceBinding) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.InsertResourceBinding(v)
}
func (s *faultSem) InsertResourceUpdate(v domain.ResourceUpdate) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.InsertResourceUpdate(v)
}
func (s *faultSem) PutResourceState(v domain.ResourceState, e uint64) (domain.ResourceState, error) {
	if err := s.fault(); err != nil {
		return domain.ResourceState{}, err
	}
	return s.SemanticTx.PutResourceState(v, e)
}
func (s *faultSem) PutResourcePathState(v domain.ResourcePathState, e uint64) (domain.ResourcePathState, error) {
	if err := s.fault(); err != nil {
		return domain.ResourcePathState{}, err
	}
	return s.SemanticTx.PutResourcePathState(v, e)
}
func (s *faultSem) InsertWorkspaceBinding(v domain.WorkspaceBinding) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.InsertWorkspaceBinding(v)
}
func (s *faultSem) InsertObservationRun(v domain.ObservationRun) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.InsertObservationRun(v)
}
func (s *faultSem) InsertObservation(v domain.ObservationRecord) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.InsertObservation(v)
}
func (s *faultSem) PutSubjectState(v domain.SubjectState, e uint64, c string) (domain.SubjectState, error) {
	if err := s.fault(); err != nil {
		return domain.SubjectState{}, err
	}
	return s.SemanticTx.PutSubjectState(v, e, c)
}
func (s *faultSem) InsertMutationReceipt(v domain.MutationReceipt) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.InsertMutationReceipt(v)
}
func (s *faultSem) InsertCoverage(c domain.CoverageRecord, m []domain.CoverageMember) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.InsertCoverage(c, m)
}
func (s *faultSem) InsertSemanticChange(v domain.SemanticChange) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.InsertSemanticChange(v)
}
func (s *faultSem) SetCurrentVersion(item, prior string) error {
	if err := s.fault(); err != nil {
		return err
	}
	return s.SemanticTx.SetCurrentVersion(item, prior)
}
