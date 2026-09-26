// Package store defines the persistence contract for semantic state, the
// call ledger, and audit records, with in-memory and SQLite implementations
// (FR-PER-001 through FR-PER-004).
//
// Every mutation runs inside Update, which serializes writers per session and
// commits atomically: either every write in the function commits or none
// does. Reads inside a transaction see that transaction's own writes. View
// reads a consistent committed snapshot. Records returned by a store are deep
// copies; mutating them never changes stored state.
//
// Stores enforce integrity rules that must hold regardless of caller:
// structural validation, immutability of items/blobs/relationships/events,
// dangling-relationship rejection, supersession acyclicity, the obligation
// and call transition tables, and compare-and-swap on mutable records.
// Authorization is the caller's job (internal/domain.AuthorizeMutation);
// stores do not know who is asking.
package store

import (
	"context"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Store is a transactional, session-partitioned store.
type Store interface {
	// Update runs fn in a read-write transaction for one session. Writers to
	// the same session are serialized; writers to different sessions may run
	// concurrently. If fn returns an error, nothing it wrote is committed and
	// Update returns that error unchanged (so errors.Is works).
	Update(ctx context.Context, sessionID string, fn func(Tx) error) error
	// View runs fn against a consistent committed snapshot of one session.
	View(ctx context.Context, sessionID string, fn func(ReadTx) error) error
	// Close releases resources. Close is idempotent.
	Close() error
}

// ItemFilter selects items within the transaction's session. Zero-valued
// fields do not filter. Results are ordered by Seq ascending, then ID.
type ItemFilter struct {
	TaskID      string
	AgentID     string
	Kinds       []domain.Kind
	Residency   domain.Residency
	DirectiveID string
	EventID     string
	MinSeq      uint64 // inclusive
	MaxSeq      uint64 // inclusive; 0 means unbounded
}

// RelationshipFilter selects relationships within the session. Zero-valued
// fields do not filter. Results are ordered by Seq ascending, then ID.
type RelationshipFilter struct {
	Type   domain.RelationshipType
	FromID string
	ToID   string
}

// LifecycleFilter selects lifecycle events. Results are ordered by Seq
// ascending, then ID.
type LifecycleFilter struct {
	TargetKind domain.TargetKind
	TargetID   string
	MinSeq     uint64
}

// CallFilter selects call records. Results are ordered by PreparedSeq
// ascending, then CallID.
type CallFilter struct {
	ConversationID string
	States         []domain.CallState
}

// ReadTx reads one session's state. Every getter returns domain.ErrNotFound
// (possibly wrapped) when the record does not exist in this session.
type ReadTx interface {
	// SessionID is the session this transaction is bound to.
	SessionID() string
	// LastSeq is the highest sequence number committed (or allocated in this
	// transaction); 0 for an empty session.
	LastSeq() uint64

	Item(id string) (domain.ContextItem, error)
	Items(f ItemFilter) ([]domain.ContextItem, error)
	Relationships(f RelationshipFilter) ([]domain.Relationship, error)
	Event(eventID string) (domain.EventRecord, error)
	// Blob returns the blob with the given hash after verifying its bytes;
	// corrupt bytes fail with domain.ErrIntegrity.
	Blob(hash string) (domain.Blob, error)
	// CurrentDirective returns the item ID of the current version of a
	// directive in a task (FR-DIR-002).
	CurrentDirective(taskID, directiveID string) (string, error)
	// Obligation returns the latest version of an obligation.
	Obligation(obligationID string) (domain.ObligationVersion, error)
	ObligationVersions(obligationID string) ([]domain.ObligationVersion, error)
	// Obligations returns the latest version of every obligation in a task,
	// ordered by ObligationID; an empty task ID returns all tasks.
	Obligations(taskID string) ([]domain.ObligationVersion, error)
	ObligationTransitions(obligationID string) ([]domain.ObligationTransition, error)
	Grant(id string) (domain.MutationGrant, error)
	// Grants returns every grant in the session ordered by ID.
	Grants() ([]domain.MutationGrant, error)
	Task(taskID string) (domain.TaskState, error)
	LifecycleEvents(f LifecycleFilter) ([]domain.LifecycleEvent, error)
	Conversation(conversationID string) (domain.Conversation, error)
	Call(callID string) (domain.CallRecord, error)
	Calls(f CallFilter) ([]domain.CallRecord, error)
	// CallAttempts returns a call's attempts ordered by attempt number.
	CallAttempts(callID string) ([]domain.CallAttempt, error)
}

// Tx is a read-write transaction for one session.
type Tx interface {
	ReadTx

	// NextSeq allocates the next sequence number. Numbers are dense and
	// strictly increasing per session; numbers allocated by a rolled-back
	// transaction are reused.
	NextSeq() uint64

	// InsertEvent records an event idempotency record. If the event ID
	// exists with the same request it returns the stored record and
	// existed=true without writing; with a different request it fails with
	// domain.ErrEventIDConflict.
	InsertEvent(e domain.EventRecord) (stored domain.EventRecord, existed bool, err error)

	// InsertItem stores a new immutable item. Its Version must be 1 and its
	// Seq must have been allocated by this session. Reusing an ID fails with
	// domain.ErrImmutable.
	InsertItem(it domain.ContextItem) error
	// UpdateItem applies a lifecycle change if the stored Version equals
	// expectedVersion, otherwise it fails with domain.ErrVersionConflict. It
	// returns the updated item. Callers record the matching LifecycleEvent.
	UpdateItem(id string, expectedVersion uint64, change domain.ItemChange) (domain.ContextItem, error)

	// InsertRelationship stores an edge. Both endpoints must be items in
	// this session (domain.ErrDanglingRelationship otherwise). A SUPERSEDES
	// edge that would close a cycle fails with domain.ErrSupersessionCycle.
	// Reusing an ID fails with domain.ErrImmutable.
	InsertRelationship(r domain.Relationship) error

	// SetCurrentDirective points (task, directive ID) at an item, which must
	// exist in this session and carry that directive ID.
	SetCurrentDirective(taskID, directiveID, itemID string) error

	// InsertBlob stores an immutable blob after verifying its hash.
	// Inserting identical bytes again is a no-op.
	InsertBlob(b domain.Blob) error

	// InsertObligationVersion stores a new obligation version. Its Version
	// must be one more than the latest stored version (or 1) and Revision 1.
	InsertObligationVersion(o domain.ObligationVersion) error
	// UpdateObligationVersion replaces a stored version's mutable fields
	// (Status, Current, RetiredSeq, EvidenceIDs, MaterializationDisabled) if
	// its Revision equals expectedRevision, then increments Revision.
	UpdateObligationVersion(o domain.ObligationVersion, expectedRevision uint64) (domain.ObligationVersion, error)
	// AppendObligationTransition records a transition; its From must equal
	// the version's current status.
	AppendObligationTransition(t domain.ObligationTransition) error

	InsertGrant(g domain.MutationGrant) error
	// RevokeGrant sets RevokedSeq on a grant that is not already revoked.
	RevokeGrant(id string, seq uint64) error

	// PutTask creates a task (expectedVersion 0, Version 1) or replaces it
	// if the stored Version equals expectedVersion; Version must then be
	// expectedVersion+1.
	PutTask(t domain.TaskState, expectedVersion uint64) error

	AppendLifecycleEvent(e domain.LifecycleEvent) error

	// PutConversation creates (expectedRevision 0, Revision 1) or replaces a
	// conversation under compare-and-swap on Revision.
	PutConversation(c domain.Conversation, expectedRevision uint64) error
	// InsertCall stores a new call record with Revision 1.
	InsertCall(c domain.CallRecord) error
	// UpdateCall replaces a call record under compare-and-swap on Revision.
	// The state change must satisfy domain.ValidCallTransition (or leave the
	// state unchanged), and immutable request fields cannot change.
	UpdateCall(c domain.CallRecord, expectedRevision uint64) error
	// PutCallAttempt inserts or updates an attempt keyed by (call, attempt).
	PutCallAttempt(a domain.CallAttempt) error
}
