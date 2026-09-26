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
	for _, it := range r.items.all() {
		if matchItem(f, it) {
			out = append(out, it.Clone())
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

func (r *readTx) CurrentDirective(taskID, directiveID string, boundary domain.AccessBoundary) (string, error) {
	if err := r.check(); err != nil {
		return "", err
	}
	id, err := r.current(taskID, directiveID, boundary, domain.NamespaceDirective)
	if errors.Is(err, domain.ErrNotFound) {
		return r.current(taskID, directiveID, boundary, domain.NamespaceAgentKey)
	}
	return id, err
}

func (r *readTx) CurrentDirectives(taskID, directiveID string) ([]string, error) {
	return currentDirectives(r, taskID, directiveID)
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

// currentDirectives is the deprecated namespace-agnostic view: the pointers
// of both namespaces, ordered by item ID.
func currentDirectives(r store.ReadTx, taskID, directiveID string) ([]string, error) {
	var out []string
	for _, ns := range []domain.DirectiveNamespace{domain.NamespaceDirective, domain.NamespaceAgentKey} {
		ids, err := r.CurrentVersions(taskID, ns, directiveID)
		if err != nil {
			return nil, err
		}
		out = append(out, ids...)
	}
	slices.Sort(out)
	return out, nil
}
