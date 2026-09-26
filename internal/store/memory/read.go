package memory

import (
	"cmp"
	"errors"
	"fmt"
	"iter"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// errDone reports use of a transaction after its Update or View returned.
var errDone = errors.New("memory store: transaction is finished")

// readTx implements store.ReadTx over a session's committed state plus, in
// an Update, the transaction's overlay.
type readTx struct {
	sessionID string
	lastSeq   uint64
	done      bool

	items        table[string, domain.ContextItem]
	rels         table[string, domain.Relationship]
	supersedes   index[string] // SUPERSEDES successors: FromID -> ToIDs
	supersededBy index[string] // SUPERSEDES predecessors: ToID -> FromIDs
	relsFrom     index[string]
	relsTo       index[string]
	relsByType   index[domain.RelationshipType]
	events       table[string, domain.EventRecord]
	blobs        table[string, domain.Blob]
	directives   table[directiveKey, string]
	obligations  table[obligationKey, domain.ObligationVersion]
	latest       table[string, uint64]
	transitions  table[string, domain.ObligationTransition]
	grants       table[string, domain.MutationGrant]
	tasks        table[string, domain.TaskState]
	lifecycle    table[string, domain.LifecycleEvent]
	convs        table[string, domain.Conversation]
	calls        table[string, domain.CallRecord]
	attempts     table[attemptKey, domain.CallAttempt]
	receipts     table[string, domain.IngestReceipt]
	envelopes    table[string, domain.EventEnvelope]
	references   table[string, domain.UnresolvedReference]
	itemsByBlob  index[string]
	duplicates   index[duplicateKey]
	refsByKey    index[string]
	itemsByKey   index[string]
	blobOwners   index[blobKey]
	canonical    liveIndex[canonicalKey]
	working      liveIndex[workingKey]
	sources      liveIndex[sourceKey]
	refOwners    index[sourceKey]
	itemsByTask  index[string]
}

var _ store.ReadTx = (*readTx)(nil)

func newReadTx(sessionID string, st *state, writable bool) *readTx {
	return &readTx{
		sessionID:    sessionID,
		lastSeq:      st.lastSeq,
		items:        newTable(st.items, writable, domain.ContextItem.Clone),
		rels:         newTable(st.rels, writable, domain.Relationship.Clone),
		supersedes:   newIndex(st.supersedes, writable),
		supersededBy: newIndex(st.supersededBy, writable),
		relsFrom:     newIndex(st.relsFrom, writable),
		relsTo:       newIndex(st.relsTo, writable),
		relsByType:   newIndex(st.relsByType, writable),
		events:       newTable(st.events, writable, domain.EventRecord.Clone),
		blobs:        newTable(st.blobs, writable, cloneBlob),
		directives:   newTable(st.directives, writable, same[string]),
		obligations:  newTable(st.obligations, writable, domain.ObligationVersion.Clone),
		latest:       newTable(st.latest, writable, same[uint64]),
		transitions:  newTable(st.transitions, writable, domain.ObligationTransition.Clone),
		grants:       newTable(st.grants, writable, domain.MutationGrant.Clone),
		tasks:        newTable(st.tasks, writable, same[domain.TaskState]),
		lifecycle:    newTable(st.lifecycle, writable, same[domain.LifecycleEvent]),
		convs:        newTable(st.convs, writable, same[domain.Conversation]),
		calls:        newTable(st.calls, writable, domain.CallRecord.Clone),
		attempts:     newTable(st.attempts, writable, same[domain.CallAttempt]),
		receipts:     newTable(st.receipts, writable, domain.IngestReceipt.Clone),
		envelopes:    newTable(st.envelopes, writable, domain.EventEnvelope.Clone),
		references:   newTable(st.references, writable, domain.UnresolvedReference.Clone),
		itemsByBlob:  newIndex(st.itemsByBlob, writable),
		duplicates:   newIndex(st.duplicates, writable),
		refsByKey:    newIndex(st.refsByKey, writable),
		itemsByKey:   newIndex(st.itemsByKey, writable),
		blobOwners:   newIndex(st.blobOwners, writable),
		canonical:    newLiveIndex(st.canonical, writable),
		working:      newLiveIndex(st.working, writable),
		sources:      newLiveIndex(st.sources, writable),
		refOwners:    newIndex(st.refOwners, writable),
		itemsByTask:  newIndex(st.itemsByTask, writable),
	}
}

func (r *readTx) finish() { r.done = true }

func (r *readTx) check() error {
	if r.done {
		return errDone
	}
	return nil
}

func notFound(kind string, id any) error {
	return fmt.Errorf("%s %v: %w", kind, id, domain.ErrNotFound)
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", domain.ErrInvalidRecord, fmt.Sprintf(format, args...))
}

func (r *readTx) SessionID() string { return r.sessionID }

func (r *readTx) LastSeq() uint64 { return r.lastSeq }

func (r *readTx) Item(id string) (domain.ContextItem, error) {
	if err := r.check(); err != nil {
		return domain.ContextItem{}, err
	}
	it, ok := r.items.get(id)
	if !ok {
		return domain.ContextItem{}, notFound("item", id)
	}
	return it, nil
}

func (r *readTx) Items(f store.ItemFilter) ([]domain.ContextItem, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	var out []domain.ContextItem
	add := func(it domain.ContextItem) {
		if matchItem(f, it) {
			out = append(out, it.Clone())
		}
	}
	if f.TaskID != "" { // a task filter reads the task's index entry (SPEC-1.3)
		for id := range r.itemsByTask.lookup(f.TaskID) {
			it, _ := r.items.peek(id)
			add(it)
		}
	} else {
		for _, it := range r.items.all() {
			add(it)
		}
	}
	slices.SortFunc(out, func(a, b domain.ContextItem) int {
		return cmp.Or(cmp.Compare(a.Seq, b.Seq), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func matchItem(f store.ItemFilter, it domain.ContextItem) bool {
	return (f.TaskID == "" || it.TaskID == f.TaskID) &&
		(f.AgentID == "" || it.AgentID == f.AgentID) &&
		(len(f.Kinds) == 0 || slices.Contains(f.Kinds, it.Kind)) &&
		(f.Residency == "" || it.Residency == f.Residency) &&
		(f.DirectiveID == "" || it.DirectiveID == f.DirectiveID) &&
		(f.EventID == "" || it.EventID == f.EventID) &&
		it.Seq >= f.MinSeq &&
		(f.MaxSeq == 0 || it.Seq <= f.MaxSeq)
}

func (r *readTx) Relationships(f store.RelationshipFilter) ([]domain.Relationship, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	// Scan the narrowest index the filter allows.
	var ids iter.Seq[string]
	switch {
	case f.FromID != "":
		ids = r.relsFrom.lookup(f.FromID)
	case f.ToID != "":
		ids = r.relsTo.lookup(f.ToID)
	case f.Type != "":
		ids = r.relsByType.lookup(f.Type)
	default:
		ids = func(yield func(string) bool) {
			for id := range r.rels.all() {
				if !yield(id) {
					return
				}
			}
		}
	}
	var out []domain.Relationship
	for id := range ids {
		rel, _ := r.rels.peek(id)
		if (f.Type == "" || rel.Type == f.Type) &&
			(f.FromID == "" || rel.FromID == f.FromID) &&
			(f.ToID == "" || rel.ToID == f.ToID) {
			out = append(out, rel.Clone())
		}
	}
	slices.SortFunc(out, func(a, b domain.Relationship) int {
		return cmp.Or(cmp.Compare(a.Seq, b.Seq), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r *readTx) Event(eventID string) (domain.EventRecord, error) {
	if err := r.check(); err != nil {
		return domain.EventRecord{}, err
	}
	e, ok := r.events.get(eventID)
	if !ok {
		return domain.EventRecord{}, notFound("event", eventID)
	}
	return e, nil
}

func (r *readTx) Blob(hash string) (domain.Blob, error) {
	if err := r.check(); err != nil {
		return domain.Blob{}, err
	}
	b, ok := r.blobs.get(hash)
	if !ok {
		return domain.Blob{}, notFound("blob", hash)
	}
	if domain.HashBytes(b.Data) != b.Hash {
		return domain.Blob{}, fmt.Errorf("blob %s: %w", hash, domain.ErrIntegrity)
	}
	return b, nil
}

func (r *readTx) CurrentVersion(key domain.CurrentKey) (string, error) {
	if err := r.check(); err != nil {
		return "", err
	}
	if err := key.Validate(); err != nil {
		return "", err
	}
	return r.current(key.TaskID, key.ID, key.Access, key.Namespace)
}

func (r *readTx) CurrentVersions(taskID string, ns domain.DirectiveNamespace, id string) ([]string, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	if !ns.Valid() {
		return nil, invalid("current versions: invalid namespace %q", ns)
	}
	var out []string
	for k, itemID := range r.directives.all() {
		if k.taskID == taskID && k.directiveID == id && k.namespace == ns {
			out = append(out, itemID)
		}
	}
	slices.Sort(out)
	return out, nil
}

// current looks up one pointer. A boundary in another session names nothing
// here.
func (r *readTx) current(taskID, id string, boundary domain.AccessBoundary, ns domain.DirectiveNamespace) (string, error) {
	itemID, ok := r.directives.get(directiveKey{taskID, id, boundary, ns})
	if !ok || boundary.SessionID != r.sessionID {
		return "", notFound("current version", taskID+"/"+id)
	}
	return itemID, nil
}

func (r *readTx) Obligation(obligationID string) (domain.ObligationVersion, error) {
	if err := r.check(); err != nil {
		return domain.ObligationVersion{}, err
	}
	v, ok := r.latest.peek(obligationID)
	if !ok {
		return domain.ObligationVersion{}, notFound("obligation", obligationID)
	}
	o, _ := r.obligations.get(obligationKey{obligationID, v})
	return o, nil
}

func (r *readTx) ObligationVersions(obligationID string) ([]domain.ObligationVersion, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	n, _ := r.latest.peek(obligationID)
	var out []domain.ObligationVersion
	for v := uint64(1); v <= n; v++ {
		o, _ := r.obligations.get(obligationKey{obligationID, v})
		out = append(out, o)
	}
	return out, nil
}

func (r *readTx) Obligations(taskID string) ([]domain.ObligationVersion, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	var out []domain.ObligationVersion
	for id, v := range r.latest.all() {
		o, _ := r.obligations.peek(obligationKey{id, v})
		if taskID == "" || o.TaskID == taskID {
			out = append(out, o.Clone())
		}
	}
	slices.SortFunc(out, func(a, b domain.ObligationVersion) int {
		return cmp.Compare(a.ObligationID, b.ObligationID)
	})
	return out, nil
}

func (r *readTx) ObligationsBySource(sourceItemID string, limit int) ([]domain.ObligationVersion, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, invalid("obligations by source: limit must be positive")
	}
	out := []domain.ObligationVersion{}
	for _, o := range r.obligations.all() {
		if o.SourceItemID != sourceItemID {
			continue
		}
		if len(out) == limit {
			return nil, store.ErrLimitExceeded
		}
		out = append(out, o.Clone())
	}
	slices.SortFunc(out, func(a, b domain.ObligationVersion) int {
		return cmp.Or(cmp.Compare(a.ObligationID, b.ObligationID), cmp.Compare(a.Version, b.Version))
	})
	return out, nil
}

func (r *readTx) ObligationTransitions(obligationID string) ([]domain.ObligationTransition, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	var out []domain.ObligationTransition
	for _, t := range r.transitions.all() {
		if t.ObligationID == obligationID {
			out = append(out, t.Clone())
		}
	}
	slices.SortFunc(out, func(a, b domain.ObligationTransition) int {
		return cmp.Or(cmp.Compare(a.Seq, b.Seq), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r *readTx) Grant(id string) (domain.MutationGrant, error) {
	if err := r.check(); err != nil {
		return domain.MutationGrant{}, err
	}
	g, ok := r.grants.get(id)
	if !ok {
		return domain.MutationGrant{}, notFound("grant", id)
	}
	return g, nil
}

func (r *readTx) Grants() ([]domain.MutationGrant, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	var out []domain.MutationGrant
	for _, g := range r.grants.all() {
		out = append(out, g.Clone())
	}
	slices.SortFunc(out, func(a, b domain.MutationGrant) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

func (r *readTx) Task(taskID string) (domain.TaskState, error) {
	if err := r.check(); err != nil {
		return domain.TaskState{}, err
	}
	t, ok := r.tasks.get(taskID)
	if !ok {
		return domain.TaskState{}, notFound("task", taskID)
	}
	return t, nil
}

func (r *readTx) LifecycleEvents(f store.LifecycleFilter) ([]domain.LifecycleEvent, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	var out []domain.LifecycleEvent
	for _, e := range r.lifecycle.all() {
		if (f.TargetKind == "" || e.TargetKind == f.TargetKind) &&
			(f.TargetID == "" || e.TargetID == f.TargetID) &&
			e.Seq >= f.MinSeq {
			out = append(out, e)
		}
	}
	slices.SortFunc(out, func(a, b domain.LifecycleEvent) int {
		return cmp.Or(cmp.Compare(a.Seq, b.Seq), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r *readTx) Conversation(conversationID string) (domain.Conversation, error) {
	if err := r.check(); err != nil {
		return domain.Conversation{}, err
	}
	c, ok := r.convs.get(conversationID)
	if !ok {
		return domain.Conversation{}, notFound("conversation", conversationID)
	}
	return c, nil
}

func (r *readTx) Call(callID string) (domain.CallRecord, error) {
	if err := r.check(); err != nil {
		return domain.CallRecord{}, err
	}
	c, ok := r.calls.get(callID)
	if !ok {
		return domain.CallRecord{}, notFound("call", callID)
	}
	return c, nil
}

func (r *readTx) Calls(f store.CallFilter) ([]domain.CallRecord, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	var out []domain.CallRecord
	for _, c := range r.calls.all() {
		if (f.ConversationID == "" || c.ConversationID == f.ConversationID) &&
			(len(f.States) == 0 || slices.Contains(f.States, c.State)) {
			out = append(out, c.Clone())
		}
	}
	slices.SortFunc(out, func(a, b domain.CallRecord) int {
		return cmp.Or(cmp.Compare(a.PreparedSeq, b.PreparedSeq), cmp.Compare(a.CallID, b.CallID))
	})
	return out, nil
}

func (r *readTx) CallAttempts(callID string) ([]domain.CallAttempt, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	var out []domain.CallAttempt
	for k, a := range r.attempts.all() {
		if k.callID == callID {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b domain.CallAttempt) int { return cmp.Compare(a.Attempt, b.Attempt) })
	return out, nil
}

func (r *readTx) Receipt(occurrenceID string) (domain.IngestReceipt, error) {
	if err := r.check(); err != nil {
		return domain.IngestReceipt{}, err
	}
	v, ok := r.receipts.get(occurrenceID)
	if !ok {
		return domain.IngestReceipt{}, notFound("receipt", occurrenceID)
	}
	return v, nil
}

func (r *readTx) Envelope(occurrenceID string) (domain.EventEnvelope, error) {
	if err := r.check(); err != nil {
		return domain.EventEnvelope{}, err
	}
	v, ok := r.envelopes.get(occurrenceID)
	if !ok {
		return domain.EventEnvelope{}, notFound("envelope", occurrenceID)
	}
	return v, nil
}

// visibleReceipts returns the receipts an occurrence filter selects, ordered
// by Seq, after validating the viewer.
func (r *readTx) visibleReceipts(viewer domain.Principal, occurrenceID string) ([]domain.IngestReceipt, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	if err := viewer.Validate(); err != nil {
		return nil, err
	}
	var out []domain.IngestReceipt
	for occ, rec := range r.receipts.all() {
		if occurrenceID == "" || occ == occurrenceID {
			out = append(out, rec)
		}
	}
	slices.SortFunc(out, func(a, b domain.IngestReceipt) int { return cmp.Compare(a.Seq, b.Seq) })
	return out, nil
}

func (r *readTx) Diagnostics(f store.DiagnosticFilter) ([]domain.DiagnosticRecord, error) {
	recs, err := r.visibleReceipts(f.Viewer, f.OccurrenceID)
	if err != nil {
		return nil, err
	}
	out := []domain.DiagnosticRecord{}
	for _, rec := range recs {
		for _, d := range rec.Diagnostics {
			if d.VisibleTo(f.Viewer) {
				out = append(out, d)
			}
		}
	}
	return out, nil
}

func (r *readTx) LifecycleCommands(f store.CommandFilter) ([]domain.LifecycleCommandRecord, error) {
	recs, err := r.visibleReceipts(f.Viewer, f.OccurrenceID)
	if err != nil {
		return nil, err
	}
	out := []domain.LifecycleCommandRecord{}
	for _, rec := range recs {
		for _, c := range rec.Lifecycle {
			if c.Access.Permits(f.Viewer) {
				out = append(out, c)
			}
		}
	}
	return out, nil
}

func (r *readTx) UnresolvedReference(id string) (domain.UnresolvedReference, error) {
	if err := r.check(); err != nil {
		return domain.UnresolvedReference{}, err
	}
	v, ok := r.references.get(id)
	if !ok {
		return domain.UnresolvedReference{}, notFound("unresolved reference", id)
	}
	return v, nil
}

func (r *readTx) UnresolvedReferences(f store.ReferenceFilter) ([]domain.UnresolvedReference, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	if f.Limit <= 0 {
		return nil, invalid("unresolved references: limit must be positive")
	}
	// A locator key lookup reads the key's index entry, never every
	// reference (R19).
	candidates := func(yield func(domain.UnresolvedReference) bool) {
		if f.LocatorKey != "" {
			for id := range r.refsByKey.lookup(f.LocatorKey) {
				v, _ := r.references.peek(id)
				if !yield(v) {
					return
				}
			}
			return
		}
		for _, v := range r.references.all() {
			if !yield(v) {
				return
			}
		}
	}
	out := []domain.UnresolvedReference{}
	for v := range candidates {
		if f.RuleVersion != "" && v.RuleVersion != f.RuleVersion {
			continue
		}
		if len(out) == f.Limit {
			return nil, store.ErrLimitExceeded
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b domain.UnresolvedReference) int {
		return cmp.Or(cmp.Compare(a.Seq, b.Seq), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

func (r *readTx) ItemsByBlob(blobHash string, limit int) ([]domain.ContextItem, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	if limit <= 0 || !domain.ValidHash(blobHash) {
		return nil, invalid("items by blob: positive limit and valid hash required")
	}
	return r.indexedItems(r.itemsByBlob.lookup(blobHash), limit)
}

func (r *readTx) ItemsBySourceKey(locatorKey string, limit int) ([]domain.ContextItem, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	if limit <= 0 || locatorKey == "" || len(locatorKey) > domain.MaxLocatorKeyBytes {
		return nil, invalid("items by source key: positive limit and a locator key required")
	}
	return r.indexedItems(r.itemsByKey.lookup(locatorKey), limit)
}

func (r *readTx) DuplicateCandidates(f store.DuplicateFilter) ([]domain.ContextItem, error) {
	if err := r.check(); err != nil {
		return nil, err
	}
	if err := f.Validate(); err != nil {
		return nil, err
	}
	key := duplicateKey{f.TaskID, f.Section, f.Role, f.Authority, f.Access, f.ContentHash}
	return r.indexedItems(r.duplicates.lookup(key), f.Limit)
}

// indexedItems loads at most limit indexed items, ordered by Seq then ID;
// more fail with store.ErrLimitExceeded.
func (r *readTx) indexedItems(ids iter.Seq[string], limit int) ([]domain.ContextItem, error) {
	out := []domain.ContextItem{}
	for id := range ids {
		if len(out) == limit {
			return nil, store.ErrLimitExceeded
		}
		it, _ := r.items.get(id)
		out = append(out, it)
	}
	slices.SortFunc(out, func(a, b domain.ContextItem) int {
		return cmp.Or(cmp.Compare(a.Seq, b.Seq), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}
