package memory

import (
	"cmp"
	"errors"
	"fmt"
	"reflect"
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

	items       table[string, domain.ContextItem]
	rels        table[string, domain.Relationship]
	supersedes  table[string, []string]
	events      table[string, domain.EventRecord]
	blobs       table[string, domain.Blob]
	directives  table[directiveKey, string]
	obligations table[obligationKey, domain.ObligationVersion]
	latest      table[string, uint64]
	transitions table[string, domain.ObligationTransition]
	grants      table[string, domain.MutationGrant]
	tasks       table[string, domain.TaskState]
	lifecycle   table[string, domain.LifecycleEvent]
	convs       table[string, domain.Conversation]
	calls       table[string, domain.CallRecord]
	attempts    table[attemptKey, domain.CallAttempt]
}

var _ store.ReadTx = (*readTx)(nil)

func newReadTx(sessionID string, st *state, writable bool) *readTx {
	return &readTx{
		sessionID:   sessionID,
		lastSeq:     st.lastSeq,
		items:       newTable(st.items, writable, domain.ContextItem.Clone),
		rels:        newTable(st.rels, writable, cloneRelationship),
		supersedes:  newTable(st.supersedes, writable, slices.Clone[[]string]),
		events:      newTable(st.events, writable, domain.EventRecord.Clone),
		blobs:       newTable(st.blobs, writable, cloneBlob),
		directives:  newTable(st.directives, writable, same[string]),
		obligations: newTable(st.obligations, writable, domain.ObligationVersion.Clone),
		latest:      newTable(st.latest, writable, same[uint64]),
		transitions: newTable(st.transitions, writable, domain.ObligationTransition.Clone),
		grants:      newTable(st.grants, writable, domain.MutationGrant.Clone),
		tasks:       newTable(st.tasks, writable, same[domain.TaskState]),
		lifecycle:   newTable(st.lifecycle, writable, same[domain.LifecycleEvent]),
		convs:       newTable(st.convs, writable, same[domain.Conversation]),
		calls:       newTable(st.calls, writable, domain.CallRecord.Clone),
		attempts:    newTable(st.attempts, writable, same[domain.CallAttempt]),
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
	var out []domain.Relationship
	for _, rel := range r.rels.all() {
		if (f.Type == "" || rel.Type == f.Type) &&
			(f.FromID == "" || rel.FromID == f.FromID) &&
			(f.ToID == "" || rel.ToID == f.ToID) {
			out = append(out, cloneRelationship(rel))
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

func (r *readTx) CurrentDirective(taskID, directiveID string) (string, error) {
	if err := r.check(); err != nil {
		return "", err
	}
	id, ok := r.directives.get(directiveKey{taskID, directiveID})
	if !ok {
		return "", notFound("directive", taskID+"/"+directiveID)
	}
	return id, nil
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
	n, ok := r.latest.peek(obligationID)
	if !ok {
		return nil, notFound("obligation", obligationID)
	}
	out := make([]domain.ObligationVersion, 0, n)
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
	if !r.latest.has(obligationID) {
		return nil, notFound("obligation", obligationID)
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
	if !r.calls.has(callID) {
		return nil, notFound("call", callID)
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

// tx implements store.Tx. Every write validates completely before touching
// the overlay, so a failed write leaves the transaction unchanged and the
// caller may continue.
type tx struct{ *readTx }

var _ store.Tx = (*tx)(nil)

func (t *tx) commit(st *state) {
	t.items.commit()
	t.rels.commit()
	t.supersedes.commit()
	t.events.commit()
	t.blobs.commit()
	t.directives.commit()
	t.obligations.commit()
	t.latest.commit()
	t.transitions.commit()
	t.grants.commit()
	t.tasks.commit()
	t.lifecycle.commit()
	t.convs.commit()
	t.calls.commit()
	t.attempts.commit()
	st.lastSeq = t.lastSeq
}

// own checks that a record belongs to the transaction's session.
func (t *tx) own(sessionID string) error {
	if err := t.check(); err != nil {
		return err
	}
	if sessionID != t.sessionID {
		return invalid("record belongs to another session")
	}
	return nil
}

func (t *tx) NextSeq() uint64 {
	if t.done {
		panic(errDone)
	}
	t.lastSeq++
	return t.lastSeq
}

func (t *tx) InsertEvent(e domain.EventRecord) (domain.EventRecord, bool, error) {
	if err := t.own(e.SessionID); err != nil {
		return domain.EventRecord{}, false, err
	}
	if err := e.Validate(); err != nil {
		return domain.EventRecord{}, false, err
	}
	if cur, ok := t.events.get(e.EventID); ok {
		if !cur.SameRequest(e) {
			return domain.EventRecord{}, false, fmt.Errorf("event %s: %w", e.EventID, domain.ErrEventIDConflict)
		}
		return cur, true, nil
	}
	t.events.put(e.EventID, e)
	return e.Clone(), false, nil
}

func (t *tx) InsertItem(it domain.ContextItem) error {
	if err := t.own(it.SessionID); err != nil {
		return err
	}
	if err := it.Validate(); err != nil {
		return err
	}
	if it.Version != 1 {
		return invalid("item %s: new items start at version 1", it.ID)
	}
	if it.Seq > t.lastSeq {
		return invalid("item %s: sequence %d was not allocated", it.ID, it.Seq)
	}
	if t.items.has(it.ID) {
		return fmt.Errorf("item %s: %w", it.ID, domain.ErrImmutable)
	}
	t.items.put(it.ID, it)
	return nil
}

func (t *tx) UpdateItem(id string, expectedVersion uint64, change domain.ItemChange) (domain.ContextItem, error) {
	if err := t.check(); err != nil {
		return domain.ContextItem{}, err
	}
	cur, ok := t.items.peek(id)
	if !ok {
		return domain.ContextItem{}, notFound("item", id)
	}
	if cur.Version != expectedVersion {
		return domain.ContextItem{}, fmt.Errorf("item %s: version %d, expected %d: %w",
			id, cur.Version, expectedVersion, domain.ErrVersionConflict)
	}
	next, err := change.Apply(cur)
	if err != nil {
		return domain.ContextItem{}, err
	}
	t.items.put(id, next)
	return next, nil
}

func (t *tx) InsertRelationship(r domain.Relationship) error {
	if err := t.own(r.SessionID); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if t.rels.has(r.ID) {
		return fmt.Errorf("relationship %s: %w", r.ID, domain.ErrImmutable)
	}
	if !t.items.has(r.FromID) || !t.items.has(r.ToID) {
		return fmt.Errorf("relationship %s: %w", r.ID, domain.ErrDanglingRelationship)
	}
	if r.Type == domain.RelSupersedes {
		cycle, err := domain.WouldCreateCycle(r.FromID, r.ToID, func(id string) ([]string, error) {
			succ, _ := t.supersedes.peek(id)
			return succ, nil
		})
		if err != nil {
			return err
		}
		if cycle {
			return fmt.Errorf("relationship %s: %w", r.ID, domain.ErrSupersessionCycle)
		}
		succ, _ := t.supersedes.peek(r.FromID)
		t.supersedes.put(r.FromID, append(slices.Clip(succ), r.ToID))
	}
	t.rels.put(r.ID, r)
	return nil
}

func (t *tx) SetCurrentDirective(taskID, directiveID, itemID string) error {
	if err := t.check(); err != nil {
		return err
	}
	if taskID == "" || directiveID == "" {
		return invalid("directive: task and directive IDs are required")
	}
	it, ok := t.items.peek(itemID)
	if !ok {
		return notFound("item", itemID)
	}
	if it.DirectiveID != directiveID {
		return invalid("item %s does not carry directive ID %s", itemID, directiveID)
	}
	t.directives.put(directiveKey{taskID, directiveID}, itemID)
	return nil
}

func (t *tx) InsertBlob(b domain.Blob) error {
	if err := t.own(b.SessionID); err != nil {
		return err
	}
	if err := b.Validate(); err != nil {
		return err
	}
	if t.blobs.has(b.Hash) {
		return nil // identical bytes: the hash matched
	}
	t.blobs.put(b.Hash, b)
	return nil
}

func (t *tx) InsertObligationVersion(o domain.ObligationVersion) error {
	if err := t.own(o.SessionID); err != nil {
		return err
	}
	if err := o.Validate(); err != nil {
		return err
	}
	if o.Revision != 1 {
		return invalid("obligation %s: new versions start at revision 1", o.ObligationID)
	}
	latest, _ := t.latest.peek(o.ObligationID)
	if o.Version != latest+1 {
		return fmt.Errorf("obligation %s: version %d, want %d: %w",
			o.ObligationID, o.Version, latest+1, domain.ErrVersionConflict)
	}
	t.obligations.put(obligationKey{o.ObligationID, o.Version}, o)
	t.latest.put(o.ObligationID, o.Version)
	return nil
}

func (t *tx) UpdateObligationVersion(o domain.ObligationVersion, expectedRevision uint64) (domain.ObligationVersion, error) {
	if err := t.own(o.SessionID); err != nil {
		return domain.ObligationVersion{}, err
	}
	key := obligationKey{o.ObligationID, o.Version}
	cur, ok := t.obligations.peek(key)
	if !ok {
		return domain.ObligationVersion{}, notFound("obligation", fmt.Sprintf("%s/%d", o.ObligationID, o.Version))
	}
	if cur.Revision != expectedRevision {
		return domain.ObligationVersion{}, fmt.Errorf("obligation %s/%d: revision %d, expected %d: %w",
			o.ObligationID, o.Version, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	next := cur.Clone()
	next.Status = o.Status
	next.Current = o.Current
	next.RetiredSeq = o.RetiredSeq
	next.EvidenceIDs = slices.Clone(o.EvidenceIDs)
	next.MaterializationDisabled = o.MaterializationDisabled
	next.Revision = expectedRevision + 1
	probe := o.Clone()
	probe.Revision = next.Revision
	if !reflect.DeepEqual(probe, next) {
		return domain.ObligationVersion{}, fmt.Errorf("obligation %s/%d: only mutable fields may change: %w",
			o.ObligationID, o.Version, domain.ErrImmutable)
	}
	if next.Status != cur.Status && !domain.ValidObligationTransition(cur.Status, next.Status) {
		return domain.ObligationVersion{}, fmt.Errorf("obligation %s/%d: %s -> %s: %w",
			o.ObligationID, o.Version, cur.Status, next.Status, domain.ErrInvalidTransition)
	}
	if err := next.Validate(); err != nil {
		return domain.ObligationVersion{}, err
	}
	t.obligations.put(key, next)
	return next, nil
}

func (t *tx) AppendObligationTransition(tr domain.ObligationTransition) error {
	if err := t.own(tr.SessionID); err != nil {
		return err
	}
	if err := tr.Validate(); err != nil {
		return err
	}
	if t.transitions.has(tr.ID) {
		return fmt.Errorf("obligation transition %s: %w", tr.ID, domain.ErrImmutable)
	}
	cur, ok := t.obligations.peek(obligationKey{tr.ObligationID, tr.Version})
	if !ok {
		return notFound("obligation", fmt.Sprintf("%s/%d", tr.ObligationID, tr.Version))
	}
	if cur.Status != tr.From {
		return fmt.Errorf("obligation %s/%d: transition from %s but status is %s: %w",
			tr.ObligationID, tr.Version, tr.From, cur.Status, domain.ErrInvalidTransition)
	}
	t.transitions.put(tr.ID, tr)
	return nil
}

func (t *tx) InsertGrant(g domain.MutationGrant) error {
	if err := t.own(g.SessionID); err != nil {
		return err
	}
	if err := g.Validate(); err != nil {
		return err
	}
	if t.grants.has(g.ID) {
		return fmt.Errorf("grant %s: %w", g.ID, domain.ErrImmutable)
	}
	t.grants.put(g.ID, g)
	return nil
}

func (t *tx) RevokeGrant(id string, seq uint64) error {
	if err := t.check(); err != nil {
		return err
	}
	g, ok := t.grants.get(id)
	if !ok {
		return notFound("grant", id)
	}
	if seq == 0 || seq < g.IssuedSeq {
		return invalid("grant %s: revocation sequence %d precedes issue", id, seq)
	}
	if g.RevokedSeq != 0 {
		return fmt.Errorf("grant %s: already revoked: %w", id, domain.ErrInvalidTransition)
	}
	g.RevokedSeq = seq
	t.grants.put(id, g)
	return nil
}

func (t *tx) PutTask(ts domain.TaskState, expectedVersion uint64) error {
	if err := t.own(ts.SessionID); err != nil {
		return err
	}
	if err := ts.Validate(); err != nil {
		return err
	}
	cur, _ := t.tasks.peek(ts.TaskID) // absent reads as version 0
	if cur.Version != expectedVersion {
		return fmt.Errorf("task %s: version %d, expected %d: %w", ts.TaskID, cur.Version, expectedVersion, domain.ErrVersionConflict)
	}
	if ts.Version != expectedVersion+1 {
		return invalid("task %s: version must be %d", ts.TaskID, expectedVersion+1)
	}
	t.tasks.put(ts.TaskID, ts)
	return nil
}

func (t *tx) AppendLifecycleEvent(e domain.LifecycleEvent) error {
	if err := t.own(e.SessionID); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if t.lifecycle.has(e.ID) {
		return fmt.Errorf("lifecycle event %s: %w", e.ID, domain.ErrImmutable)
	}
	t.lifecycle.put(e.ID, e)
	return nil
}

func (t *tx) PutConversation(c domain.Conversation, expectedRevision uint64) error {
	if err := t.own(c.SessionID); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	cur, ok := t.convs.peek(c.ConversationID) // absent reads as revision 0
	if cur.Revision != expectedRevision {
		return fmt.Errorf("conversation %s: revision %d, expected %d: %w",
			c.ConversationID, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	if c.Revision != expectedRevision+1 {
		return invalid("conversation %s: revision must be %d", c.ConversationID, expectedRevision+1)
	}
	if ok && (c.TaskID != cur.TaskID || c.AgentID != cur.AgentID) {
		return fmt.Errorf("conversation %s: task and agent cannot change: %w", c.ConversationID, domain.ErrImmutable)
	}
	t.convs.put(c.ConversationID, c)
	return nil
}

func (t *tx) InsertCall(c domain.CallRecord) error {
	if err := t.own(c.SessionID); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	if c.Revision != 1 {
		return invalid("call %s: new calls start at revision 1", c.CallID)
	}
	if t.calls.has(c.CallID) {
		return fmt.Errorf("call %s: %w", c.CallID, domain.ErrImmutable)
	}
	if c.State.Reserving() {
		if err := t.checkReservation(c); err != nil {
			return err
		}
	}
	t.calls.put(c.CallID, c)
	return nil
}

func (t *tx) UpdateCall(c domain.CallRecord, expectedRevision uint64) error {
	if err := t.own(c.SessionID); err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	cur, ok := t.calls.peek(c.CallID)
	if !ok {
		return notFound("call", c.CallID)
	}
	if cur.Revision != expectedRevision {
		return fmt.Errorf("call %s: revision %d, expected %d: %w", c.CallID, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	if c.Revision != expectedRevision+1 {
		return invalid("call %s: revision must be %d", c.CallID, expectedRevision+1)
	}
	if c.State != cur.State && !domain.ValidCallTransition(cur.State, c.State) {
		return fmt.Errorf("call %s: %s -> %s: %w", c.CallID, cur.State, c.State, domain.ErrInvalidTransition)
	}
	if !sameCallRequest(cur, c) {
		return fmt.Errorf("call %s: request fields cannot change: %w", c.CallID, domain.ErrImmutable)
	}
	if !cur.State.Reserving() && c.State.Reserving() {
		if err := t.checkReservation(c); err != nil {
			return err
		}
	}
	t.calls.put(c.CallID, c)
	return nil
}

// sameCallRequest reports whether b differs from a only in the fields the
// call lifecycle advances: state, attempt count, outcome, cancellation,
// completion, and revision. Everything else is frozen at PrepareCall
// (FR-CALL-001).
func sameCallRequest(a, b domain.CallRecord) bool {
	b = b.Clone()
	b.State, b.Attempts, b.OutcomeHash, b.Outcome = a.State, a.Attempts, a.OutcomeHash, a.Outcome
	b.CancelReason, b.FinishedSeq, b.Revision = a.CancelReason, a.FinishedSeq, a.Revision
	return reflect.DeepEqual(a, b)
}

// checkReservation enforces FR-CALL-005 defensively: at most one call per
// conversation is PREPARED, SENT, or UNKNOWN.
func (t *tx) checkReservation(c domain.CallRecord) error {
	for id, other := range t.calls.all() {
		if id != c.CallID && other.ConversationID == c.ConversationID && other.State.Reserving() {
			return fmt.Errorf("conversation %s: %w", c.ConversationID, domain.ErrCallInFlight)
		}
	}
	return nil
}

func (t *tx) PutCallAttempt(a domain.CallAttempt) error {
	if err := t.own(a.SessionID); err != nil {
		return err
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if !t.calls.has(a.CallID) {
		return notFound("call", a.CallID)
	}
	t.attempts.put(attemptKey{a.CallID, a.Attempt}, a)
	return nil
}
