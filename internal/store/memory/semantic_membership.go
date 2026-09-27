package memory

import (
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Coverage (P3-6), logical membership (P3-7), checkpoints (P3-27), and owner
// registrations (P3-32). Stores enforce structure, exact references, keys,
// the exchange state machine and CAS; services prove completeness,
// applicability and authority (schema manifest).

func immutable(what, id string) error { return fmt.Errorf("%s %s: %w", what, id, domain.ErrImmutable) }

func transition(format string, args ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, args...), domain.ErrInvalidTransition)
}

// --- Coverage ---

func (t *semTx) InsertCoverage(c domain.CoverageRecord, members []domain.CoverageMember) error {
	if err := t.t.companion("coverage", c.SemanticMeta, c.Validate); err != nil {
		return err
	}
	if t.r.sem.coverages.has(c.ID) {
		return immutable("coverage", c.ID)
	}
	for _, m := range members {
		if err := t.t.own(m.SessionID); err != nil {
			return err
		}
		key, err := m.Key()
		if err != nil {
			return err
		}
		if m.ID != key {
			return invalid("coverage %s: member ID must be its canonical key", c.ID)
		}
	}
	sig, err := domain.CoverageSignature(c, members)
	if err != nil {
		return invalid("coverage %s: %v", c.ID, err)
	}
	if sig != c.Signature {
		return invalid("coverage %s: signature does not match members", c.ID)
	}
	for _, m := range members {
		if err := t.checkMember(c, m); err != nil {
			return err
		}
	}
	t.r.sem.coverages.put(c.ID, c)
	t.r.sem.covMembers.put(c.ID, members)
	seen := map[string]bool{}
	for _, m := range members {
		if m.Source != nil && !seen[m.Source.ItemID] {
			seen[m.Source.ItemID] = true
			t.r.sem.covBySource.add(covSourceKey{m.Source.ItemID, c.Purpose}, seqRef{c.Seq, c.ID})
		}
	}
	t.t.sequencedWrite(c.Seq)
	return nil
}

// checkMember resolves one member's exact reference in this session.
func (t *semTx) checkMember(c domain.CoverageRecord, m domain.CoverageMember) error {
	switch {
	case m.Source != nil:
		if err := t.checkContent(*m.Source); err != nil {
			return fmt.Errorf("coverage %s: %w", c.ID, err)
		}
		if m.LeaseID != "" {
			// A lease member names its lease's exact source content.
			if l, ok := t.r.sem.ret.leases.peek(m.LeaseID); !ok || l.Source != *m.Source {
				return invalid("coverage %s: lease %s is not a stored lease of its source", c.ID, m.LeaseID)
			}
		}
	case m.NestedCoverageID != "":
		if !t.r.sem.coverages.has(m.NestedCoverageID) {
			return invalid("coverage %s: nested coverage %s is not stored", c.ID, m.NestedCoverageID)
		}
	case m.ExchangeID != "":
		x, ok := t.r.sem.exchanges.peek(m.ExchangeID)
		if !ok {
			return invalid("coverage %s: exchange %s is not stored", c.ID, m.ExchangeID)
		}
		if c.ConversationID != "" && x.ConversationID != c.ConversationID {
			return invalid("coverage %s: exchange %s belongs to another conversation", c.ID, m.ExchangeID)
		}
	}
	return nil
}

// checkContent requires ref to name a stored item with exactly that content.
func (t *semTx) checkContent(ref domain.ItemContentRef) error {
	it, ok := t.r.items.peek(ref.ItemID)
	if !ok || it.ContentHash != ref.ContentHash {
		return invalid("content %s is not a stored item with that content", ref.ItemID)
	}
	return nil
}

func (r semRead) Coverage(id string) (domain.CoverageRecord, error) {
	if err := r.r.check(); err != nil {
		return domain.CoverageRecord{}, err
	}
	c, ok := r.r.sem.coverages.get(id)
	if !ok {
		return c, notFound("coverage", id)
	}
	return c, nil
}

