package obligation

// TEMPORARY test harness: a strict in-memory implementation of the Phase 3
// store.SemanticTxBase facet layered over the Phase 2 memory store, used only
// until W2 publishes the real memory/SQLite semantic backends. It enforces
// the manifest rules W4's services rely on (session, allocated sequence,
// append-only IDs, CAS, reference existence, transaction rollback) so service
// tests do not depend on a lax double. Replace newTestStore with the real
// backend and delete this file once W2 lands.

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/memory"
)

type proofCache struct{ proof, assertion string }

type semState struct {
	receipts     map[string]domain.MutationReceipt
	decls        map[domain.ObligationRef]domain.ObligationDeclaration
	caches       map[domain.ObligationRef]proofCache
	proofs       map[string]domain.ApplicabilityProof
	deps         map[string][]domain.ProofDependency
	assertions   map[string]domain.AssertionRecord
	details      map[string]domain.TransitionDetail
	resBindings  map[string]domain.ResourceBinding
	resStates    map[string]domain.ResourceState
	resUpdates   map[string]domain.ResourceUpdate
	pathStates   map[string]domain.ResourcePathState
	wsBindings   map[domain.WorkspaceBindingRef]domain.WorkspaceBinding
	runs         map[string]domain.ObservationRun
	observations map[string]domain.ObservationRecord
	subjects     map[string]domain.SubjectState
	coverages    map[string]domain.CoverageRecord
	members      map[string][]domain.CoverageMember
}

func newSemState() *semState {
	return &semState{
		receipts: map[string]domain.MutationReceipt{}, decls: map[domain.ObligationRef]domain.ObligationDeclaration{},
		caches: map[domain.ObligationRef]proofCache{}, proofs: map[string]domain.ApplicabilityProof{},
		deps: map[string][]domain.ProofDependency{}, assertions: map[string]domain.AssertionRecord{},
		details: map[string]domain.TransitionDetail{}, resBindings: map[string]domain.ResourceBinding{},
		resStates: map[string]domain.ResourceState{}, resUpdates: map[string]domain.ResourceUpdate{},
		pathStates: map[string]domain.ResourcePathState{}, wsBindings: map[domain.WorkspaceBindingRef]domain.WorkspaceBinding{},
		runs: map[string]domain.ObservationRun{}, observations: map[string]domain.ObservationRecord{},
		subjects: map[string]domain.SubjectState{}, coverages: map[string]domain.CoverageRecord{},
		members: map[string][]domain.CoverageMember{},
	}
}

// clone copies every map; stored values are never mutated in place.
func (s *semState) clone() *semState {
	return &semState{
		receipts: cloneMap(s.receipts), decls: cloneMap(s.decls), caches: cloneMap(s.caches),
		proofs: cloneMap(s.proofs), deps: cloneMap(s.deps), assertions: cloneMap(s.assertions),
		details: cloneMap(s.details), resBindings: cloneMap(s.resBindings), resStates: cloneMap(s.resStates),
		resUpdates: cloneMap(s.resUpdates), pathStates: cloneMap(s.pathStates), wsBindings: cloneMap(s.wsBindings),
		runs: cloneMap(s.runs), observations: cloneMap(s.observations), subjects: cloneMap(s.subjects),
		coverages: cloneMap(s.coverages), members: cloneMap(s.members),
	}
}

func cloneMap[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// checkCommit enforces deferred references: every proof names a transition
// detail recorded in the same or an earlier transaction.
func (s *semState) checkCommit() error {
	for _, p := range s.proofs {
		if _, ok := s.details[p.TransitionID]; !ok {
			return domain.ErrDanglingRelationship
		}
		if p.EvidenceCoverageID != "" {
			if c, ok := s.coverages[p.EvidenceCoverageID]; !ok || c.Purpose != domain.CoverageEvidenceSupport {
				return domain.ErrDanglingRelationship
			}
		}
	}
	return nil
}

// testStore wraps a memory store with the semantic facet. Writers are
// serialized so the facet state commits exactly when the memory store does.
type testStore struct {
	store.Store
	mu        sync.Mutex
	committed map[string]*semState
	// failAt, when positive, makes the failAt-th facet write of the next
	// Update fail with errInjected (failure injection, P3-1).
	failAt int
}

var errInjected = errors.New("semstore: injected write failure")

func newTestStore() *testStore {
	return &testStore{Store: memory.New(), committed: map[string]*semState{}}
}

func (f *testStore) state(session string) *semState {
	if s, ok := f.committed[session]; ok {
		return s
	}
	return newSemState()
}

func (f *testStore) Update(ctx context.Context, session string, fn func(store.Tx) error) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	st := f.state(session).clone()
	failAt := f.failAt
	f.failAt = 0
	err := f.Store.Update(ctx, session, func(tx store.Tx) error {
		ftx := &semTx{Tx: tx, st: st, failAt: failAt}
		if err := fn(ftx); err != nil {
			return err
		}
		return st.checkCommit()
	})
	if err == nil {
		f.committed[session] = st
	}
	return err
}

