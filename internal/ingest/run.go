package ingest

import (
	"errors"
	"time"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/gcqueue"
	"github.com/tdavison784/context-runtime/internal/graph"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/policy"
	"github.com/tdavison784/context-runtime/internal/store"
)

// run is one Apply in progress. Everything it creates is buffered into the
// receipt in creation order; the transaction makes it all-or-nothing.
type run struct {
	g          Ingester
	binding    *domain.OutcomeBinding // a provider outcome's originating context
	membership *OutcomeMembership     // how that outcome joins its exchange, if at all
	pol        *domain.Phase3Policy   // effective Phase 3 policy; nil only for frozen v2 (tests)
	obl        *obligation.Service    // built on first use from pol
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

	items    []domain.ContextItem
	rels     int
	refs     int         // unresolved references declared so far (ordinals)
	refLinks int         // optional REFERENCES edges written (ruling 1)
	derived  map[int]int // derived items per span (MaxItemsPerSpan across parts)

	// Per parse unit: ingestion diagnostics are reported after the
	// parser's, and a parser notice about an item survives only if that
	// item was written (R20.2).
	unitDiags []scopedDiag
	written   map[domain.ByteRange]domain.AccessBoundary // parser items written, by range
	refused   map[int]bool                               // sections ingestion refused
	diags     diagnostics
	commands  []domain.LifecycleCommandRecord
	dups      []domain.IngestLink
	repls     []domain.IngestLink

	suppliedBlobs map[string]bool // blob hashes whose bytes this event supplied
	unverified    map[string]bool // unverified items already reported (DUR-3.1)

	transcripts      map[int]domain.ContextItem // span index -> its transcript item
	opResults        []domain.OperationResult   // in operation order (P3-34)
	mutationReceipts []string                   // typed operations' receipts, in order
	created          map[string]Created         // operation alias -> what it created
	unitOp           uint64                     // operation (or span) index being ingested
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
	r.unverified = map[string]bool{}
	r.derived = map[int]int{}
	r.transcripts = map[int]domain.ContextItem{}

	// Resource control runs outside task lifecycle: it never creates a
	// task, opens a turn, or is refused because a task completed (P3-20,
	// P3-34).
	if !r.e.Control {
		if err := r.advanceTask(); err != nil {
			return domain.IngestReceipt{}, err
		}
	}
	if len(r.e.Operations) == 0 {
		for si := range r.e.Spans {
			r.unitOp = uint64(si)
			if err := r.ingestSpan(si); err != nil {
				return domain.IngestReceipt{}, err
			}
		}
	}
	for oi, op := range r.e.Operations {
		if err := r.operation(oi, op); err != nil {
			return domain.IngestReceipt{}, err
		}
	}
	if r.membership != nil {
		if r.pol == nil {
			return domain.IngestReceipt{}, domain.ErrUnsupportedSchema
		}
		if err := r.registerOutput(*r.membership); err != nil {
			return domain.IngestReceipt{}, err
		}
	}
	return r.commit()
}

// operation applies the oi-th operation of the event's ordered stream
// (P3-34). A span operation ingests its span, exactly once (ValidateV3);
// its result is readable at the span's transcript boundary, and an alias
// of it names what the span created. A typed operation runs through its
// registered handler.
func (r *run) operation(oi int, op domain.SemanticOperation) error {
	if op.Kind != domain.OperationSpan {
		return r.typedOperation(oi, op)
	}
	si, from := op.Span.Index, len(r.items)
	r.unitOp = uint64(oi)
	if err := r.ingestSpan(si); err != nil {
		return err
	}
	if op.Alias != "" {
		c, err := r.spanCreated(from)
		if err != nil {
			return err
		}
		r.alias(op.Alias, c)
	}
	r.opResults = append(r.opResults, domain.OperationResult{Index: oi, Kind: op.Kind, Alias: op.Alias, Access: r.transcripts[si].Access})
	return nil
}