func (r semRead) CoverageMembers(coverageID string, p store.Page) (store.ResultPage[domain.CoverageMember], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.CoverageMember]{}, err
	}
	if p.Limit <= 0 {
		return store.ResultPage[domain.CoverageMember]{}, invalid("page limit must be positive")
	}
	ms, _ := r.r.sem.covMembers.peek(coverageID)
	var out store.ResultPage[domain.CoverageMember]
	after := cursorRef(p.After)
	for _, m := range ms {
		ref := seqRef{m.Seq, m.ID}
		if !after.less(ref) {
			continue
		}
		if len(out.Records) == p.Limit {
			out.More = true
			break
		}
		out.Records = append(out.Records, m.Clone())
		out.Next = store.Cursor{Seq: ref.seq, ID: ref.id}
	}
	return out, nil
}

func (r semRead) CoveragesBySource(itemID string, purpose domain.CoveragePurpose, p store.Page) (store.ResultPage[domain.CoverageRecord], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.CoverageRecord]{}, err
	}
	return page(p, r.r.sem.covBySource.after(covSourceKey{itemID, purpose}, cursorRef(p.After)), loadAll(&r.r.sem.coverages, ident))
}

// --- Logical exchanges ---

func (t *semTx) InsertLogicalExchange(x domain.LogicalExchange) error {
	if err := t.t.companion("exchange", x.SemanticMeta, x.Validate); err != nil {
		return err
	}
	if t.r.sem.exchanges.has(x.ID) {
		return immutable("exchange", x.ID)
	}
	if x.State != domain.ExchangeOpen && x.State != domain.ExchangeExecuting {
		return transition("exchange %s: a new exchange is OPEN or EXECUTING", x.ID)
	}
	if x.Revision != 1 {
		return invalid("exchange %s: new exchanges start at revision 1", x.ID)
	}
	last, _ := t.r.sem.exchLast.peek(x.ConversationID)
	if x.Ordinal != last+1 {
		return invalid("exchange %s: ordinal %d, want the next ordinal %d", x.ID, x.Ordinal, last+1)
	}
	ref := seqRef{x.Seq, x.ID}
	t.r.sem.exchanges.put(x.ID, x)
	t.r.sem.exchOrdinal.put(convOrdinal{x.ConversationID, x.Ordinal}, x.ID)
	t.r.sem.exchLast.put(x.ConversationID, x.Ordinal)
	t.r.sem.exchByConv.add(x.ConversationID, ref)
	t.r.sem.openByTask.add(x.Principal.TaskID, ref)
	t.t.sequencedWrite(x.Seq)
	return nil
}

