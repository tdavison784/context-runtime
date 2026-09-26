package ingest

import (
	"errors"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
)

// run is one Apply in progress. Everything it creates is buffered into the
// receipt in creation order; the transaction makes it all-or-nothing.
type run struct {
	g          Ingester
	tx         store.Tx
	p          domain.Principal
	e          domain.Event
	limits     domain.Limits
	occurrence string
	payload    string
	now        time.Time

	seq        uint64 // the event's own sequence number
	task       domain.TaskState
	hasTask    bool
	openedTurn uint64

	items   []domain.ContextItem
	rels    int
	refs    int         // unresolved references declared so far (ordinals)
	derived map[int]int // derived items per span (MaxItemsPerSpan across parts)

	// Per parse unit: ingestion diagnostics are reported after the
	// parser's, and a parser notice about an item survives only if that
	// item was written (R20.2).
	unitDiags []domain.Diagnostic
	written   map[domain.ByteRange]bool // ranges of parser items written
	refused   map[int]bool              // sections ingestion refused
	diags     diagnostics
	commands  []domain.LifecycleCommandRecord
	dups      []domain.IngestLink
	repls     []domain.IngestLink

	suppliedBlobs map[string]bool // blob hashes whose bytes this event supplied
}

func isNotFound(err error) bool { return errors.Is(err, domain.ErrNotFound) }

// graphEventID is the event identity passed to graph operations: the
// occurrence ID, which is stable across retries of a caller-keyed event and
// unique for an anonymous one, so relationship and audit IDs never alias
// across anonymous events.
func (r *run) graphEventID() string { return r.occurrence }

// itemKey is the event key item IDs derive from (M3): the caller EventID,
// or the persisted occurrence ID of an anonymous event.
func (r *run) itemKey() string {
	if r.e.EventID != "" {
		return r.e.EventID
	}
	return r.occurrence
}

func (r *run) apply() (domain.IngestReceipt, error) {
	r.seq = r.tx.NextSeq()
	r.diags = newDiagnostics(r.limits)
	r.suppliedBlobs = map[string]bool{}
	r.derived = map[int]int{}

	if err := r.advanceTask(); err != nil {
		return domain.IngestReceipt{}, err
	}
	for si := range r.e.Spans {
		if err := r.ingestSpan(si); err != nil {
			return domain.IngestReceipt{}, err
		}
	}
	return r.commit()
}

// advanceTask creates the principal's task on its first task-bound event
// and opens a turn when the trusted envelope says so, exactly once per
// event (D18). Ownership is immutable: a task of another workflow is
// rejected, and a COMPLETED task is never reactivated by ingestion.
func (r *run) advanceTask() error {
	if r.p.TaskID == "" {
		return nil
	}
	t, err := r.tx.Task(r.p.TaskID)
	var expected uint64
	created := false
	switch {
	case isNotFound(err):
		t = domain.TaskState{SessionID: r.p.SessionID, TaskID: r.p.TaskID, WorkflowID: r.p.WorkflowID, Status: domain.TaskActive}
		created = true
	case err != nil:
		return err
	default:
		expected = t.Version
		if t.WorkflowID != r.p.WorkflowID {
			return domain.ErrInvalidAuthorityPromotion
		}
		if t.Status != domain.TaskActive {
			return domain.ErrInvalidTransition
		}
	}
	action := "created"
	if r.e.OpensTurn() {
		t.Turn++
		t.TurnID = domain.DerivedTurnID(r.p.SessionID, r.p.TaskID, t.Turn)
		r.openedTurn = t.Turn
		action = "turn_opened"
	}
	if created || r.openedTurn != 0 {
		t.Version = expected + 1
		ev := domain.LifecycleEvent{
			ID:         auditID("task", r.p.SessionID, r.occurrence, r.p.TaskID, action),
			SessionID:  r.p.SessionID,
			Seq:        r.tx.NextSeq(),
			TargetKind: domain.TargetTask,
			TargetID:   r.p.TaskID,
			Action:     action,
			To:         t.TurnID,
			Actor:      r.p,
			EventID:    r.e.EventID,
		}
		stored, err := r.tx.PutTask(t, expected, ev)
		if err != nil {
			return err
		}
		t = stored
	}
	r.task, r.hasTask = t, true
	return nil
}

// fill sets the fields every item of this event shares, so a prospective
// item can be compared (deduplication) before it is written.
func (r *run) fill(it domain.ContextItem) domain.ContextItem {
	it.EventID = r.e.EventID
	it.SessionID = r.p.SessionID
	it.WorkflowID = r.p.WorkflowID
	it.TaskID = r.p.TaskID
	it.AgentID = r.p.AgentID
	if r.hasTask && r.task.Turn > 0 {
		it.CreatedTurn = r.task.Turn
		it.TurnID = r.task.TurnID
	}
	it.ContentHash = domain.ContentHash(it.Parts)
	it.SemanticBytes = domain.SemanticBytes(it.Parts)
	it.CreatedAt = r.now
	it.Version = 1
	return it
}

// newItem allocates a filled item's ID and sequence number in creation
// order (M3), validates it, including D18 turn ownership, and inserts it.
func (r *run) newItem(it domain.ContextItem) (domain.ContextItem, error) {
	if len(r.items) >= r.limits.MaxEventItems {
		return domain.ContextItem{}, errLimit("MaxEventItems")
	}
	it = r.fill(it)
	it.ID = domain.DerivedItemID(r.p.SessionID, r.itemKey(), len(r.items))
	it.Seq = r.tx.NextSeq()
	if err := it.Validate(); err != nil {
		return domain.ContextItem{}, err
	}
	if err := it.ValidateTurnOwnership(); err != nil {
		return domain.ContextItem{}, err
	}
	if err := r.tx.InsertItem(it); err != nil {
		return domain.ContextItem{}, err
	}
	r.items = append(r.items, it.Clone())
	return it, nil
}

