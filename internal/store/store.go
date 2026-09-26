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
// takes the expected current Version or Revision. On success the store
// itself writes expected+1, ignoring the Version/Revision value in the
// argument, and returns the stored record; a mismatch fails with
// domain.ErrVersionConflict. For the create-or-replace methods (PutTask,
// PutConversation) a missing record reads as version 0: expected 0 creates
// it, and expected > 0 is a version conflict. Update-only methods
// (UpdateItem, UpdateObligationVersion, AppendObligationTransition,
// UpdateCall) fail with domain.ErrNotFound when their target is missing.
//
// Errors (compared with errors.Is; implementations may wrap them):
//
//   - domain.ErrInvalidRecord: a record fails structural validation, names
//     another session than the transaction's, breaks the sequence rule, or
//     carries an audit event that does not target the record it describes.
//   - domain.ErrNotFound: a single-record getter, or a write, names a record
//     missing from this session (an item for SetCurrentVersion, an
//     obligation version for a transition, a call for an attempt).
//   - domain.ErrImmutable: an immutable record's ID is reused, or a write
//     changes a field outside those its method may change (obligation fields
//     other than Current/RetiredSeq/MaterializationDisabled, frozen call
//     fields, closed call attempts, terminal calls).
//   - domain.ErrInvalidTransition: a state change outside its table,
//     including a transition whose From is not the current status, a
//     transition on a retired obligation version, revoking a revoked grant,
//     a new attempt that is not SENT or whose call is not PREPARED, and a
//     call transition without its attempt evidence.
//   - domain.ErrVersionConflict: compare-and-swap mismatch, and an obligation
//     version that is not one more than the latest.
//   - domain.ErrIntegrity: stored bytes or records fail verification (a blob
//     whose bytes no longer match its hash, an item whose parts no longer
//     match its ContentHash or SemanticBytes, a row that cannot be decoded).
//     Such a record is never returned. It is never used for operational
//     failures.
//   - Context and I/O errors are returned as themselves (wrapped at most),
//     so errors.Is(err, context.Canceled) and similar checks hold.
//
// Authorization is the caller's job (domain.AuthorizeMutation,
// domain.AuthorizeGrantIssuance, domain.AuthorizeSupersession); stores do not
// know who is asking.
package store