func (t *semTx) PutLogicalExchange(x domain.LogicalExchange, expectedRevision uint64) (domain.LogicalExchange, error) {
	if err := t.t.own(x.SessionID); err != nil {
		return domain.LogicalExchange{}, err
	}
	cur, ok := t.r.sem.exchanges.peek(x.ID)
	if !ok {
		return domain.LogicalExchange{}, notFound("exchange", x.ID)
	}
	if cur.Revision != expectedRevision {
		return domain.LogicalExchange{}, fmt.Errorf("exchange %s: revision %d, expected %d: %w", x.ID, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	x.Revision = expectedRevision + 1
	frozen := x
	frozen.State, frozen.AcknowledgmentID, frozen.Revision = cur.State, cur.AcknowledgmentID, cur.Revision
	if frozen != cur {
		return domain.LogicalExchange{}, immutable("exchange", x.ID)
	}
	if err := x.Validate(); err != nil {
		return domain.LogicalExchange{}, err
	}
	if !exchangeTransition(cur.State, x.State) {
		return domain.LogicalExchange{}, transition("exchange %s: %s -> %s", x.ID, cur.State, x.State)
	}
	if x.State == domain.ExchangeClosed || x.State == domain.ExchangeCancelled {
		a, ok := t.r.sem.acks.peek(x.AcknowledgmentID)
		if !ok || a.ExchangeID != x.ID {
			return domain.LogicalExchange{}, invalid("exchange %s: acknowledgment %s is not stored for it", x.ID, x.AcknowledgmentID)
		}
		if a.Cancelled != (x.State == domain.ExchangeCancelled) {
			return domain.LogicalExchange{}, invalid("exchange %s: acknowledgment disagrees with %s", x.ID, x.State)
		}
		t.r.sem.openByTask.remove(x.Principal.TaskID, seqRef{x.Seq, x.ID})
	}
	t.r.sem.exchanges.put(x.ID, x)
	t.t.markSemantic()
	return x, nil
}

// exchangeTransition is the exchange state machine: OPEN may start
// executing, and an open or executing exchange closes or is cancelled once.
func exchangeTransition(from, to domain.ExchangeState) bool {
	switch from {
	case domain.ExchangeOpen:
		return to == domain.ExchangeExecuting || to == domain.ExchangeClosed || to == domain.ExchangeCancelled
	case domain.ExchangeExecuting:
		return to == domain.ExchangeClosed || to == domain.ExchangeCancelled
	}
	return false
}

func (t *semTx) InsertExchangeMember(m domain.ExchangeMember) error {
	if err := t.t.companion("exchange member", m.SemanticMeta, m.Validate); err != nil {
		return err
	}
	x, ok := t.r.sem.exchanges.peek(m.ExchangeID)
	if !ok {
		return invalid("exchange member %s: exchange %s is not stored", m.ID, m.ExchangeID)
	}
	if x.State != domain.ExchangeOpen && x.State != domain.ExchangeExecuting {
		return transition("exchange member %s: exchange %s is %s", m.ID, x.ID, x.State)
	}
	if t.r.sem.members.has(m.ID) {
		return immutable("exchange member", m.ID)
	}
	if t.r.sem.memberPos.has(exchangePos{m.ExchangeID, m.Position}) {
		return invalid("exchange member %s: position %d is taken", m.ID, m.Position)
	}
	if err := t.checkContent(m.Source); err != nil {
		return fmt.Errorf("exchange member %s: %w", m.ID, err)
	}
	if m.AdmissionID != "" {
		a, ok := t.r.sem.admissions.peek(m.AdmissionID)
		if !ok || a.ExchangeID != m.ExchangeID {
			return invalid("exchange member %s: admission %s is not stored for its exchange", m.ID, m.AdmissionID)
		}
	}
	ref := seqRef{m.Seq, m.ID}
	t.r.sem.members.put(m.ID, m)
	t.r.sem.memberPos.put(exchangePos{m.ExchangeID, m.Position}, m.ID)
	t.r.sem.membersByEx.add(m.ExchangeID, ref)
	t.r.sem.membersByIt.add(m.Source.ItemID, ref)
	t.t.sequencedWrite(m.Seq)
	return nil
}

func (t *semTx) InsertExchangeAcknowledgment(a domain.ExchangeAcknowledgment) error {
	if err := t.t.companion("acknowledgment", a.SemanticMeta, a.Validate); err != nil {
		return err
	}
	if !t.r.sem.exchanges.has(a.ExchangeID) {
		return invalid("acknowledgment %s: exchange %s is not stored", a.ID, a.ExchangeID)
	}
	if t.r.sem.acks.has(a.ID) {
		return immutable("acknowledgment", a.ID)
	}
	if t.r.sem.ackByEx.has(a.ExchangeID) {
		return immutable("acknowledgment of exchange", a.ExchangeID)
	}
	if !a.Cancelled {
		m, ok := t.r.sem.admissions.peek(a.ManifestID)
		if !ok || m.ExchangeID != a.ExchangeID || m.CallID != a.ConsumingCallID {
			return invalid("acknowledgment %s: manifest %s is not this exchange's consuming inference", a.ID, a.ManifestID)
		}
	}
	t.r.sem.acks.put(a.ID, a)
	t.r.sem.ackByEx.put(a.ExchangeID, a.ID)
	t.t.sequencedWrite(a.Seq)
	return nil
}

func (t *semTx) InsertAdmissionManifest(m domain.AdmissionManifest) error {
	if err := t.t.companion("admission", m.SemanticMeta, m.Validate); err != nil {
		return err
	}
	if t.r.sem.admissions.has(m.ID) {
		return immutable("admission", m.ID)
	}
	x, ok := t.r.sem.exchanges.peek(m.ExchangeID)
	if !ok || x.ConversationID != m.ConversationID {
		return invalid("admission %s: exchange %s is not stored in its conversation", m.ID, m.ExchangeID)
	}
	if !t.r.sem.coverages.has(m.CoverageID) {
		return invalid("admission %s: coverage %s is not stored", m.ID, m.CoverageID)
	}
	ms, ok := t.r.sem.membership.peek(m.ConversationID)
	if !ok || ms.Revision < m.MembershipRevision {
		return invalid("admission %s: membership revision %d is not recorded", m.ID, m.MembershipRevision)
	}
	t.r.sem.admissions.put(m.ID, m)
	t.r.sem.admByEx.add(m.ExchangeID, seqRef{m.Seq, m.ID})
	t.t.sequencedWrite(m.Seq)
	return nil
}

// PutConversationMembership creates (expected 0) or advances a
// conversation's membership state. Its Seq is the sequence of this write and
// must be allocated in the transaction. LastOrdinal and ClosedFrontier never
// decrease, LastOrdinal names a recorded exchange, and every ordinal the
// frontier newly covers must be CLOSED by a consuming acknowledgment:
// cancellation is not coverage.
func (t *semTx) PutConversationMembership(s domain.ConversationMembershipState, expectedRevision uint64) (domain.ConversationMembershipState, error) {
	if err := t.t.companion("membership", s.SemanticMeta, s.Validate); err != nil {
		return domain.ConversationMembershipState{}, err
	}
	cur, ok := t.r.sem.membership.peek(s.ConversationID)
	if cur.Revision != expectedRevision {
		return domain.ConversationMembershipState{}, fmt.Errorf("membership %s: revision %d, expected %d: %w", s.ConversationID, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	if ok && cur.ID != s.ID {
		return domain.ConversationMembershipState{}, immutable("membership", s.ConversationID)
	}
	if last, _ := t.r.sem.exchLast.peek(s.ConversationID); s.LastOrdinal > last {
		return domain.ConversationMembershipState{}, invalid("membership %s: ordinal %d is not a recorded exchange", s.ConversationID, s.LastOrdinal)
	}
	if s.LastOrdinal < cur.LastOrdinal || s.ClosedFrontier < cur.ClosedFrontier {
		return domain.ConversationMembershipState{}, transition("membership %s: ordinals cannot move backward", s.ConversationID)
	}
	for o := cur.ClosedFrontier + 1; o <= s.ClosedFrontier; o++ {
		id, _ := t.r.sem.exchOrdinal.peek(convOrdinal{s.ConversationID, o})
		x, _ := t.r.sem.exchanges.peek(id)
		if x.State != domain.ExchangeClosed {
			return domain.ConversationMembershipState{}, transition("membership %s: ordinal %d is not closed by a consuming acknowledgment", s.ConversationID, o)
		}
	}
	s.Revision = expectedRevision + 1
	t.r.sem.membership.put(s.ConversationID, s)
	t.t.sequencedWrite(s.Seq)
	return s, nil
}

func (r semRead) LogicalExchange(id string) (domain.LogicalExchange, error) {
	if err := r.r.check(); err != nil {
		return domain.LogicalExchange{}, err
	}
	x, ok := r.r.sem.exchanges.get(id)
	if !ok {
		return x, notFound("exchange", id)
	}
	return x, nil
}

func (r semRead) ExchangeAcknowledgment(id string) (domain.ExchangeAcknowledgment, error) {
	if err := r.r.check(); err != nil {
		return domain.ExchangeAcknowledgment{}, err
	}
	a, ok := r.r.sem.acks.get(id)
	if !ok {
		return a, notFound("acknowledgment", id)
	}
	return a, nil
}

func (r semRead) ConversationMembership(conversationID string) (domain.ConversationMembershipState, error) {
	if err := r.r.check(); err != nil {
		return domain.ConversationMembershipState{}, err
	}
	s, ok := r.r.sem.membership.get(conversationID)
	if !ok {
		return s, notFound("membership", conversationID)
	}
	return s, nil
}

func (r semRead) ExchangesByConversation(conversationID string, p store.Page) (store.ResultPage[domain.LogicalExchange], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.LogicalExchange]{}, err
	}
	return page(p, r.r.sem.exchByConv.after(conversationID, cursorRef(p.After)), loadAll(&r.r.sem.exchanges, ident))
}

func (r semRead) ExchangeMembers(exchangeID string, p store.Page) (store.ResultPage[domain.ExchangeMember], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ExchangeMember]{}, err
	}
	return page(p, r.r.sem.membersByEx.after(exchangeID, cursorRef(p.After)), loadAll(&r.r.sem.members, ident))
}