// boundary derives an item's access boundary (D8, D12): the requested
// scope's boundary for the principal intersected with within (the span's
// authenticated boundary for a transcript, the transcript's for anything
// derived from it), so a scope can narrow but never widen or declassify
// its source.
func (r *run) boundary(scope domain.Scope, within domain.AccessBoundary) (domain.AccessBoundary, bool) {
	return domain.Intersect(scope, domain.BoundaryFor(scope, r.p), within)
}

// ingestSpan stores the span's snapshot as one transcript item, then
// derives its semantic items (D8).
func (r *run) ingestSpan(si int) error {
	span := r.e.Spans[si]
	transcript, err := r.transcript(si, span)
	if err != nil {
		return err
	}
	actor, err := domain.SourceActor(r.p, span.Authority)
	if err != nil {
		return err
	}
	if err := r.detectDuplicate(actor, transcript); err != nil {
		return err
	}
	if err := r.linkPendingReferences(actor, transcript); err != nil {
		return err
	}
	return r.deriveSpan(si, span, transcript)
}

// transcript writes the span's verbatim transcript item (D8, policy v1).
func (r *run) transcript(si int, span domain.Span) (domain.ContextItem, error) {
	d, err := policy.ForTranscript(span.Authority)
	if err != nil {
		return domain.ContextItem{}, err
	}
	access, ok := r.boundary(d.Scope, span.Access)
	if !ok {
		return domain.ContextItem{}, domain.ErrInvalidAuthorityPromotion
	}
	parts := make([]domain.ContentPart, 0, len(span.Parts))
	for _, part := range span.Parts {
		cp := part.Snapshot()
		if part.Type != domain.PartText {
			if err := r.blob(part, access); err != nil {
				return domain.ContextItem{}, err
			}
		}
		parts = append(parts, cp)
	}
	it := domain.ContextItem{
		Role:       domain.RoleTranscript,
		Kind:       d.Kind,
		Generation: d.Generation,
		Authority:  span.Authority,
		Scope:      d.Scope,
		Access:     access,
		Residency:  d.Residency,
		GoalStatus: d.GoalStatus,
		Retention:  d.Retention,
		Parts:      parts,
	}
	if span.Source != nil {
		src := *span.Source
		it.Source = &src
	}
	return r.newItem(it)
}

// commit writes the replayable envelope and the immutable receipt, with its
// diagnostic and lifecycle-command records, in one store write (D14, D16).
func (r *run) commit() (domain.IngestReceipt, error) {
	rc := domain.IngestReceipt{
		SessionID:     r.p.SessionID,
		OccurrenceID:  r.occurrence,
		EventID:       r.e.EventID,
		Principal:     r.p,
		PayloadHash:   r.payload,
		Seq:           r.seq,
		Items:         r.items,
		Diagnostics:   r.diags.records(r.p.SessionID, r.occurrence, r.e.EventID, r.e.Spans),
		Lifecycle:     r.commands,
		Duplicates:    r.dups,
		Replacements:  r.repls,
		Versions:      r.g.Versions(),
		SchemaVersion: domain.IngestReceiptSchemaVersion,
	}
	if r.openedTurn != 0 {
		rc.OpenedTurn, rc.TurnID = r.openedTurn, r.task.TurnID
	}
	if len(rc.Diagnostics) > r.limits.MaxEventDiagnostics+len(r.e.Spans) {
		return domain.IngestReceipt{}, errLimit("MaxEventDiagnostics")
	}
	if err := rc.Validate(); err != nil {
		return domain.IngestReceipt{}, err
	}
	env, err := domain.NewEventEnvelope(r.p, r.occurrence, r.e)
	if err != nil {
		return domain.IngestReceipt{}, err
	}
	if err := r.tx.InsertIngestion(env, rc); err != nil {
		return domain.IngestReceipt{}, err
	}
	return rc.Clone(), nil
}

// blob stores supplied image/document bytes, or authorizes a reference to
// bytes already in the session (D19, R5): possession of a hash is not a
// capability, so a reference is accepted only when an item the principal
// can access already references that blob and the new item's boundary is
// within that item's. Missing and inaccessible references fail identically
// with the bare domain.ErrNotFound, checked before the blob is read. The
// referrers come from the store's bounded blob index (R19); more than the
// lookup limit rejects the event (store.ErrLimitExceeded, D17).
func (r *run) blob(part domain.InputPart, access domain.AccessBoundary) error {
	cp := part.Snapshot()
	if part.Data != nil {
		r.suppliedBlobs[cp.BlobHash] = true
		return r.tx.InsertBlob(domain.Blob{SessionID: r.p.SessionID, Hash: cp.BlobHash, MediaType: part.MediaType, Data: part.Data})
	}
	if r.suppliedBlobs[cp.BlobHash] {
		return nil
	}
	items, err := r.tx.ItemsByBlob(cp.BlobHash, r.g.lookupLimit())
	if err != nil {
		return err
	}
	for _, it := range items {
		if !it.Access.Permits(r.p) || !access.Within(it.Access) {
			continue
		}
		for _, q := range it.Parts {
			if q.BlobHash == cp.BlobHash && q.BlobSize == cp.BlobSize {
				return nil
			}
		}
	}
	return domain.ErrNotFound
}