func (f *testStore) View(ctx context.Context, session string, fn func(store.ReadTx) error) error {
	f.mu.Lock()
	st := f.state(session)
	f.mu.Unlock()
	return f.Store.View(ctx, session, func(tx store.ReadTx) error {
		return fn(&semReadTx{ReadTx: tx, st: st})
	})
}

type semReadTx struct {
	store.ReadTx
	st *semState
}

func (r *semReadTx) SemanticReadBackend() store.SemanticReader {
	return &semBackend{rtx: r.ReadTx, st: r.st}
}

type semTx struct {
	store.Tx
	st       *semState
	poisoned error
	failAt   int
	writes   int
}

func (t *semTx) Poison(err error) {
	if t.poisoned == nil {
		t.poisoned = cmp.Or(err, store.ErrPoisoned)
	}
	t.Tx.Poison(err)
}

func (t *semTx) SemanticTransaction() (store.SemanticTx, error) {
	return &semGuarded{semBackend: semBackend{rtx: t.Tx, tx: t, st: t.st}}, nil
}
func (t *semTx) SemanticBackend() store.SemanticTxBase {
	return &semBackend{rtx: t.Tx, tx: t, st: t.st}
}

// semGuarded poisons the transaction on any write failure, like store.Guard.
type semGuarded struct{ semBackend }

func (g *semGuarded) Poison(err error) { g.tx.Poison(err) }

// semBackend implements the facet. Unimplemented families panic through the
// nil embedded interface, which makes an unexpected dependency loud.
type semBackend struct {
	store.SemanticTxBase
	rtx store.ReadTx
	tx  *semTx
	st  *semState
}

var errFakeRead = errors.New("semstore: write on read snapshot")

// write runs a mutation with Guard's poison discipline.
func (b *semBackend) write(meta *domain.SemanticMeta, f func() error) error {
	if b.tx == nil {
		return errFakeRead
	}
	if b.tx.poisoned != nil {
		return b.tx.poisoned
	}
	err := func() error {
		b.tx.writes++
		if b.tx.writes == b.tx.failAt {
			return errInjected
		}
		if meta != nil {
			if err := meta.Validate(); err != nil {
				return err
			}
			if meta.SessionID != b.rtx.SessionID() || !b.tx.Allocated(meta.Seq) {
				return domain.ErrInvalidRecord
			}
		}
		return f()
	}()
	if err != nil {
		b.tx.Poison(err)
	}
	return err
}

func page[T any](all []T, key func(T) store.Cursor, p store.Page) (store.ResultPage[T], error) {
	if p.Limit <= 0 {
		return store.ResultPage[T]{}, domain.ErrInvalidRecord
	}
	slices.SortFunc(all, func(a, b T) int {
		ka, kb := key(a), key(b)
		return cmp.Or(cmp.Compare(ka.Seq, kb.Seq), cmp.Compare(ka.ID, kb.ID))
	})
	var out store.ResultPage[T]
	for _, v := range all {
		k := key(v)
		if k.Seq < p.After.Seq || k.Seq == p.After.Seq && k.ID <= p.After.ID {
			continue
		}
		if len(out.Records) == p.Limit {
			out.More = true
			break
		}
		out.Records = append(out.Records, v)
		out.Next = k
	}
	return out, nil
}

func receiptKey(f domain.MutationFamily, id string) string { return string(f) + "\x00" + id }

// --- receipts ---

func (b *semBackend) MutationReceipt(f domain.MutationFamily, id string) (domain.MutationReceipt, error) {
	r, ok := b.st.receipts[receiptKey(f, id)]
	if !ok {
		return domain.MutationReceipt{}, domain.ErrNotFound
	}
	return r.Clone(), nil
}