func (r semRead) MembershipsByItem(itemID string, p store.Page) (store.ResultPage[domain.ExchangeMember], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.ExchangeMember]{}, err
	}
	return page(p, r.r.sem.membersByIt.after(itemID, cursorRef(p.After)), loadAll(&r.r.sem.members, ident))
}

func (r semRead) AdmissionManifest(id string) (domain.AdmissionManifest, error) {
	if err := r.r.check(); err != nil {
		return domain.AdmissionManifest{}, err
	}
	m, ok := r.r.sem.admissions.get(id)
	if !ok {
		return m, notFound("admission", id)
	}
	return m, nil
}

func (r semRead) AdmissionsByExchange(exchangeID string, p store.Page) (store.ResultPage[domain.AdmissionManifest], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.AdmissionManifest]{}, err
	}
	return page(p, r.r.sem.admByEx.after(exchangeID, cursorRef(p.After)), loadAll(&r.r.sem.admissions, ident))
}

func (r semRead) OpenExchangesByTask(taskID string, p store.Page) (store.ResultPage[domain.LogicalExchange], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.LogicalExchange]{}, err
	}
	return page(p, r.r.sem.openByTask.after(taskID, cursorRef(p.After)), loadAll(&r.r.sem.exchanges, ident))
}

// ReservingCallsByTask pages the PREPARED, SENT, and UNKNOWN calls whose
// frozen principal names taskID, in (PreparedSeq, CallID) order.
func (r semRead) ReservingCallsByTask(taskID string, p store.Page) (store.ResultPage[domain.CallRecord], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.CallRecord]{}, err
	}
	return page(p, r.r.sem.reserving.after(taskID, cursorRef(p.After)), loadAll(&r.r.calls, ident))
}