import (
	"context"
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// ErrLimitExceeded reports that a bounded read matched more records than
// its limit. Bounded reads never return a truncated result.
var ErrLimitExceeded = errors.New("store: result exceeds limit")

// Store is a transactional, session-partitioned store.
type Store interface {
	// Update runs fn in a read-write transaction for one session. Writers to
	// the same session are serialized. Writers to different sessions are
	// independent logically but may be serialized by the implementation (the
	// SQLite store has a single writer). If fn returns an error, nothing it
	// wrote is committed and Update returns that error unchanged (so
	// errors.Is works).
	//
	// Semantic-write rule: a transaction that changes semantic state (any
	// write other than blobs, conversations, calls, call attempts, and
	// TargetCall lifecycle events) must also write at least one record carrying a
	// sequence number allocated in it (an item, relationship, event record,
	// ingestion receipt, unresolved reference, obligation version or
	// transition, grant, or
	// non-TargetCall lifecycle event); otherwise the commit fails with
	// domain.ErrInvalidRecord. The
	// call ledger's preview-staleness check depends on every semantic
	// change being visible in the sequence (FR-CALL-001). Blobs are exempt
	// because they are content-addressed and inert until a sequenced record
	// references them.
	//
	// Cancellation: ctx may abort the transaction before commit, and then
	// Update returns the context's error (errors.Is(err, context.Canceled)
	// holds). Once commit begins it is not cancelled, so Update never
	// reports failure for a transaction that committed.
	//
	// fn must not call the Store re-entrantly (Update or View, for any
	// session); implementations may deadlock.
	Update(ctx context.Context, sessionID string, fn func(Tx) error) error
	// View runs fn against a consistent committed snapshot of one session.
	View(ctx context.Context, sessionID string, fn func(ReadTx) error) error
	// Sessions lists every session that has committed records, in
	// ascending order, so startup recovery can visit each one.
	Sessions(ctx context.Context) ([]string, error)
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

// DiagnosticFilter selects persisted diagnostics (D16). Viewer is required:
// a read returns only records whose source access boundary permits it
// (domain.DiagnosticRecord.VisibleTo), so IDs, ranges, counts, and reasons
// of spans the viewer cannot see never leave the store. An empty
// OccurrenceID selects every event. Results are ordered by the event's
// receipt Seq, then SpanIndex, then Index.
type DiagnosticFilter struct {
	Viewer       domain.Principal
	OccurrenceID string
}

// CommandFilter selects recorded lifecycle commands (D1) the way
// DiagnosticFilter selects diagnostics: only records whose source span
// boundary permits Viewer, ordered by receipt Seq, then Ordinal.
type CommandFilter struct {
	Viewer       domain.Principal
	OccurrenceID string
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
	// BlobReferrer returns the one referrer blob authorization needs (see
	// BlobReferrerFilter); an invalid filter is domain.ErrInvalidRecord.
	BlobReferrer(f BlobReferrerFilter) (Lookup, error)
	// CanonicalCandidates returns the live duplicate candidates f selects.
	CanonicalCandidates(f CanonicalFilter) (Lookup, error)
	// CurrentWorking returns the current Working snapshot f selects.
	CurrentWorking(f WorkingFilter) (Lookup, error)
	// SourceItems returns one page of the source items f selects.
	SourceItems(f SourceFilter) (Lookup, error)
	// VisibleReferences returns one page of the unresolved references f
	// selects, whether more remain, and the cursor to continue after.
	VisibleReferences(f VisibleReferenceFilter) (refs []domain.UnresolvedReference, more bool, next Cursor, err error)
	Relationships(f RelationshipFilter) ([]domain.Relationship, error)
	Event(eventID string) (domain.EventRecord, error)
	// Blob returns the blob with the given hash after verifying its bytes;
	// corrupt bytes fail with domain.ErrIntegrity.
	Blob(hash string) (domain.Blob, error)
	// CurrentVersion returns the item ID the current-version map holds for
	// key (FR-DIR-002, FR-TOOL-002). The identity is (task, access boundary,
	// namespace, ID): versions in different boundaries are independent, so a
	// boundary a caller cannot see never blocks or reveals itself through a
	// shared ID, and a parsed directive never collides with keyed agent state
	// of the same ID (M6, R6). A key naming another session is ErrNotFound; a
	// key that fails domain.CurrentKey.Validate is ErrInvalidRecord. The map
	// records only what SetCurrentVersion wrote: callers still check that the
	// named item is current (no incoming SUPERSEDES, not a duplicate).
	CurrentVersion(key domain.CurrentKey) (string, error)
	// CurrentVersions returns the item IDs the map holds for (namespace, id)
	// across every access boundary in a task, ordered by item ID; empty when
	// none exist. Callers filter by access before acting, so a version a
	// principal cannot see stays invisible (FR-DIR-002, FR-DIR-005). An
	// invalid namespace is ErrInvalidRecord.
	CurrentVersions(taskID string, ns domain.DirectiveNamespace, id string) ([]string, error)
	// Obligation returns the latest version of an obligation.
	Obligation(obligationID string) (domain.ObligationVersion, error)
	ObligationVersions(obligationID string) ([]domain.ObligationVersion, error)
	// Obligations returns the latest version of every obligation in a task,
	// ordered by ObligationID; an empty task ID returns all tasks.
	Obligations(taskID string) ([]domain.ObligationVersion, error)
	// ObligationsBySource returns every obligation version (current or
	// retired) whose SourceItemID is sourceItemID, ordered by ObligationID
	// then Version (D13, R9). It is bounded: limit must be positive
	// (domain.ErrInvalidRecord otherwise), and more than limit matches fail
	// with ErrLimitExceeded rather than returning a partial answer, so a
	// caller retiring bound obligations never misses one.
	ObligationsBySource(sourceItemID string, limit int) ([]domain.ObligationVersion, error)
	ObligationTransitions(obligationID string) ([]domain.ObligationTransition, error)
	// Receipt returns the immutable receipt of an accepted event by its
	// occurrence ID (D14): the original item values, diagnostics, commands,
	// links, and execution versions exactly as InsertIngestion stored them,
	// even after later lifecycle changes. A caller-keyed event's occurrence
	// is domain.CallerOccurrenceID(session, EventID), so ingestion looks an
	// EventID up before allocating any sequence number. The receipt is the
	// submitting principal's (its PayloadHash covers the principal); callers
	// compare the request before returning it and never expose it to
	// another principal.
	Receipt(occurrenceID string) (domain.IngestReceipt, error)
	// Envelope returns the replayable request stored with a receipt.
	Envelope(occurrenceID string) (domain.EventEnvelope, error)
	// Diagnostics returns the persisted diagnostics f selects (D16). A
	// viewer that fails validation is domain.ErrInvalidRecord.
	Diagnostics(f DiagnosticFilter) ([]domain.DiagnosticRecord, error)
	// LifecycleCommands returns the recorded commands f selects (D1).
	LifecycleCommands(f CommandFilter) ([]domain.LifecycleCommandRecord, error)
	// UnresolvedReference returns one unresolved reference by ID.
	UnresolvedReference(id string) (domain.UnresolvedReference, error)
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

// Tx is a read-write transaction for one session, as Update passes it to
// fn: a store's TxBase behind a Guard that implements Poison.
type Tx interface {
	TxBase
	// Poison marks the transaction failed (DUR-1.3): every later write on it
	// fails with err, and Update rolls back everything the transaction wrote
	// and returns err, even when fn returns nil or another error. The first
	// error wins; later calls are no-ops. Poison(nil) poisons with
	// ErrPoisoned. Reads keep working. Callers poison a transaction when a
	// multi-step operation fails partway, so no partial result can commit.
	Poison(err error)
}

// TxBase is the read-write transaction a store implements; Update wraps it
// in a Guard to form the Tx fn receives.
type TxBase interface {
	ReadTx

	// Allocated reports whether seq was allocated by NextSeq in this
	// transaction. Callers use it to confine an operation to records the
	// transaction itself created (for example, provenance may be linked
	// only when the derived item was inserted in the same transaction).
	Allocated(seq uint64) bool

	// NextSeq allocates the next sequence number. Numbers are dense and
	// strictly increasing per session; numbers allocated by a rolled-back
	// transaction are reused.
	NextSeq() uint64

	// InsertEvent records an event idempotency record. If the event ID
	// exists with the same request it returns the stored record and
	// existed=true without writing; with a different request it fails with
	// domain.ErrEventIDConflict.
	InsertEvent(e domain.EventRecord) (stored domain.EventRecord, existed bool, err error)

	// InsertIngestion stores an accepted event's replayable envelope and
	// immutable receipt, with the receipt's item snapshots, diagnostic
	// records, and lifecycle command records, in one indivisible write
	// (D14, D16, D1). Both must validate, name this session, and agree on
	// occurrence, EventID, principal, and payload hash; receipt.Seq must be
	// allocated in this transaction; every receipt item must be stored in
	// this session, with its Seq allocated in this transaction and its
	// stored value equal to the snapshot; every link target and resolved
	// command target must be a stored item (domain.ErrInvalidRecord
	// otherwise); every blob the envelope references must be stored in this
	// session with the referenced size (domain.ErrIntegrity otherwise). Reusing an occurrence fails with domain.ErrEventIDConflict
	// when the payload hash differs and domain.ErrImmutable otherwise. It
	// is a sequenced semantic write.
	InsertIngestion(env domain.EventEnvelope, receipt domain.IngestReceipt) error

	// InsertUnresolvedReference stores an immutable unresolved reference
	// (M5, R2). It must validate and name this session, its Seq must be
	// allocated in this transaction, and its declaring ItemID must be a
	// stored item (domain.ErrInvalidRecord otherwise); reusing an ID fails
	// with domain.ErrImmutable. It is a sequenced semantic write.
	InsertUnresolvedReference(r domain.UnresolvedReference) error

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

	// SetCurrentVersion points the item's current-version key
	// (domain.ContextItem.CurrentKey: its task, access boundary, namespace,
	// and directive ID) at the item. The item must exist in this session
	// (domain.ErrNotFound) and have a valid key (domain.ErrInvalidRecord). It
	// is a semantic write.
	SetCurrentVersion(itemID string) error

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
	// RetireObligationVersion marks a current obligation version noncurrent
	// and appends its audit event atomically (D13, FR-OBL-006). The stored
	// Revision must equal expectedRevision (domain.ErrVersionConflict
	// otherwise); the event must target the obligation (TargetObligation,
	// TargetID == obligationID) with a Seq allocated in this transaction,
	// which becomes the version's RetiredSeq (domain.ErrInvalidRecord
	// otherwise). A version that is already retired fails with
	// domain.ErrInvalidTransition. Status, evidence, and transitions are
	// kept. It returns the updated version.
	RetireObligationVersion(obligationID string, version, expectedRevision uint64, event domain.LifecycleEvent) (domain.ObligationVersion, error)
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
	// compare-and-swap on Version, appending event atomically. Every task
	// change is semantic (turns and status drive eligibility), so event is
	// required: a TargetTask audit event for this task with a Seq allocated
	// in this transaction.
	PutTask(t domain.TaskState, expectedVersion uint64, event domain.LifecycleEvent) (domain.TaskState, error)

	// AppendLifecycleEvent appends an audit event. TargetCall events are
	// reserved for the call ledger (internal/invocation), and a TargetCall
	// event's Seq is never shared with a semantic record: at commit, a
	// sequence number used by a TargetCall event and by an item,
	// relationship, event record, ingestion receipt, unresolved reference,
	// obligation version or transition, grant, or non-TargetCall lifecycle
	// event fails with
	// domain.ErrInvalidRecord,
	// so a semantic write cannot hide behind a ledger sequence number
	// (FR-CALL-001). Ledger records (calls, attempts) may share it.
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
	// one-reserving-call rule of InsertCall holds. Terminal calls
	// (COMPLETED, FAILED, ABANDONED) are immutable: any update fails with
	// domain.ErrImmutable. Attempts may change only on PREPARED -> SENT
	// (by exactly one); every other transition keeps the stored Attempts, so
	// evidence is always judged against the stored current attempt, never
	// an earlier one. Leaving SENT or UNKNOWN requires that current attempt
	// to be stored already in the matching closed state, so no transition
	// can outrun its evidence:
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