func (b *semBackend) InsertMutationReceipt(r domain.MutationReceipt) error {
	return b.write(&r.SemanticMeta, func() error {
		if err := r.Validate(); err != nil {
			return err
		}
		k := receiptKey(r.Family, r.RequestID)
		if _, ok := b.st.receipts[k]; ok {
			return domain.ErrImmutable
		}
		b.st.receipts[k] = r.Clone()
		return nil
	})
}

// --- obligations ---

func (b *semBackend) ExactObligation(ref domain.ObligationRef) (domain.ObligationVersion, error) {
	if ref.SessionID != b.rtx.SessionID() {
		return domain.ObligationVersion{}, domain.ErrNotFound
	}
	versions, err := b.rtx.ObligationVersions(ref.ObligationID)
	if err != nil {
		return domain.ObligationVersion{}, err
	}
	for _, v := range versions {
		if v.Version == ref.Version {
			c := b.st.caches[ref]
			v.CurrentProofID, v.CurrentAssertionID = c.proof, c.assertion
			return v, nil
		}
	}
	return domain.ObligationVersion{}, domain.ErrNotFound
}

func (b *semBackend) ObligationDeclaration(ref domain.ObligationRef) (domain.ObligationDeclaration, error) {
	d, ok := b.st.decls[ref]
	if !ok {
		return domain.ObligationDeclaration{}, domain.ErrNotFound
	}
	return d.Clone(), nil
}

func (b *semBackend) InsertObligationDeclaration(d domain.ObligationDeclaration) error {
	return b.write(&d.SemanticMeta, func() error {
		if err := d.Validate(); err != nil {
			return err
		}
		if _, ok := b.st.decls[d.Target]; ok {
			return domain.ErrImmutable
		}
		o, err := b.ExactObligation(d.Target)
		if err != nil {
			return domain.ErrInvalidRecord
		}
		if o.SourceItemID != d.SourceItemID || o.DeclarationSlot != d.DeclarationSlot || o.BindingState != d.Binding ||
			!equalPtr(o.Matcher, d.Matcher) || !equalPtr(o.WorkspaceBindingRef, d.WorkspaceBinding) || !equalTargetPtr(o.TargetSpec, d.TargetSpec) {
			return domain.ErrInvalidRecord
		}
		b.st.decls[d.Target] = d.Clone()
		return nil
	})
}

func equalPtr[T comparable](a, b *T) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func equalTargetPtr(a, b *domain.TargetSpec) bool {
	return a == nil && b == nil || a != nil && b != nil && equalTarget(*a, *b)
}

func (b *semBackend) CurrentBoundObligationsBySubject(key string, p store.Page) (store.ResultPage[domain.ObligationVersion], error) {
	all, err := b.rtx.Obligations("")
	if err != nil {
		return store.ResultPage[domain.ObligationVersion]{}, err
	}
	var out []domain.ObligationVersion
	for _, o := range all {
		if o.Current && o.BindingState == domain.BindingBound && o.TargetSubjectKey == key {
			v, err := b.ExactObligation(domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version})
			if err != nil {
				return store.ResultPage[domain.ObligationVersion]{}, err
			}
			out = append(out, v)
		}
	}
	return page(out, func(o domain.ObligationVersion) store.Cursor {
		return store.Cursor{Seq: o.CreatedSeq, ID: o.ObligationID}
	}, p)
}

// GrantsFor scans legacy grants; the real backends use an index.
func (b *semBackend) GrantsFor(action domain.Action, target domain.GrantTarget, limit int) ([]domain.MutationGrant, error) {
	if limit <= 0 {
		return nil, domain.ErrInvalidRecord
	}
	all, err := b.rtx.Grants()
	if err != nil {
		return nil, err
	}
	var out []domain.MutationGrant
	for _, g := range all {
		if g.Action != action {
			continue
		}
		for _, t := range g.Targets {
			if t.AuthorizationKey == target.AuthorizationKey {
				out = append(out, g.Clone())
				break
			}
		}
	}
	if len(out) > limit {
		return nil, store.ErrLimitExceeded
	}
	return out, nil
}

// --- workspace and resources ---

func (b *semBackend) WorkspaceBinding(ref domain.WorkspaceBindingRef) (domain.WorkspaceBinding, error) {
	w, ok := b.st.wsBindings[ref]
	if !ok {
		return domain.WorkspaceBinding{}, domain.ErrNotFound
	}
	return w, nil
}