// noteReservation keeps the reserving-calls index in step with a call write.
func (t *tx) noteReservation(before, after domain.CallRecord) {
	ref := seqRef{after.PreparedSeq, after.CallID}
	switch {
	case before.State.Reserving() && !after.State.Reserving():
		t.sem.reserving.remove(before.Principal.TaskID, ref)
	case !before.State.Reserving() && after.State.Reserving():
		t.sem.reserving.add(after.Principal.TaskID, ref)
	}
}

// --- Checkpoints and owners ---

func (t *semTx) InsertCheckpoint(c domain.Checkpoint) error {
	if err := t.t.companion("checkpoint", c.SemanticMeta, c.Validate); err != nil {
		return err
	}
	if t.r.sem.checkpoints.has(c.ID) {
		return immutable("checkpoint", c.ID)
	}
	if t.r.sem.ckByItem.has(c.ItemID) {
		return immutable("checkpoint item", c.ItemID)
	}
	if it, ok := t.r.items.peek(c.ItemID); !ok || it.Role != domain.RoleCheckpoint {
		return invalid("checkpoint %s: item %s is not a stored CHECKPOINT item", c.ID, c.ItemID)
	}
	issuing, ok := t.r.sem.exchanges.peek(c.IssuingExchangeID)
	if !ok || issuing.ConversationID != c.ConversationID || issuing.Ordinal <= c.CoveredFrontier {
		return invalid("checkpoint %s: issuing exchange must be a later round of its conversation", c.ID)
	}
	gen, ok := t.r.sem.admissions.peek(c.GenerationManifestID)
	if !ok || gen.ConversationID != c.ConversationID || gen.Purpose != domain.AdmissionGenerationInput {
		return invalid("checkpoint %s: generation manifest is not stored for its conversation", c.ID)
	}
	src, ok := t.r.sem.coverages.peek(c.SourceCoverageID)
	if !ok || src.Purpose != domain.CoverageGenerationInput && src.Purpose != domain.CoverageProvenance {
		return invalid("checkpoint %s: source coverage must be stored generation-input or provenance coverage", c.ID)
	}
	ex, ok := t.r.sem.coverages.peek(c.CoveredExchangesID)
	if !ok || ex.Purpose != domain.CoverageExchangeReplacement || ex.ConversationID != c.ConversationID ||
		ex.ClosedFrontier != c.CoveredFrontier || ex.MembershipRevision != c.MembershipRevision {
		return invalid("checkpoint %s: exchange coverage must be this conversation's closed prefix", c.ID)
	}
	if ms, ok := t.r.sem.membership.peek(c.ConversationID); !ok || ms.Revision < c.MembershipRevision {
		return invalid("checkpoint %s: membership revision %d is not recorded", c.ID, c.MembershipRevision)
	}
	if c.PriorCheckpointID != "" {
		prior, ok := t.r.sem.checkpoints.peek(c.PriorCheckpointID)
		if !ok || prior.ConversationID != c.ConversationID {
			return invalid("checkpoint %s: prior checkpoint is not stored in its conversation", c.ID)
		}
	}
	t.r.sem.checkpoints.put(c.ID, c)
	t.r.sem.ckByItem.put(c.ItemID, c.ID)
	t.r.sem.ckByConv.add(c.ConversationID, seqRef{c.Seq, c.ID})
	t.t.sequencedWrite(c.Seq)
	return nil
}