// advanceTask creates the principal's task on its first task-bound event
// and opens a turn when the trusted envelope says so, exactly once per
// event (D18). Ownership is immutable: a task of another workflow is
// rejected, and a COMPLETED task is never reactivated by ingestion.
func (r *run) advanceTask() error {
	if r.binding != nil {
		return r.outcomeTask()
	}
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
	if r.openedTurn != 0 {
		// Each turn advance is one TTL trigger, identified by the new turn
		// (P3-39, SPEC-1.6); Collect evaluates TTL expiry itself.
		return r.enqueueGC(r.p, domain.GCTTL, t.TaskID, t.TurnID)
	}
	return nil
}

// enqueueGC produces a durable GC trigger in the event's transaction
// (P3-39, SPEC-1.6) through the leaf producer, under the event's RECORDED
// policy (SPEC-2.11): it alone decides whether the trigger is enabled and is
// recorded on the request. A task-less trigger produces nothing (H4): Phase
// 3 has no session-scoped GC. Frozen v2 history records no policy and
// produces no trigger.
func (r *run) enqueueGC(origin domain.Principal, trigger domain.GCTrigger, taskID, triggerID string) error {
	if r.pol == nil {
		return nil
	}
	_, err := gcqueue.Enqueue(r.tx, *r.pol, origin, trigger, taskID, triggerID)
	return err
}