func (b *semBackend) WorkspaceBindingsByContext(source, task, conversation string, p store.Page) (store.ResultPage[domain.WorkspaceBinding], error) {
	var out []domain.WorkspaceBinding
	for _, w := range b.st.wsBindings {
		if w.Context.Matches(source, task, conversation) {
			out = append(out, w)
		}
	}
	return page(out, func(w domain.WorkspaceBinding) store.Cursor { return store.Cursor{Seq: w.Seq, ID: w.ID} }, p)
}

func (b *semBackend) InsertWorkspaceBinding(w domain.WorkspaceBinding) error {
	return b.write(&w.SemanticMeta, func() error {
		if err := w.Validate(); err != nil {
			return err
		}
		ref := domain.WorkspaceBindingRef{ID: w.ID, Version: w.Version}
		if _, ok := b.st.wsBindings[ref]; ok {
			return domain.ErrImmutable
		}
		if _, ok := b.st.resBindings[w.ResourceID]; !ok {
			return domain.ErrInvalidRecord
		}
		if w.Version > 1 {
			if _, ok := b.st.wsBindings[domain.WorkspaceBindingRef{ID: w.ID, Version: w.Version - 1}]; !ok {
				return domain.ErrVersionConflict
			}
		}
		b.st.wsBindings[ref] = w
		return nil
	})
}

func (b *semBackend) ResourceBinding(id string) (domain.ResourceBinding, error) {
	r, ok := b.st.resBindings[id]
	if !ok {
		return domain.ResourceBinding{}, domain.ErrNotFound
	}
	return r, nil
}

func (b *semBackend) InsertResourceBinding(r domain.ResourceBinding) error {
	return b.write(&r.SemanticMeta, func() error {
		if err := r.Validate(); err != nil {
			return err
		}
		if _, ok := b.st.resBindings[r.ResourceID]; ok {
			return domain.ErrImmutable
		}
		b.st.resBindings[r.ResourceID] = r
		return nil
	})
}

func (b *semBackend) ResourceState(id string) (domain.ResourceState, error) {
	r, ok := b.st.resStates[id]
	if !ok {
		return domain.ResourceState{}, domain.ErrNotFound
	}
	return r, nil
}

func (b *semBackend) PutResourceState(s domain.ResourceState, expected uint64) (domain.ResourceState, error) {
	err := b.write(&s.SemanticMeta, func() error {
		cur := b.st.resStates[s.ResourceID]
		if cur.Revision != expected {
			return domain.ErrVersionConflict
		}
		s.Revision = expected + 1
		if err := s.Validate(); err != nil {
			return err
		}
		bind, ok := b.st.resBindings[s.ResourceID]
		u, uok := b.st.resUpdates[s.LastUpdateID]
		if !ok || bind.ID != s.BindingID || !uok || u.ResourceID != s.ResourceID || s.AuthoritativeRevision != u.ResultingAuthoritativeRevision {
			return domain.ErrInvalidRecord
		}
		if expected > 0 && s.AuthoritativeRevision <= cur.AuthoritativeRevision {
			return domain.ErrInvalidTransition
		}
		b.st.resStates[s.ResourceID] = s
		return nil
	})
	if err != nil {
		return domain.ResourceState{}, err
	}
	return s, nil
}

func (b *semBackend) ResourceUpdate(id string) (domain.ResourceUpdate, error) {
	u, ok := b.st.resUpdates[id]
	if !ok {
		return domain.ResourceUpdate{}, domain.ErrNotFound
	}
	return u.Clone(), nil
}

func (b *semBackend) InsertResourceUpdate(u domain.ResourceUpdate) error {
	return b.write(&u.SemanticMeta, func() error {
		if err := u.Validate(); err != nil {
			return err
		}
		if _, ok := b.st.resUpdates[u.ID]; ok {
			return domain.ErrImmutable
		}
		if _, ok := b.st.resBindings[u.ResourceID]; !ok {
			return domain.ErrInvalidRecord
		}
		b.st.resUpdates[u.ID] = u.Clone()
		return nil
	})
}

func (b *semBackend) ResourcePathState(loc domain.ResourceLocator) (domain.ResourcePathState, error) {
	k, err := loc.Key()
	if err != nil {
		return domain.ResourcePathState{}, domain.ErrInvalidRecord
	}
	s, ok := b.st.pathStates[k]
	if !ok {
		return domain.ResourcePathState{}, domain.ErrNotFound
	}
	return s, nil
}

