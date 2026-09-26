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
// blob availability for item parts, dangling-relationship rejection,
// supersession acyclicity, the obligation and call transition tables, one
// reserving call per conversation, audit records written atomically with the
// changes they describe, and compare-and-swap on mutable records.
//
// Sequence rule: every sequence-valued field a write introduces (an inserted
// record's Seq, a transition's Seq, a call's FinishedSeq, ...) must have been
// allocated by NextSeq in the same transaction; otherwise the write fails
// with domain.ErrInvalidRecord. This keeps commit order and audit order equal.
//
// Compare-and-swap rule: every Update*/Put* method except PutCallAttempt
// takes the expected current Version or Revision (0 to create). On success the store itself writes
// expected+1, ignoring the Version/Revision value in the argument, and
// returns the stored record; a mismatch fails with domain.ErrVersionConflict.
// A missing record reads as version 0, so updating one with expected > 0 is a
// version conflict too.
//
// Errors (compared with errors.Is; implementations may wrap them):
//
//   - domain.ErrInvalidRecord: a record fails structural validation, names
//     another session than the transaction's, breaks the sequence rule, or
//     carries an audit event that does not target the record it describes.
//   - domain.ErrNotFound: a single-record getter, or a write, names a record
//     missing from this session (an item for SetCurrentDirective, an
//     obligation version for a transition, a call for an attempt).
//   - domain.ErrImmutable: an immutable record's ID is reused, or a write
//     changes a field outside those its method may change (obligation fields
//     other than Current/RetiredSeq/MaterializationDisabled, frozen call
//     fields, closed call attempts).
//   - domain.ErrInvalidTransition: a state change outside its table,
//     including a transition whose From is not the current status, a
//     transition on a retired obligation version, revoking a revoked grant,
//     a new attempt that is not SENT or whose call is not PREPARED, and a
//     call transition without its attempt evidence.
//   - domain.ErrVersionConflict: compare-and-swap mismatch, and an obligation
//     version that is not one more than the latest.
//
// Authorization is the caller's job (domain.AuthorizeMutation,
// domain.AuthorizeGrantIssuance, domain.AuthorizeSupersession); stores do not
// know who is asking.
package store

