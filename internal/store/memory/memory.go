// Package memory implements store.Store in process memory (FR-PER-001).
//
// Each session has its own RWMutex: Update holds it exclusively, so writers
// to one session are serialized while other sessions proceed, and View holds
// it shared, so a view always sees one committed snapshot. A transaction
// buffers its writes in an overlay that is folded into the session's state
// only when fn succeeds. Records are deep-copied on the way in and out.
package memory

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ErrClosed reports use of a closed store.
var ErrClosed = errors.New("memory store: closed")

// Store is an in-memory store.Store. The zero value is not usable; call New.
type Store struct {
	mu       sync.Mutex
	sessions map[string]*session
	closed   bool
}

var _ store.Store = (*Store)(nil)

// New returns an empty store.
func New() *Store { return &Store{sessions: map[string]*session{}} }

type session struct {
	mu sync.RWMutex
	st *state
	// committed is set once a transaction commits a record; Sessions lists
	// only such sessions.
	committed bool
}

// directiveKey is a current-version identity (FR-DIR-002, M6): versions in
// different access boundaries or namespaces are independent.
type directiveKey struct {
	taskID, directiveID string
	boundary            domain.AccessBoundary
	namespace           domain.DirectiveNamespace
}

// currentIDKey is a current-version identity without its boundary.
type currentIDKey struct {
	taskID    string
	namespace domain.DirectiveNamespace
	id        string
}

// relKey keys relationships by type and one endpoint (SPEC-3.1 item 5), so
// a typed read never walks another type's edges at the same endpoint.
type relKey struct {
	typ domain.RelationshipType
	id  string
}

type obligationKey struct {
	id      string
	version uint64
}

type attemptKey struct {
	callID  string
	attempt int
}

// state is one session's committed records.
type state struct {
	lastSeq      uint64
	items        map[string]domain.ContextItem
	rels         map[string]domain.Relationship
	supersedes   map[string][]string // SUPERSEDES successors: FromID -> ToIDs
	supersededBy map[string][]string // SUPERSEDES predecessors: ToID -> FromIDs
	relsFrom     map[relKey][]string // relationship IDs by (Type, FromID)
	relsTo       map[relKey][]string // relationship IDs by (Type, ToID)
	relsByType   map[domain.RelationshipType][]string
	events       map[string]domain.EventRecord
	blobs        map[string]domain.Blob
	directives   map[directiveKey]string
	obligations  map[obligationKey]domain.ObligationVersion
	latest       map[string]uint64 // obligation ID -> latest version
	transitions  map[string]domain.ObligationTransition
	grants       map[string]domain.MutationGrant
	tasks        map[string]domain.TaskState
	lifecycle    map[string]domain.LifecycleEvent
	convs        map[string]domain.Conversation
	calls        map[string]domain.CallRecord
	attempts     map[attemptKey]domain.CallAttempt
	receipts     map[string]domain.IngestReceipt // by occurrence ID
	envelopes    map[string]domain.EventEnvelope // by occurrence ID
	references   map[string]domain.UnresolvedReference

	// Access-filtered lookup indexes (F1): keyed by owner columns, live-only
	// where noted.
	blobOwners  map[blobKey][]seqRef
	canonical   map[canonicalKey][]seqRef // live
	working     map[workingKey][]seqRef   // live
	sources     map[sourceKey][]seqRef    // live
	refOwners   map[sourceKey][]seqRef
	itemsByTask map[string][]string // task ID -> item IDs (SPEC-1.3)
	// Keyed secondary indexes (SPEC-2.1): the boundaries a (task,
	// namespace, ID) has current-version pointers in, and the obligation
	// versions bound to each source item.
	currentIDs   map[currentIDKey]map[domain.AccessBoundary]bool
	oblsBySource map[string]map[obligationKey]bool

	// sem holds the Phase 3 companion records (semantic.go).
	sem *semState
}