func (b *semBackend) PutResourcePathState(s domain.ResourcePathState, expected uint64) (domain.ResourcePathState, error) {
	err := b.write(&s.SemanticMeta, func() error {
		k, err := s.Locator.Key()
		if err != nil {
			return domain.ErrInvalidRecord
		}
		if b.st.pathStates[k].Revision != expected {
			return domain.ErrVersionConflict
		}
		s.Revision = expected + 1
		if err := s.Validate(); err != nil {
			return err
		}
		if _, ok := b.st.resUpdates[s.ResourceUpdateID]; !ok {
			return domain.ErrInvalidRecord
		}
		b.st.pathStates[k] = s
		return nil
	})
	if err != nil {
		return domain.ResourcePathState{}, err
	}
	return s, nil
}

func (b *semBackend) SetObligationMaterialization(ref domain.ObligationRef, disabled bool, expected uint64, ev domain.LifecycleEvent) (domain.ObligationVersion, error) {
	var out domain.ObligationVersion
	err := b.write(nil, func() error {
		o, err := b.ExactObligation(ref)
		if err != nil {
			return err
		}
		if ev.TargetKind != domain.TargetObligation || ev.TargetID != ref.ObligationID || !b.tx.Allocated(ev.Seq) {
			return domain.ErrInvalidRecord
		}
		o.MaterializationDisabled = disabled
		cache := b.st.caches[ref]
		o.CurrentProofID, o.CurrentAssertionID = "", ""
		if out, err = b.tx.UpdateObligationVersion(o, expected); err != nil {
			return err
		}
		out.CurrentProofID, out.CurrentAssertionID = cache.proof, cache.assertion
		return b.tx.AppendLifecycleEvent(ev)
	})
	return out, err
}

// --- proofs, assertions, transitions ---

func (b *semBackend) ApplicabilityProof(id string) (domain.ApplicabilityProof, error) {
	p, ok := b.st.proofs[id]
	if !ok {
		return domain.ApplicabilityProof{}, domain.ErrNotFound
	}
	return p.Clone(), nil
}

func (b *semBackend) ProofDependencies(proofID string, p store.Page) (store.ResultPage[domain.ProofDependency], error) {
	var out []domain.ProofDependency
	for _, d := range b.st.deps[proofID] {
		out = append(out, d.Clone())
	}
	return page(out, func(d domain.ProofDependency) store.Cursor { return store.Cursor{Seq: d.Seq, ID: d.ID} }, p)
}

func (b *semBackend) InsertApplicabilityProof(p domain.ApplicabilityProof, deps []domain.ProofDependency) error {
	return b.write(&p.SemanticMeta, func() error {
		if err := p.Validate(); err != nil {
			return err
		}
		if _, ok := b.st.proofs[p.ID]; ok {
			return domain.ErrImmutable
		}
		if _, err := b.ExactObligation(p.Target); err != nil {
			return domain.ErrInvalidRecord
		}
		if len(deps) != len(p.DependencyIDs) {
			return domain.ErrInvalidRecord
		}
		for i, d := range deps {
			if err := d.Validate(); err != nil {
				return err
			}
			if d.ID != p.DependencyIDs[i] || d.ProofID != p.ID || d.SessionID != p.SessionID || !b.tx.Allocated(d.Seq) {
				return domain.ErrInvalidRecord
			}
			if _, ok := b.st.resBindings[d.ResourceID]; !ok {
				return domain.ErrInvalidRecord
			}
		}
		for _, id := range p.EvidenceIDs {
			if _, err := b.rtx.Item(id); err != nil {
				return domain.ErrDanglingRelationship
			}
		}
		b.st.proofs[p.ID] = p.Clone()
		cp := make([]domain.ProofDependency, len(deps))
		for i, d := range deps {
			cp[i] = d.Clone()
		}
		b.st.deps[p.ID] = cp
		return nil
	})
}

func (b *semBackend) Assertion(id string) (domain.AssertionRecord, error) {
	a, ok := b.st.assertions[id]
	if !ok {
		return domain.AssertionRecord{}, domain.ErrNotFound
	}
	return a, nil
}

