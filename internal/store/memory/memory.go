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
}

type directiveKey struct{ taskID, directiveID string }

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
	lastSeq     uint64
	items       map[string]domain.ContextItem
	rels        map[string]domain.Relationship
	supersedes  map[string][]string // SUPERSEDES successors: FromID -> ToIDs
	relsFrom    map[string][]string // relationship IDs by FromID
	relsTo      map[string][]string // relationship IDs by ToID
	relsByType  map[domain.RelationshipType][]string
	events      map[string]domain.EventRecord
	blobs       map[string]domain.Blob
	directives  map[directiveKey]string
	obligations map[obligationKey]domain.ObligationVersion
	latest      map[string]uint64 // obligation ID -> latest version
	transitions map[string]domain.ObligationTransition
	grants      map[string]domain.MutationGrant
	tasks       map[string]domain.TaskState
	lifecycle   map[string]domain.LifecycleEvent
	convs       map[string]domain.Conversation
	calls       map[string]domain.CallRecord
	attempts    map[attemptKey]domain.CallAttempt
}

func newState() *state {
	return &state{
		items:       map[string]domain.ContextItem{},
		rels:        map[string]domain.Relationship{},
		supersedes:  map[string][]string{},
		relsFrom:    map[string][]string{},
		relsTo:      map[string][]string{},
		relsByType:  map[domain.RelationshipType][]string{},
		events:      map[string]domain.EventRecord{},
		blobs:       map[string]domain.Blob{},
		directives:  map[directiveKey]string{},
		obligations: map[obligationKey]domain.ObligationVersion{},
		latest:      map[string]uint64{},
		transitions: map[string]domain.ObligationTransition{},
		grants:      map[string]domain.MutationGrant{},
		tasks:       map[string]domain.TaskState{},
		lifecycle:   map[string]domain.LifecycleEvent{},
		convs:       map[string]domain.Conversation{},
		calls:       map[string]domain.CallRecord{},
		attempts:    map[attemptKey]domain.CallAttempt{},
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

// Update implements store.Store. A canceled context before or during fn
// rolls the transaction back and returns the context's error.
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
	defer t.finish()
	if err := fn(t); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	t.commit(sess.st)
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

// Close implements store.Store. Later calls fail with ErrClosed.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func cloneRelationship(r domain.Relationship) domain.Relationship {
	if r.Coverage != nil {
		c := *r.Coverage
		r.Coverage = &c
	}
	return r
}

func cloneBlob(b domain.Blob) domain.Blob {
	b.Data = slices.Clone(b.Data)
	return b
}