// fill sets the fields every item of this event shares, so a prospective
// item can be compared (deduplication) before it is written.
func (r *run) fill(it domain.ContextItem) domain.ContextItem {
	it.EventID = r.e.EventID
	it.SessionID = r.p.SessionID
	it.WorkflowID = r.p.WorkflowID
	it.TaskID = r.p.TaskID
	it.AgentID = r.p.AgentID
	switch {
	case r.binding != nil:
		// A provider outcome belongs to the turn its call was issued in,
		// never the task's newest turn (P3-34).
		it.CreatedTurn, it.TurnID = r.binding.Turn, r.binding.TurnID
	case r.hasTask && r.task.Turn > 0:
		it.CreatedTurn = r.task.Turn
		it.TurnID = r.task.TurnID
	}
	// Under a Phase 3 policy a parsed directive's namespace is explicit
	// (P3-3); its current key is the same as the legacy fallback's.
	if r.pol != nil && it.DirectiveID != "" && it.Section != domain.SectionNone && it.Namespace == "" {
		it.Namespace = domain.NamespaceDirective
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
	if err := r.registerOwner(it); err != nil {
		return domain.ContextItem{}, err
	}
	it.ID = domain.DerivedItemID(r.p.SessionID, r.itemKey(), len(r.items))
	it.Seq = r.tx.NextSeq()
	validate := it.Validate
	if r.pol != nil {
		validate = it.ValidateSemantic
	}
	if err := validate(); err != nil {
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
	r.transcripts[si] = transcript
	actor, err := domain.SourceActor(r.p, span.Authority)
	if err != nil {
		return err
	}
	if err := r.detectDuplicate(si, 0, actor, transcript); err != nil {
		return err
	}
	if err := r.linkPendingReferences(si, actor, transcript); err != nil {
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
	for pi, part := range span.Parts {
		cp := part.Snapshot()
		if part.Type != domain.PartText {
			if err := r.blob(si, pi, part, access); err != nil {
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
	if err := r.settleItems(); err != nil {
		return domain.IngestReceipt{}, err
	}
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
	var (
		env domain.EventEnvelope
		err error
	)
	if r.pol != nil {
		rc.SchemaVersion, rc.RequestHashVersion, rc.Operations, rc.MutationReceiptIDs = domain.IngestReceiptSchemaV2, domain.RequestHashV3, r.opResults, r.mutationReceipts
		env, err = domain.NewSemanticEventEnvelope(r.p, r.occurrence, r.e, r.limits, *r.pol)
	} else {
		env, err = domain.NewEventEnvelope(r.p, r.occurrence, r.e)
	}
	if err != nil {
		return domain.IngestReceipt{}, err
	}
	if err := rc.Validate(); err != nil {
		return domain.IngestReceipt{}, err
	}
	if err := r.tx.InsertIngestion(env, rc); err != nil {
		return domain.IngestReceipt{}, err
	}
	return rc.Clone(), nil
}

// settleItems refreshes the receipt's snapshot of every item this event
// created that a later step of the same event changed, such as a goal it
// declared and then resolved (P3-35): the receipt records each item as the
// event left it, which is what the store holds when the receipt commits.
// Only executed mutations can change an item after its creation.
func (r *run) settleItems() error {
	if len(r.mutationReceipts) == 0 {
		return nil
	}
	for i, it := range r.items {
		stored, err := r.tx.Item(it.ID)
		if err != nil {
			return err
		}
		if stored.Version != it.Version {
			r.items[i] = stored.Clone()
		}
	}
	return nil
}

// blob stores supplied image/document bytes, or authorizes a reference to
// bytes already in the session (D19, R5): possession of a hash is not a
// capability, so a reference is accepted only when an item the principal
// can access already references that blob and the new item's boundary is
// within that item's. Missing and inaccessible references fail identically
// with the bare domain.ErrNotFound, checked before the blob is read. The
// store answers the one question asked, the earliest verified referrer the
// principal can access within access, filtering access inside its query
// (F1, SEC-1.1), so no other record is ever counted or observable.
func (r *run) blob(si, pi int, part domain.InputPart, access domain.AccessBoundary) error {
	cp := part.Snapshot()
	if part.Data != nil {
		r.suppliedBlobs[cp.BlobHash] = true
		return r.tx.InsertBlob(domain.Blob{SessionID: r.p.SessionID, Hash: cp.BlobHash, MediaType: part.MediaType, Data: part.Data})
	}
	if r.suppliedBlobs[cp.BlobHash] {
		return nil
	}
	found, err := r.tx.BlobReferrer(store.BlobReferrerFilter{Viewer: r.p, BlobHash: cp.BlobHash, Within: access})
	if err != nil {
		return err
	}
	r.reportUnverified(si, pi, domain.ByteRange{}, access, found.Unverified)
	for _, it := range found.Items {
		for _, q := range it.Parts {
			if q.BlobHash == cp.BlobHash && q.BlobSize == cp.BlobSize {
				return nil
			}
		}
	}
	return domain.ErrNotFound
}

// reportUnverified records one content-free ItemUnverified diagnostic per
// lookup match the store excluded because its stored content failed
// verification (DUR-1.4). The match never decides anything and never
// blocks the event; reading it directly still fails with ErrIntegrity. The
// store only reports matches the viewer can access, and the record is
// readable at access, so it discloses nothing hidden. Each item is reported
// once per event, however many lookups or pages return it (DUR-3.1).
func (r *run) reportUnverified(si, pi int, rng domain.ByteRange, access domain.AccessBoundary, ids []string) {
	for _, id := range ids {
		if r.unverified[id] {
			continue
		}
		r.unverified[id] = true
		r.diags.add(domain.Diagnostic{SpanIndex: si, PartIndex: pi, Code: domain.ItemUnverified, Reason: domain.ReasonUnverifiedItem, Range: rng}, access)
	}
}

// The obligation service settles pending invalidations inline before graph
// retires a replaced source's obligations (ruling M2).
var _ graph.PendingSettler = (*obligation.Service)(nil)

// graphOptions are the options of every graph supersession this run makes:
// the run's obligation service (the configured one, or one built from the
// recorded policy) settles before retirement (M2, K1 A3). A run with no
// semantic policy has no obligation service, and graph fails closed on a
// pending settlement.
func (r *run) graphOptions() []graph.Option {
	svc, err := r.obligations()
	if err != nil {
		return nil
	}
	return []graph.Option{graph.WithPendingSettler(svc)}
}