func (b *semBackend) InsertAssertion(a domain.AssertionRecord) error {
	return b.write(&a.SemanticMeta, func() error {
		if err := a.Validate(); err != nil {
			return err
		}
		if _, ok := b.st.assertions[a.ID]; ok {
			return domain.ErrImmutable
		}
		if a.ProofID != "" {
			if p, ok := b.st.proofs[a.ProofID]; !ok || p.AssertionID != a.ID || p.Target != a.Target {
				return domain.ErrInvalidRecord
			}
		}
		b.st.assertions[a.ID] = a
		return nil
	})
}

func (b *semBackend) TransitionDetail(id string) (domain.TransitionDetail, error) {
	d, ok := b.st.details[id]
	if !ok {
		return domain.TransitionDetail{}, domain.ErrNotFound
	}
	return d.Clone(), nil
}

func (b *semBackend) TransitionsByVersion(ref domain.ObligationRef, p store.Page) (store.ResultPage[domain.ObligationTransition], error) {
	all, err := b.rtx.ObligationTransitions(ref.ObligationID)
	if err != nil {
		return store.ResultPage[domain.ObligationTransition]{}, err
	}
	var out []domain.ObligationTransition
	for _, t := range all {
		if t.Version == ref.Version {
			out = append(out, t)
		}
	}
	return page(out, func(t domain.ObligationTransition) store.Cursor { return store.Cursor{Seq: t.Seq, ID: t.ID} }, p)
}

func (b *semBackend) AppendSemanticObligationTransition(t domain.ObligationTransition, d domain.TransitionDetail, expected uint64) (domain.ObligationVersion, error) {
	var out domain.ObligationVersion
	err := b.write(&d.SemanticMeta, func() error {
		if err := t.Validate(); err != nil {
			return err
		}
		if err := d.Validate(); err != nil {
			return err
		}
		ref := domain.ObligationRef{SessionID: t.SessionID, ObligationID: t.ObligationID, Version: t.Version}
		if t.Cause == "" || d.TransitionID != t.ID || d.ID != t.ID || d.Target != ref || d.Cause != t.Cause || d.ProofID != t.ProofID || d.PreviousProofID != t.PriorProofID || d.Seq != t.Seq {
			return domain.ErrInvalidRecord
		}
		if _, ok := b.st.details[t.ID]; ok {
			return domain.ErrImmutable
		}
		cache := b.st.caches[ref]
		if t.From == domain.ObligationSatisfied && t.PriorProofID != cache.proof {
			return domain.ErrInvalidRecord
		}
		if t.ProofID != "" {
			p, ok := b.st.proofs[t.ProofID]
			if !ok || p.Target != ref || p.TransitionID != t.ID || !equalPtr(p.Matcher, t.Matcher) {
				return domain.ErrInvalidRecord
			}
		}
		if d.AssertionID != "" {
			if _, ok := b.st.assertions[d.AssertionID]; !ok {
				return domain.ErrInvalidRecord
			}
		}
		var err error
		if out, err = b.tx.AppendObligationTransition(t, expected); err != nil {
			return err
		}
		if t.To == domain.ObligationSatisfied {
			b.st.caches[ref] = proofCache{proof: t.ProofID, assertion: d.AssertionID}
		} else {
			delete(b.st.caches, ref)
		}
		b.st.details[t.ID] = d.Clone()
		c := b.st.caches[ref]
		out.CurrentProofID, out.CurrentAssertionID = c.proof, c.assertion
		return nil
	})
	return out, err
}

// --- current pointers ---

func (b *semBackend) SetCurrentVersion(itemID, expectedPrior string) error {
	return b.write(nil, func() error {
		it, err := b.rtx.Item(itemID)
		if err != nil {
			return err
		}
		key, ok := it.CurrentKey()
		if !ok {
			return domain.ErrInvalidRecord
		}
		prior, err := b.rtx.CurrentVersion(key)
		if errors.Is(err, domain.ErrNotFound) {
			prior, err = "", nil
		}
		if err != nil {
			return err
		}
		if prior != expectedPrior {
			return domain.ErrVersionConflict
		}
		return b.tx.Tx.SetCurrentVersion(itemID)
	})
}

// --- resource fan-out and subjects ---

func (b *semBackend) ResourceUpdates(resourceID string, p store.Page) (store.ResultPage[domain.ResourceUpdate], error) {
	var out []domain.ResourceUpdate
	for _, u := range b.st.resUpdates {
		if u.ResourceID == resourceID {
			out = append(out, u.Clone())
		}
	}
	return page(out, func(u domain.ResourceUpdate) store.Cursor { return store.Cursor{Seq: u.Seq, ID: u.ID} }, p)
}