import (
	"context"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Store is a transactional, session-partitioned store.
type Store interface {
	// Update runs fn in a read-write transaction for one session. Writers to
	// the same session are serialized. Writers to different sessions are
	// independent logically but may be serialized by the implementation (the
	// SQLite store has a single writer). If fn returns an error, nothing it
	// wrote is committed and Update returns that error unchanged (so
	// errors.Is works).
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

// ReadTx reads one session's state. Every single-record getter returns
// domain.ErrNotFound (possibly wrapped) when the record does not exist in
// this session. List methods return an empty result and a nil error when
// nothing matches, including when their parent (an obligation or call) does
// not exist.
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
	// Seq must be allocated in this transaction. Every image or document part
	// must reference a blob already stored in this session whose length
	// equals the part's BlobSize (domain.ErrIntegrity otherwise). Reusing an
	// ID fails with domain.ErrImmutable.
	InsertItem(it domain.ContextItem) error
	// UpdateItem applies a lifecycle change and appends its audit event
	// atomically. The stored Version must equal expectedVersion
	// (domain.ErrVersionConflict otherwise); the event must target this item
	// (TargetItem, TargetID == id) with a Seq allocated in this transaction.
	// It returns the updated item.
	UpdateItem(id string, expectedVersion uint64, change domain.ItemChange, event domain.LifecycleEvent) (domain.ContextItem, error)

	// InsertRelationship stores an edge. Both endpoints must be items in
	// this session (domain.ErrDanglingRelationship otherwise). A SUPERSEDES
	// edge that would close a cycle fails with domain.ErrSupersessionCycle.
	// Reusing an ID fails with domain.ErrImmutable.
	InsertRelationship(r domain.Relationship) error

	// SetCurrentDirective points (task, directive ID) at an item, which must
	// exist in this session (domain.ErrNotFound), belong to taskID, and
	// carry that directive ID (domain.ErrInvalidRecord).
	SetCurrentDirective(taskID, directiveID, itemID string) error

	// InsertBlob stores an immutable blob after verifying its hash.
	// Inserting identical bytes again is a no-op.
	InsertBlob(b domain.Blob) error

	// InsertObligationVersion stores a new obligation version. Its Version
	// must be one more than the latest stored version (or 1), its Revision 1,
	// its Status UNRESOLVED, and its CreatedSeq allocated in this transaction.
	InsertObligationVersion(o domain.ObligationVersion) error
	// UpdateObligationVersion replaces a stored version's non-status mutable
	// fields (Current, RetiredSeq, MaterializationDisabled) under
	// compare-and-swap. Status and EvidenceIDs change only through
	// AppendObligationTransition; a differing Status fails with
	// domain.ErrInvalidTransition.
	UpdateObligationVersion(o domain.ObligationVersion, expectedRevision uint64) (domain.ObligationVersion, error)
	// AppendObligationTransition records a transition and applies it to the
	// version atomically. The version's Revision must equal
	// expectedRevision (domain.ErrVersionConflict otherwise), so a matcher
	// that evaluated evidence against an earlier revision cannot apply its
	// result after the obligation changed in between (FR-OBL-005, INV-16).
	// From must equal the version's current status and the version must be
	// current. The version's Status becomes To, its EvidenceIDs become the
	// transition's evidence (for SATISFIED) or empty, and its Revision
	// increments. It returns the updated version.
	AppendObligationTransition(t domain.ObligationTransition, expectedRevision uint64) (domain.ObligationVersion, error)

	// InsertGrant stores a grant. Callers must first check
	// domain.AuthorizeGrantIssuance against the grant's targets.
	InsertGrant(g domain.MutationGrant) error
	// RevokeGrant sets RevokedSeq (event.Seq) on a grant that is not already
	// revoked and appends the audit event atomically; the event must target
	// the grant (TargetGrant) with a Seq allocated in this transaction.
	// Callers must first check domain.AuthorizeGrantRevocation.
	RevokeGrant(id string, event domain.LifecycleEvent) (domain.MutationGrant, error)

	// PutTask creates a task (expectedVersion 0) or replaces it under
	// compare-and-swap on Version. A change of Status (and task creation)
	// requires event, a TargetTask audit event with a Seq allocated in this
	// transaction, appended atomically; otherwise event must be nil.
	PutTask(t domain.TaskState, expectedVersion uint64, event *domain.LifecycleEvent) (domain.TaskState, error)

	// AppendLifecycleEvent appends an audit event. TargetCall events are
	// reserved for the call ledger (internal/invocation).
	AppendLifecycleEvent(e domain.LifecycleEvent) error

	// PutConversation creates (expectedRevision 0) or replaces a
	// conversation under compare-and-swap on Revision.
	PutConversation(c domain.Conversation, expectedRevision uint64) (domain.Conversation, error)
	// InsertCall stores a new call record (Revision 1). The call must be
	// PREPARED with no attempts, so no call can enter the ledger past the
	// evidence gates of UpdateCall (domain.ErrInvalidTransition otherwise).
	// It fails with domain.ErrCallInFlight if the conversation already has
	// another reserving call (FR-CALL-005).
	InsertCall(c domain.CallRecord) error
	// UpdateCall replaces a call record under compare-and-swap on Revision.
	// The state change must satisfy domain.ValidCallTransition (or leave the
	// state unchanged), frozen proposal fields cannot change, and the
	// one-reserving-call rule of InsertCall holds. Leaving SENT or UNKNOWN
	// requires attempt number c.Attempts to be stored already in the
	// matching closed state, so no transition can outrun its evidence:
	//
	//	-> COMPLETED  attempt COMPLETED with OutcomeHash == c.OutcomeHash
	//	-> FAILED     attempt FAILED with OutcomeHash == c.OutcomeHash
	//	-> PREPARED   (retry, from SENT only) attempt FAILED and Retryable
	//	-> UNKNOWN    attempt UNKNOWN
	//	-> ABANDONED  attempt ABANDONED
	//
	// PREPARED -> SENT requires attempt c.Attempts stored as SENT.
	// Violations fail with domain.ErrInvalidTransition.
	UpdateCall(c domain.CallRecord, expectedRevision uint64) (domain.CallRecord, error)
	// PutCallAttempt inserts a new attempt (numbered densely from 1, state
	// SENT, only while its call is PREPARED) or moves an existing attempt
	// along domain.ValidAttemptTransition. It is the one unversioned write:
	// the attempt transition table serializes it instead. Closed attempts
	// (COMPLETED, FAILED, ABANDONED) are immutable, and only State,
	// OutcomeHash, Retryable, FinishedSeq, and FinishedAt may change.
	PutCallAttempt(a domain.CallAttempt) error
}
