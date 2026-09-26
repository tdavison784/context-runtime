package memory

import (
	"fmt"
	"reflect"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// tx implements store.Tx. Every write validates completely before touching
// the overlay, so a failed write leaves the transaction unchanged and the
// caller may continue.
type tx struct {
	*readTx
	baseSeq uint64 // last committed sequence number when the transaction began

	// semantic records a write that changes semantic state; sequenced
	// records a write of a record carrying a sequence number allocated in
	// this transaction. Update enforces the semantic-write rule with them.
	semantic, sequenced bool
	// semSeqs are the sequences of Phase 3 companion writes, for the
	// TargetCall sharing check; deferred are their commit-time reference
	// checks (semantic.go).
	semSeqs  []uint64
	deferred []func() error
}

func (t *tx) markSemantic()  { t.semantic = true }
func (t *tx) markSequenced() { t.semantic, t.sequenced = true, true }

var _ store.TxBase = (*tx)(nil)

// commit folds the transaction into st and reports whether it wrote any
// record.
func (t *tx) commit(st *state) bool {
	wrote := t.items.dirty() || t.rels.dirty() || t.events.dirty() || t.blobs.dirty() ||
		t.directives.dirty() || t.obligations.dirty() || t.transitions.dirty() || t.grants.dirty() ||
		t.tasks.dirty() || t.lifecycle.dirty() || t.convs.dirty() || t.calls.dirty() || t.attempts.dirty() ||
		t.receipts.dirty() || t.envelopes.dirty() || t.references.dirty() || t.sem.dirty()
	t.items.commit()
	t.rels.commit()
	t.supersedes.commit()
	t.supersededBy.commit()
	t.relsFrom.commit()
	t.blobOwners.commit()
	t.canonical.commit()
	t.working.commit()
	t.sources.commit()
	t.currentIDs.commit()
	t.oblsBySource.commit()
	t.refOwners.commit()
	t.itemsByTask.commit()
	t.relsTo.commit()
	t.relsByType.commit()
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
	t.receipts.commit()
	t.envelopes.commit()
	t.references.commit()
	t.sem.commit()
	st.lastSeq = t.lastSeq
	return wrote
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

// Allocated implements store.Tx.
func (t *tx) Allocated(seq uint64) bool {
	return !t.done && seq > t.baseSeq && seq <= t.lastSeq
}

// fresh enforces the sequence rule: seq must have been allocated by NextSeq
// in this transaction.
func (t *tx) fresh(what string, seq uint64) error {
	if !t.Allocated(seq) {
		return invalid("%s: sequence %d was not allocated in this transaction", what, seq)
	}
	return nil
}

// freshIfChanged applies the sequence rule to a sequence-valued field only
// when a write introduces a new non-zero value.
func (t *tx) freshIfChanged(what string, old, seq uint64) error {
	if seq == 0 || seq == old {
		return nil
	}
	return t.fresh(what, seq)
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
	if err := t.fresh("event "+e.EventID, e.Seq); err != nil {
		return domain.EventRecord{}, false, err
	}
	t.events.put(e.EventID, e)
	t.markSequenced()
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
	if err := t.fresh("item "+it.ID, it.Seq); err != nil {
		return err
	}
	if t.items.has(it.ID) {
		return fmt.Errorf("item %s: %w", it.ID, domain.ErrImmutable)
	}
	for i, p := range it.Parts {
		if p.Type == domain.PartText {
			continue
		}
		b, ok := t.blobs.peek(p.BlobHash)
		if !ok || uint64(len(b.Data)) != p.BlobSize {
			return fmt.Errorf("item %s part %d: blob is missing or its size differs: %w", it.ID, i, domain.ErrIntegrity)
		}
	}
	t.items.put(it.ID, it)
	t.indexLookups(it)
	t.itemsByTask.add(it.TaskID, it.ID)
	t.markSequenced()
	return nil
}

func (t *tx) UpdateItem(id string, expectedVersion uint64, change domain.ItemChange, event domain.LifecycleEvent) (domain.ContextItem, error) {
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
	if err := t.checkTargetEvent(event, domain.TargetItem, id); err != nil {
		return domain.ContextItem{}, err
	}
	next, err := change.Apply(cur)
	if err != nil {
		return domain.ContextItem{}, err
	}
	t.items.put(id, next)
	t.lifecycle.put(event.ID, event)
	t.markSequenced()
	return next, nil
}

func (t *tx) InsertRelationship(r domain.Relationship) error {
	if err := t.own(r.SessionID); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if err := t.fresh("relationship "+r.ID, r.Seq); err != nil {
		return err
	}
	if t.rels.has(r.ID) {
		return fmt.Errorf("relationship %s: %w", r.ID, domain.ErrImmutable)
	}
	if !t.items.has(r.FromID) || !t.items.has(r.ToID) {
		return fmt.Errorf("relationship %s: %w", r.ID, domain.ErrDanglingRelationship)
	}
	if r.Type == domain.RelSupersedes {
		// A cycle through the new edge needs an existing edge into FromID;
		// a new version usually has none, so a long chain is not walked on
		// every append.
		if _, superseded := iterFirst(t.supersededBy.lookup(r.FromID)); superseded {
			cycle, err := domain.WouldCreateCycle(r.FromID, r.ToID, func(id string) ([]string, error) {
				return slices.Collect(t.supersedes.lookup(id)), nil
			})
			if err != nil {
				return err
			}
			if cycle {
				return fmt.Errorf("relationship %s: %w", r.ID, domain.ErrSupersessionCycle)
			}
		}
		t.supersedes.add(r.FromID, r.ToID)
		t.supersededBy.add(r.ToID, r.FromID)
	}
	t.rels.put(r.ID, r)
	t.relsFrom.add(relKey{r.Type, r.FromID}, r.ID)
	t.relsTo.add(relKey{r.Type, r.ToID}, r.ID)
	t.relsByType.add(r.Type, r.ID)
	// A superseded or duplicate item is no longer live (F1).
	switch r.Type {
	case domain.RelSupersedes:
		t.retireLookups(r.ToID, false)
	case domain.RelDuplicateOf:
		t.retireLookups(r.FromID, true)
	}
	t.markSequenced()
	return nil
}

func (t *tx) SetCurrentVersion(itemID string) error {
	if err := t.check(); err != nil {
		return err
	}
	it, ok := t.items.peek(itemID)
	if !ok {
		return notFound("item", itemID)
	}
	key, ok := it.CurrentKey()
	if !ok {
		return invalid("item %s has no directive ID", itemID)
	}
	if err := key.Validate(); err != nil {
		return fmt.Errorf("item %s: %w", itemID, err)
	}
	t.directives.put(directiveKey{key.TaskID, key.ID, key.Access, key.Namespace}, itemID)
	t.currentIDs.add(currentIDKey{key.TaskID, key.Namespace, key.ID}, key.Access)
	t.markSemantic()
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
	// Blobs are exempt from the semantic-write rule: they are inert until a
	// sequenced record references them.
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
	if o.Revision != 1 || o.Status != domain.ObligationUnresolved {
		return invalid("obligation %s: new versions start UNRESOLVED at revision 1", o.ObligationID)
	}
	if err := t.fresh("obligation "+o.ObligationID, o.CreatedSeq); err != nil {
		return err
	}
	latest, _ := t.latest.peek(o.ObligationID)
	if o.Version != latest+1 {
		return fmt.Errorf("obligation %s: version %d, want %d: %w",
			o.ObligationID, o.Version, latest+1, domain.ErrVersionConflict)
	}
	t.obligations.put(obligationKey{o.ObligationID, o.Version}, o)
	t.latest.put(o.ObligationID, o.Version)
	t.oblsBySource.add(o.SourceItemID, obligationKey{o.ObligationID, o.Version})
	t.markSequenced()
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
	if o.Status != cur.Status {
		return domain.ObligationVersion{}, fmt.Errorf("obligation %s/%d: status changes only through transitions: %w",
			o.ObligationID, o.Version, domain.ErrInvalidTransition)
	}
	next := cur.Clone()
	next.Current = o.Current
	next.RetiredSeq = o.RetiredSeq
	next.MaterializationDisabled = o.MaterializationDisabled
	next.Revision = expectedRevision + 1
	probe := o.Clone()
	probe.Revision = next.Revision
	if !sameObligation(probe, next) {
		return domain.ObligationVersion{}, fmt.Errorf("obligation %s/%d: only Current, RetiredSeq, and MaterializationDisabled may change: %w",
			o.ObligationID, o.Version, domain.ErrImmutable)
	}
	if err := next.Validate(); err != nil {
		return domain.ObligationVersion{}, err
	}
	if err := t.freshIfChanged("obligation "+o.ObligationID, cur.RetiredSeq, next.RetiredSeq); err != nil {
		return domain.ObligationVersion{}, err
	}
	t.obligations.put(key, next)
	t.markSemantic()
	return next, nil
}

func (t *tx) RetireObligationVersion(obligationID string, version, expectedRevision uint64, event domain.LifecycleEvent) (domain.ObligationVersion, error) {
	if err := t.check(); err != nil {
		return domain.ObligationVersion{}, err
	}
	key := obligationKey{obligationID, version}
	cur, ok := t.obligations.peek(key)
	if !ok {
		return domain.ObligationVersion{}, notFound("obligation", fmt.Sprintf("%s/%d", obligationID, version))
	}
	if cur.Revision != expectedRevision {
		return domain.ObligationVersion{}, fmt.Errorf("obligation %s/%d: revision %d, expected %d: %w",
			obligationID, version, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	if !cur.Current {
		return domain.ObligationVersion{}, fmt.Errorf("obligation %s/%d is already retired: %w", obligationID, version, domain.ErrInvalidTransition)
	}
	if err := t.checkTargetEvent(event, domain.TargetObligation, obligationID); err != nil {
		return domain.ObligationVersion{}, err
	}
	next := cur.Clone()
	next.Current, next.RetiredSeq, next.Revision = false, event.Seq, expectedRevision+1
	if err := next.Validate(); err != nil {
		return domain.ObligationVersion{}, err
	}
	t.obligations.put(key, next)
	t.lifecycle.put(event.ID, event)
	t.markSequenced()
	return next.Clone(), nil
}

// sameObligation compares versions treating nil and empty evidence alike.
func sameObligation(a, b domain.ObligationVersion) bool {
	if len(a.EvidenceIDs) == 0 && len(b.EvidenceIDs) == 0 {
		a.EvidenceIDs, b.EvidenceIDs = nil, nil
	}
	return reflect.DeepEqual(a, b)
}

func (t *tx) AppendObligationTransition(tr domain.ObligationTransition, expectedRevision uint64) (domain.ObligationVersion, error) {
	if err := t.own(tr.SessionID); err != nil {
		return domain.ObligationVersion{}, err
	}
	if err := tr.Validate(); err != nil {
		return domain.ObligationVersion{}, err
	}
	if err := t.fresh("obligation transition "+tr.ID, tr.Seq); err != nil {
		return domain.ObligationVersion{}, err
	}
	if t.transitions.has(tr.ID) {
		return domain.ObligationVersion{}, fmt.Errorf("obligation transition %s: %w", tr.ID, domain.ErrImmutable)
	}
	key := obligationKey{tr.ObligationID, tr.Version}
	cur, ok := t.obligations.peek(key)
	if !ok {
		return domain.ObligationVersion{}, notFound("obligation", fmt.Sprintf("%s/%d", tr.ObligationID, tr.Version))
	}
	if cur.Revision != expectedRevision {
		return domain.ObligationVersion{}, fmt.Errorf("obligation %s/%d: revision %d, expected %d: %w",
			tr.ObligationID, tr.Version, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	if !cur.Current {
		return domain.ObligationVersion{}, fmt.Errorf("obligation %s/%d: retired versions do not transition: %w",
			tr.ObligationID, tr.Version, domain.ErrInvalidTransition)
	}
	if cur.Status != tr.From {
		return domain.ObligationVersion{}, fmt.Errorf("obligation %s/%d: transition from %s but status is %s: %w",
			tr.ObligationID, tr.Version, tr.From, cur.Status, domain.ErrInvalidTransition)
	}
	next := cur.Clone()
	next.Status = tr.To
	next.EvidenceIDs = nil
	if tr.To == domain.ObligationSatisfied {
		next.EvidenceIDs = slices.Clone(tr.EvidenceIDs)
	}
	next.Revision++
	t.transitions.put(tr.ID, tr)
	t.obligations.put(key, next)
	t.markSequenced()
	return next, nil
}

func (t *tx) InsertGrant(g domain.MutationGrant) error {
	if err := t.own(g.SessionID); err != nil {
		return err
	}
	if err := g.Validate(); err != nil {
		return err
	}
	if g.RevokedSeq != 0 {
		return invalid("grant %s: a new grant cannot be revoked", g.ID)
	}
	if err := t.fresh("grant "+g.ID, g.IssuedSeq); err != nil {
		return err
	}
	if t.grants.has(g.ID) {
		return fmt.Errorf("grant %s: %w", g.ID, domain.ErrImmutable)
	}
	t.grants.put(g.ID, g)
	t.markSequenced()
	return nil
}

func (t *tx) RevokeGrant(id string, event domain.LifecycleEvent) (domain.MutationGrant, error) {
	if err := t.check(); err != nil {
		return domain.MutationGrant{}, err
	}
	g, ok := t.grants.get(id)
	if !ok {
		return domain.MutationGrant{}, notFound("grant", id)
	}
	if g.RevokedSeq != 0 {
		return domain.MutationGrant{}, fmt.Errorf("grant %s: already revoked: %w", id, domain.ErrInvalidTransition)
	}
	if err := t.checkTargetEvent(event, domain.TargetGrant, id); err != nil {
		return domain.MutationGrant{}, err
	}
	g.RevokedSeq = event.Seq
	t.grants.put(id, g)
	t.lifecycle.put(event.ID, event)
	t.markSequenced()
	return g, nil
}

func (t *tx) PutTask(ts domain.TaskState, expectedVersion uint64, event domain.LifecycleEvent) (domain.TaskState, error) {
	if err := t.own(ts.SessionID); err != nil {
		return domain.TaskState{}, err
	}
	cur, _ := t.tasks.peek(ts.TaskID) // absent reads as version 0
	if cur.Version != expectedVersion {
		return domain.TaskState{}, fmt.Errorf("task %s: version %d, expected %d: %w",
			ts.TaskID, cur.Version, expectedVersion, domain.ErrVersionConflict)
	}
	ts.Version = expectedVersion + 1
	if err := ts.Validate(); err != nil {
		return domain.TaskState{}, err
	}
	if err := t.freshIfChanged("task "+ts.TaskID, cur.CompletedSeq, ts.CompletedSeq); err != nil {
		return domain.TaskState{}, err
	}
	if err := t.checkTargetEvent(event, domain.TargetTask, ts.TaskID); err != nil {
		return domain.TaskState{}, err
	}
	t.lifecycle.put(event.ID, event)
	t.tasks.put(ts.TaskID, ts)
	t.markSequenced()
	return ts, nil
}

// checkTargetEvent validates an audit event that must describe a change to
// the given target.
func (t *tx) checkTargetEvent(e domain.LifecycleEvent, kind domain.TargetKind, id string) error {
	if err := t.checkLifecycleEvent(e); err != nil {
		return err
	}
	if e.TargetKind != kind || e.TargetID != id {
		return invalid("lifecycle event %s targets %s %s, want %s %s", e.ID, e.TargetKind, e.TargetID, kind, id)
	}
	return nil
}

// checkLifecycleEvent validates an audit event about to be appended.
func (t *tx) checkLifecycleEvent(e domain.LifecycleEvent) error {
	if err := t.own(e.SessionID); err != nil {
		return err
	}
	if err := e.Validate(); err != nil {
		return err
	}
	if err := t.fresh("lifecycle event "+e.ID, e.Seq); err != nil {
		return err
	}
	if t.lifecycle.has(e.ID) {
		return fmt.Errorf("lifecycle event %s: %w", e.ID, domain.ErrImmutable)
	}
	return nil
}

func (t *tx) AppendLifecycleEvent(e domain.LifecycleEvent) error {
	if err := t.checkLifecycleEvent(e); err != nil {
		return err
	}
	t.lifecycle.put(e.ID, e)
	// TargetCall events belong to the call ledger, which is not semantic
	// state.
	if e.TargetKind != domain.TargetCall {
		t.markSequenced()
	}
	return nil
}

func (t *tx) PutConversation(c domain.Conversation, expectedRevision uint64) (domain.Conversation, error) {
	if err := t.own(c.SessionID); err != nil {
		return domain.Conversation{}, err
	}
	cur, ok := t.convs.peek(c.ConversationID) // absent reads as revision 0
	if cur.Revision != expectedRevision {
		return domain.Conversation{}, fmt.Errorf("conversation %s: revision %d, expected %d: %w",
			c.ConversationID, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	c.Revision = expectedRevision + 1
	if err := c.Validate(); err != nil {
		return domain.Conversation{}, err
	}
	if ok && (c.TaskID != cur.TaskID || c.AgentID != cur.AgentID) {
		return domain.Conversation{}, fmt.Errorf("conversation %s: task and agent cannot change: %w", c.ConversationID, domain.ErrImmutable)
	}
	t.convs.put(c.ConversationID, c)
	return c, nil
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
	// Later states need attempt evidence that only UpdateCall checks, so a
	// call enters the ledger PREPARED with no attempts.
	if c.State != domain.CallPrepared || c.Attempts != 0 {
		return fmt.Errorf("call %s: new calls start PREPARED with no attempts: %w", c.CallID, domain.ErrInvalidTransition)
	}
	if err := t.fresh("call "+c.CallID, c.PreparedSeq); err != nil {
		return err
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
	t.noteReservation(domain.CallRecord{}, c)
	return nil
}

func (t *tx) UpdateCall(c domain.CallRecord, expectedRevision uint64) (domain.CallRecord, error) {
	if err := t.own(c.SessionID); err != nil {
		return domain.CallRecord{}, err
	}
	cur, ok := t.calls.peek(c.CallID)
	if !ok {
		return domain.CallRecord{}, notFound("call", c.CallID)
	}
	if cur.Revision != expectedRevision {
		return domain.CallRecord{}, fmt.Errorf("call %s: revision %d, expected %d: %w",
			c.CallID, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	if cur.State.Terminal() {
		return domain.CallRecord{}, fmt.Errorf("call %s: terminal calls are immutable: %w", c.CallID, domain.ErrImmutable)
	}
	c = c.Clone()
	c.Revision = expectedRevision + 1
	if err := c.Validate(); err != nil {
		return domain.CallRecord{}, err
	}
	if c.State != cur.State && !domain.ValidCallTransition(cur.State, c.State) {
		return domain.CallRecord{}, fmt.Errorf("call %s: %s -> %s: %w", c.CallID, cur.State, c.State, domain.ErrInvalidTransition)
	}
	// Attempts advances only when an attempt is sent, so evidence is always
	// judged against the stored current attempt.
	wantAttempts := cur.Attempts
	if cur.State == domain.CallPrepared && c.State == domain.CallSent {
		wantAttempts++
	}
	if c.Attempts != wantAttempts {
		return domain.CallRecord{}, fmt.Errorf("call %s: attempts %d, want %d: %w",
			c.CallID, c.Attempts, wantAttempts, domain.ErrInvalidTransition)
	}
	if c.State != cur.State && !t.hasEvidence(cur.State, c) {
		return domain.CallRecord{}, fmt.Errorf("call %s: %s -> %s without matching attempt %d: %w",
			c.CallID, cur.State, c.State, c.Attempts, domain.ErrInvalidTransition)
	}
	if !sameCallProposal(cur, c) {
		return domain.CallRecord{}, fmt.Errorf("call %s: frozen proposal fields cannot change: %w", c.CallID, domain.ErrImmutable)
	}
	if err := t.freshIfChanged("call "+c.CallID, cur.FinishedSeq, c.FinishedSeq); err != nil {
		return domain.CallRecord{}, err
	}
	if !cur.State.Reserving() && c.State.Reserving() {
		if err := t.checkReservation(c); err != nil {
			return domain.CallRecord{}, err
		}
	}
	t.calls.put(c.CallID, c)
	t.noteReservation(cur, c)
	return c, nil
}

// sameCallProposal reports whether b differs from a only in the fields the
// call lifecycle advances: state, attempt count, outcome, reason, completion,
// and revision. Everything else is frozen at PrepareCall (FR-CALL-001).
func sameCallProposal(a, b domain.CallRecord) bool {
	b = b.Clone()
	b.State, b.Attempts, b.OutcomeHash, b.Outcome = a.State, a.Attempts, a.OutcomeHash, a.Outcome
	b.Reason, b.FinishedSeq, b.Revision = a.Reason, a.FinishedSeq, a.Revision
	return reflect.DeepEqual(a, b)
}

// hasEvidence reports whether the stored attempt c.Attempts justifies moving
// c from state from to c.State: no call transition may outrun the transport
// attempt that caused it.
func (t *tx) hasEvidence(from domain.CallState, c domain.CallRecord) bool {
	if from != domain.CallSent && from != domain.CallUnknown && !(from == domain.CallPrepared && c.State == domain.CallSent) {
		return true // PREPARED -> FAILED is a cancellation of an unsent call
	}
	a, ok := t.attempts.peek(attemptKey{c.CallID, c.Attempts})
	if !ok {
		return false
	}
	switch c.State {
	case domain.CallSent:
		return a.State == domain.AttemptSent
	case domain.CallCompleted:
		return a.State == domain.AttemptCompleted && a.OutcomeHash == c.OutcomeHash
	case domain.CallFailed:
		return a.State == domain.AttemptFailed && a.OutcomeHash == c.OutcomeHash
	case domain.CallPrepared:
		return from == domain.CallSent && a.State == domain.AttemptFailed && a.Retryable
	case domain.CallUnknown:
		return a.State == domain.AttemptUnknown
	case domain.CallAbandoned:
		return a.State == domain.AttemptAbandoned
	}
	return false
}

// checkReservation enforces FR-CALL-005: at most one call per conversation
// is PREPARED, SENT, or UNKNOWN.
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
	call, ok := t.calls.peek(a.CallID)
	if !ok {
		return notFound("call", a.CallID)
	}
	key := attemptKey{a.CallID, a.Attempt}
	cur, exists := t.attempts.peek(key)
	if !exists {
		if a.Attempt > 1 && !t.attempts.has(attemptKey{a.CallID, a.Attempt - 1}) {
			return invalid("call attempt %s/%d: attempts are numbered densely from 1", a.CallID, a.Attempt)
		}
		if a.State != domain.AttemptSent || call.State != domain.CallPrepared {
			return fmt.Errorf("call attempt %s/%d: a new attempt starts SENT from a PREPARED call: %w",
				a.CallID, a.Attempt, domain.ErrInvalidTransition)
		}
		if err := t.fresh("call attempt "+a.CallID, a.SentSeq); err != nil {
			return err
		}
		t.attempts.put(key, a)
		return nil
	}
	if a == cur {
		return nil
	}
	switch cur.State {
	case domain.AttemptCompleted, domain.AttemptFailed, domain.AttemptAbandoned:
		return fmt.Errorf("call attempt %s/%d: closed attempts are immutable: %w", a.CallID, a.Attempt, domain.ErrImmutable)
	}
	probe := a
	probe.State, probe.OutcomeHash, probe.Retryable = cur.State, cur.OutcomeHash, cur.Retryable
	probe.FinishedSeq, probe.FinishedAt = cur.FinishedSeq, cur.FinishedAt
	if probe != cur {
		return fmt.Errorf("call attempt %s/%d: only state, outcome, and finish fields may change: %w",
			a.CallID, a.Attempt, domain.ErrImmutable)
	}
	if !domain.ValidAttemptTransition(cur.State, a.State) {
		return fmt.Errorf("call attempt %s/%d: %s -> %s: %w", a.CallID, a.Attempt, cur.State, a.State, domain.ErrInvalidTransition)
	}
	if err := t.freshIfChanged("call attempt "+a.CallID, cur.FinishedSeq, a.FinishedSeq); err != nil {
		return err
	}
	t.attempts.put(key, a)
	return nil
}

// checkLedgerSeqs enforces that a TargetCall event's sequence number is not
// shared with a semantic record written in the same transaction, so a
// semantic write cannot hide behind a ledger sequence number (FR-CALL-001).
// Call and attempt sequence fields may share it.
func (t *tx) checkLedgerSeqs() error {
	ledger := map[uint64]bool{}
	for _, e := range t.lifecycle.over {
		if e.TargetKind == domain.TargetCall {
			ledger[e.Seq] = true
		}
	}
	if len(ledger) == 0 {
		return nil
	}
	seqs := slices.Clone(t.semSeqs)
	for _, it := range t.items.over {
		seqs = append(seqs, it.Seq)
	}
	for _, r := range t.rels.over {
		seqs = append(seqs, r.Seq)
	}
	for _, e := range t.events.over {
		seqs = append(seqs, e.Seq)
	}
	for _, o := range t.obligations.over {
		seqs = append(seqs, o.CreatedSeq)
	}
	for _, tr := range t.transitions.over {
		seqs = append(seqs, tr.Seq)
	}
	for _, g := range t.grants.over {
		seqs = append(seqs, g.IssuedSeq)
	}
	for _, r := range t.receipts.over {
		seqs = append(seqs, r.Seq)
	}
	for _, r := range t.references.over {
		seqs = append(seqs, r.Seq)
	}
	for _, e := range t.lifecycle.over {
		if e.TargetKind != domain.TargetCall {
			seqs = append(seqs, e.Seq)
		}
	}
	for _, seq := range seqs {
		if ledger[seq] {
			return invalid("sequence %d is used by both a TargetCall event and a semantic record", seq)
		}
	}
	return nil
}

func (t *tx) InsertIngestion(env domain.EventEnvelope, r domain.IngestReceipt) error {
	if err := t.check(); err != nil {
		return err
	}
	if err := store.ValidateIngestion(t.sessionID, env, r); err != nil {
		return err
	}
	if old, ok := t.receipts.peek(r.OccurrenceID); ok {
		if old.PayloadHash != r.PayloadHash {
			return fmt.Errorf("occurrence %s: %w", r.OccurrenceID, domain.ErrEventIDConflict)
		}
		return fmt.Errorf("receipt %s: %w", r.OccurrenceID, domain.ErrImmutable)
	}
	if err := t.fresh("receipt "+r.OccurrenceID, r.Seq); err != nil {
		return err
	}
	for _, snap := range r.Items {
		stored, ok := t.items.peek(snap.ID)
		if !ok || !t.Allocated(snap.Seq) || !store.ReceiptItemMatches(stored, snap) {
			return invalid("receipt %s: item %s is not the item this transaction stored", r.OccurrenceID, snap.ID)
		}
	}
	for _, links := range [][]domain.IngestLink{r.Duplicates, r.Replacements} {
		for _, l := range links {
			if !t.items.has(l.TargetID) {
				return invalid("receipt %s: link target %s is not stored", r.OccurrenceID, l.TargetID)
			}
		}
	}
	for _, c := range r.Lifecycle {
		if c.Resolution != domain.TargetResolved {
			continue
		}
		if it, ok := t.items.peek(c.ResolvedItemID); !ok || c.ResolvedVersion > it.Version {
			return invalid("receipt %s: resolved command target is not stored", r.OccurrenceID)
		}
	}
	for _, span := range env.Event.Spans {
		for _, p := range span.Parts {
			if p.BlobHash == "" {
				continue
			}
			if b, ok := t.blobs.peek(p.BlobHash); !ok || uint64(len(b.Data)) != p.BlobSize {
				return fmt.Errorf("envelope references blob %s, which is not stored: %w", p.BlobHash, domain.ErrIntegrity)
			}
		}
	}
	t.receipts.put(r.OccurrenceID, r)
	t.envelopes.put(env.OccurrenceID, env)
	t.markSequenced()
	return nil
}

func (t *tx) InsertUnresolvedReference(r domain.UnresolvedReference) error {
	if err := t.own(r.SessionID); err != nil {
		return err
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if err := t.fresh("unresolved reference "+r.ID, r.Seq); err != nil {
		return err
	}
	if !t.items.has(r.ItemID) {
		return invalid("unresolved reference %s: declaring item %s is not stored", r.ID, r.ItemID)
	}
	if t.references.has(r.ID) {
		return fmt.Errorf("unresolved reference %s: %w", r.ID, domain.ErrImmutable)
	}
	t.references.put(r.ID, r)
	t.refOwners.add(sourceKey{r.LocatorKey, ownersOf(r.Access)}, seqRef{r.Seq, r.ID})
	t.markSequenced()
	return nil
}