func (b *semBackend) CurrentProofsByDependency(resourceID, pathKey string, p store.Page) (store.ResultPage[domain.ApplicabilityProof], error) {
	var out []domain.ApplicabilityProof
	for _, pr := range b.st.proofs {
		if b.st.caches[pr.Target].proof != pr.ID {
			continue
		}
		o, err := b.ExactObligation(pr.Target)
		if err != nil || !o.Current || o.Status != domain.ObligationSatisfied {
			continue
		}
		for _, d := range b.st.deps[pr.ID] {
			key := ""
			if d.Locator != nil {
				key, _ = d.Locator.Key()
			}
			if d.ResourceID == resourceID && (pathKey == "" || d.Kind == domain.DependencyWorkspace || key == pathKey) {
				out = append(out, pr.Clone())
				break
			}
		}
	}
	return page(out, func(pr domain.ApplicabilityProof) store.Cursor { return store.Cursor{Seq: pr.Seq, ID: pr.ID} }, p)
}

func subjectKeyOf(subject, task string, a domain.AccessBoundary) string {
	return subject + "\x00" + task + "\x00" + string(a.Scope) + "\x00" + a.WorkflowID + "\x00" + a.TaskID + "\x00" + a.AgentID
}

func (b *semBackend) SubjectState(subject, task string, a domain.AccessBoundary) (domain.SubjectState, error) {
	s, ok := b.st.subjects[subjectKeyOf(subject, task, a)]
	if !ok {
		return domain.SubjectState{}, domain.ErrNotFound
	}
	return s, nil
}

func (b *semBackend) SubjectStatesByResource(resourceID string, p store.Page) (store.ResultPage[domain.SubjectState], error) {
	var out []domain.SubjectState
	for _, s := range b.st.subjects {
		if o, ok := b.st.observations[s.ObservationID]; ok {
			if run, ok := b.st.runs[o.RunID]; ok && targetResource(run.Subject.Target) == resourceID {
				out = append(out, s)
			}
		}
	}
	return page(out, func(s domain.SubjectState) store.Cursor { return store.Cursor{Seq: s.Seq, ID: s.ID} }, p)
}

func (b *semBackend) PutSubjectState(s domain.SubjectState, expected uint64, cause string) (domain.SubjectState, error) {
	err := b.write(&s.SemanticMeta, func() error {
		k := subjectKeyOf(s.SubjectKey, s.TaskID, s.Access)
		cur := b.st.subjects[k]
		if cur.Revision != expected {
			return domain.ErrVersionConflict
		}
		s.Revision = expected + 1
		if err := s.Validate(); err != nil {
			return err
		}
		_, obs := b.st.observations[cause]
		_, upd := b.st.resUpdates[cause]
		if !obs && !upd {
			return domain.ErrInvalidRecord
		}
		o, ok := b.st.observations[s.ObservationID]
		if !ok || o.SubjectKey != s.SubjectKey {
			return domain.ErrInvalidRecord
		}
		if _, err := b.rtx.Item(s.CurrentItemID); err != nil {
			return domain.ErrInvalidRecord
		}
		if expected > 0 && s.AcceptedOrdinal < cur.AcceptedOrdinal {
			return domain.ErrInvalidTransition
		}
		b.st.subjects[k] = s
		return nil
	})
	if err != nil {
		return domain.SubjectState{}, err
	}
	return s, nil
}

// --- runs and observations ---

func (b *semBackend) ObservationRun(id string) (domain.ObservationRun, error) {
	r, ok := b.st.runs[id]
	if !ok {
		return domain.ObservationRun{}, domain.ErrNotFound
	}
	return r.Clone(), nil
}

func (b *semBackend) InsertObservationRun(r domain.ObservationRun) error {
	return b.write(&r.SemanticMeta, func() error {
		if err := r.Validate(); err != nil {
			return err
		}
		if _, ok := b.st.runs[r.ID]; ok {
			return domain.ErrImmutable
		}
		if _, ok := b.st.wsBindings[r.Binding]; !ok {
			return domain.ErrInvalidRecord
		}
		b.st.runs[r.ID] = r.Clone()
		return nil
	})
}