func newState() *state {
	return &state{
		items:        map[string]domain.ContextItem{},
		rels:         map[string]domain.Relationship{},
		supersedes:   map[string][]string{},
		supersededBy: map[string][]string{},
		relsFrom:     map[relKey][]string{},
		relsTo:       map[relKey][]string{},
		relsByType:   map[domain.RelationshipType][]string{},
		events:       map[string]domain.EventRecord{},
		blobs:        map[string]domain.Blob{},
		directives:   map[directiveKey]string{},
		obligations:  map[obligationKey]domain.ObligationVersion{},
		latest:       map[string]uint64{},
		transitions:  map[string]domain.ObligationTransition{},
		grants:       map[string]domain.MutationGrant{},
		tasks:        map[string]domain.TaskState{},
		lifecycle:    map[string]domain.LifecycleEvent{},
		convs:        map[string]domain.Conversation{},
		calls:        map[string]domain.CallRecord{},
		attempts:     map[attemptKey]domain.CallAttempt{},
		receipts:     map[string]domain.IngestReceipt{},
		envelopes:    map[string]domain.EventEnvelope{},
		references:   map[string]domain.UnresolvedReference{},
		blobOwners:   map[blobKey][]seqRef{},
		canonical:    map[canonicalKey][]seqRef{},
		working:      map[workingKey][]seqRef{},
		sources:      map[sourceKey][]seqRef{},
		currentIDs:   map[currentIDKey]map[domain.AccessBoundary]bool{},
		oblsBySource: map[string]map[obligationKey]bool{},
		refOwners:    map[sourceKey][]seqRef{},
		itemsByTask:  map[string][]string{},
		sem:          newSemState(),
	}
}

// session returns the named session, creating it if create is set. It
// returns nil for a missing session when create is unset.
func (s *Store) session(id string, create bool) (*session, error) {
	if id == "" {
		return nil, fmt.Errorf("%w: session ID is required", domain.ErrInvalidRecord)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	sess := s.sessions[id]
	if sess == nil && create {
		sess = &session{st: newState()}
		s.sessions[id] = sess
	}
	return sess, nil
}

// Update implements store.Store. A context canceled before commit rolls the
// transaction back and returns the context's error; commit itself cannot be
// interrupted.
func (s *Store) Update(ctx context.Context, sessionID string, fn func(store.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sess, err := s.session(sessionID, true)
	if err != nil {
		return err
	}
	sess.mu.Lock()
	defer sess.mu.Unlock()
	t := &tx{readTx: newReadTx(sessionID, sess.st, true), baseSeq: sess.st.lastSeq}
	t.writer = t
	defer t.finish()
	g := store.NewGuard(t)
	err = fn(g)
	if p := g.Poisoned(); p != nil {
		return p // the overlay is discarded: nothing commits (DUR-1.3)
	}
	if err != nil {
		return err
	}
	if err := t.runDeferred(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if t.semantic && !t.sequenced {
		return invalid("transaction changes semantic state without a sequenced record")
	}
	if err := t.checkLedgerSeqs(); err != nil {
		return err
	}
	if t.commit(sess.st) {
		sess.committed = true
	}
	return nil
}

// View implements store.Store.
func (s *Store) View(ctx context.Context, sessionID string, fn func(store.ReadTx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sess, err := s.session(sessionID, false)
	if err != nil {
		return err
	}
	st := newState()
	if sess != nil {
		sess.mu.RLock()
		defer sess.mu.RUnlock()
		st = sess.st
	}
	r := newReadTx(sessionID, st, false)
	defer r.finish()
	return fn(r)
}

// Sessions implements store.Store.
func (s *Store) Sessions(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	all := make(map[string]*session, len(s.sessions))
	maps.Copy(all, s.sessions)
	s.mu.Unlock()
	var out []string
	for id, sess := range all {
		sess.mu.RLock()
		if sess.committed {
			out = append(out, id)
		}
		sess.mu.RUnlock()
	}
	slices.Sort(out)
	return out, nil
}

// Close implements store.Store. Later calls fail with ErrClosed.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func cloneBlob(b domain.Blob) domain.Blob {
	b.Data = slices.Clone(b.Data)
	return b
}