func (r semRead) Checkpoint(id string) (domain.Checkpoint, error) {
	if err := r.r.check(); err != nil {
		return domain.Checkpoint{}, err
	}
	c, ok := r.r.sem.checkpoints.get(id)
	if !ok {
		return c, notFound("checkpoint", id)
	}
	return c, nil
}

// CheckpointsByConversation pages newest first; a checkpoint is visible when
// its item's access boundary permits viewer, checked before the limit.
func (r semRead) CheckpointsByConversation(viewer domain.Principal, conversationID string, p store.Page) (store.ResultPage[domain.Checkpoint], error) {
	if err := r.r.check(); err != nil {
		return store.ResultPage[domain.Checkpoint]{}, err
	}
	if err := viewer.Validate(); err != nil {
		return store.ResultPage[domain.Checkpoint]{}, err
	}
	return page(p, r.r.sem.ckByConv.before(conversationID, cursorRef(p.After)), func(id string) (domain.Checkpoint, bool) {
		c, ok := r.r.sem.checkpoints.get(id)
		if !ok {
			return c, false
		}
		it, ok := r.r.items.peek(c.ItemID)
		return c, ok && it.Access.Permits(viewer)
	})
}

func (t *semTx) InsertOwnerRegistration(o domain.OwnerRegistration) error {
	if err := t.t.companion("owner registration", o.SemanticMeta, o.Validate); err != nil {
		return err
	}
	if t.r.sem.owners.has(ownerKey{o.Kind, o.OwnerID}) {
		return immutable("owner registration", o.OwnerID)
	}
	t.r.sem.owners.put(ownerKey{o.Kind, o.OwnerID}, o)
	t.t.sequencedWrite(o.Seq)
	return nil
}

func (r semRead) OwnerRegistration(kind domain.OwnerKind, ownerID string) (domain.OwnerRegistration, error) {
	if err := r.r.check(); err != nil {
		return domain.OwnerRegistration{}, err
	}
	o, ok := r.r.sem.owners.get(ownerKey{kind, ownerID})
	if !ok {
		return o, notFound("owner registration", ownerID)
	}
	return o, nil
}