func (b *semBackend) RunsBySubject(subject string, p store.Page) (store.ResultPage[domain.ObservationRun], error) {
	var out []domain.ObservationRun
	for _, r := range b.st.runs {
		if r.SubjectKey == subject {
			out = append(out, r.Clone())
		}
	}
	return page(out, func(r domain.ObservationRun) store.Cursor { return store.Cursor{Seq: r.Seq, ID: r.ID} }, p)
}

func (b *semBackend) Observation(id string) (domain.ObservationRecord, error) {
	o, ok := b.st.observations[id]
	if !ok {
		return domain.ObservationRecord{}, domain.ErrNotFound
	}
	return o, nil
}

func (b *semBackend) ObservationsByRun(runID string, p store.Page) (store.ResultPage[domain.ObservationRecord], error) {
	var out []domain.ObservationRecord
	for _, o := range b.st.observations {
		if o.RunID == runID {
			out = append(out, o)
		}
	}
	return page(out, func(o domain.ObservationRecord) store.Cursor { return store.Cursor{Seq: o.Seq, ID: o.ID} }, p)
}

func (b *semBackend) InsertObservation(o domain.ObservationRecord) error {
	return b.write(&o.SemanticMeta, func() error {
		if err := o.Validate(); err != nil {
			return err
		}
		if _, ok := b.st.observations[o.ID]; ok {
			return domain.ErrImmutable
		}
		run, ok := b.st.runs[o.RunID]
		if !ok || run.ExecutionID != o.ExecutionID || run.SubjectKey != o.SubjectKey || run.Binding != o.Binding || run.Reporter != o.Reporter || run.Access != o.Access {
			return domain.ErrInvalidRecord
		}
		ev, err := b.rtx.Item(o.EvidenceItemID)
		if err != nil || ev.Authority != domain.AuthorityTool || ev.Access != o.Access {
			return domain.ErrInvalidRecord
		}
		b.st.observations[o.ID] = o
		return nil
	})
}

// --- coverage ---

func (b *semBackend) Coverage(id string) (domain.CoverageRecord, error) {
	c, ok := b.st.coverages[id]
	if !ok {
		return domain.CoverageRecord{}, domain.ErrNotFound
	}
	return c, nil
}

func (b *semBackend) CoverageMembers(id string, p store.Page) (store.ResultPage[domain.CoverageMember], error) {
	var out []domain.CoverageMember
	for _, m := range b.st.members[id] {
		out = append(out, m.Clone())
	}
	return page(out, func(m domain.CoverageMember) store.Cursor { return store.Cursor{Seq: m.Seq, ID: m.ID} }, p)
}

func (b *semBackend) InsertCoverage(c domain.CoverageRecord, members []domain.CoverageMember) error {
	return b.write(&c.SemanticMeta, func() error {
		if err := c.Validate(); err != nil {
			return err
		}
		if _, ok := b.st.coverages[c.ID]; ok {
			return domain.ErrImmutable
		}
		sig, err := domain.CoverageSignature(c, members)
		if err != nil || sig != c.Signature {
			return domain.ErrInvalidRecord
		}
		for _, m := range members {
			if k, _ := m.Key(); m.ID != k {
				return domain.ErrInvalidRecord
			}
			if m.Source != nil {
				it, err := b.rtx.Item(m.Source.ItemID)
				if err != nil || it.ContentHash != m.Source.ContentHash {
					return domain.ErrDanglingRelationship
				}
			}
		}
		b.st.coverages[c.ID] = c
		cp := make([]domain.CoverageMember, len(members))
		for i, m := range members {
			cp[i] = m.Clone()
		}
		b.st.members[c.ID] = cp
		return nil
	})
}

func (b *semBackend) ObligationsByTaskOwner(taskID string, p store.Page) (store.ResultPage[domain.ObligationVersion], error) {
	all, err := b.rtx.Obligations("")
	if err != nil {
		return store.ResultPage[domain.ObligationVersion]{}, err
	}
	var out []domain.ObligationVersion
	for _, o := range all {
		if (o.Access.Scope == domain.ScopeTurn || o.Access.Scope == domain.ScopeTask) && o.Access.TaskID == taskID {
			v, err := b.ExactObligation(domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version})
			if err != nil {
				return store.ResultPage[domain.ObligationVersion]{}, err
			}
			out = append(out, v)
		}
	}
	return page(out, func(o domain.ObligationVersion) store.Cursor {
		return store.Cursor{Seq: o.CreatedSeq, ID: o.ObligationID}
	}, p)
}
