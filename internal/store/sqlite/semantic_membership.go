package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// Coverage (P3-6), logical membership (P3-7), checkpoints (P3-27), and owner
// registrations (P3-32), with the same structural and reference rules as
// the memory store (shared storetest cases).

// --- Coverage ---

func (s semTx) InsertCoverage(c domain.CoverageRecord, members []domain.CoverageMember) error {
	t := s.t
	if err := t.companion(c.SemanticMeta, c.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("coverage", c.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "coverage", c.ID))
	}
	for _, m := range members {
		if err := t.checkSession(m.SessionID); err != nil {
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
		if err := s.checkMember(c, m); err != nil {
			return err
		}
	}
	return t.atomic(func() error {
		if err := t.put("coverage", c.ID, 0, c, false); err != nil {
			return err
		}
		seen := map[string]bool{}
		for i, m := range members {
			if err := t.put("coverage_member", c.ID, i, coverageMemberRow{SessionID: t.session, CoverageID: c.ID, Ordinal: i, Member: m}, false); err != nil {
				return err
			}
			if m.Source != nil && !seen[m.Source.ItemID] {
				seen[m.Source.ItemID] = true
				if _, err := t.conn.ExecContext(t.ctx, "INSERT INTO lookup_coverage_source(session_id,item_id,purpose,seq,coverage_id) VALUES(?,?,?,?,?)",
					t.session, m.Source.ItemID, string(c.Purpose), c.Seq, c.ID); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func immutableIf(ok bool, what, id string) error {
	if ok {
		return immutable(what, id)
	}
	return nil
}

func (s semTx) checkMember(c domain.CoverageRecord, m domain.CoverageMember) error {
	switch {
	case m.Source != nil:
		if err := s.checkContent(*m.Source); err != nil {
			return fmt.Errorf("coverage %s: %w", c.ID, err)
		}
		if m.LeaseID != "" {
			// A lease member names its lease's exact source content.
			var l domain.RetrievalLease
			if err := s.t.get("retrieval_lease", m.LeaseID, 0, &l); err != nil || l.Source != *m.Source {
				return notStored(errors.Join(err, domain.ErrNotFound), "coverage %s: lease %s is not a stored lease of its source", c.ID, m.LeaseID)
			}
		}
	case m.NestedCoverageID != "":
		ok, err := s.t.exists("coverage", m.NestedCoverageID)
		if err != nil {
			return err
		}
		if !ok {
			return invalid("coverage %s: nested coverage %s is not stored", c.ID, m.NestedCoverageID)
		}
	case m.ExchangeID != "":
		var x domain.LogicalExchange
		if err := s.t.get("exchange", m.ExchangeID, 0, &x); err != nil {
			return notStored(err, "coverage %s: exchange %s is not stored", c.ID, m.ExchangeID)
		}
		if c.ConversationID != "" && x.ConversationID != c.ConversationID {
			return invalid("coverage %s: exchange %s belongs to another conversation", c.ID, m.ExchangeID)
		}
	}
	return nil
}

// notStored turns a missing reference into ErrInvalidRecord and passes any
// other read error through.
func notStored(err error, format string, args ...any) error {
	if errors.Is(err, domain.ErrNotFound) {
		return invalid(format, args...)
	}
	return err
}

// checkContent requires ref to name a stored item with exactly that content.
func (s semRead) checkContent(ref domain.ItemContentRef) error {
	var hash string
	err := s.t.conn.QueryRowContext(s.t.ctx, "SELECT f_content_hash FROM rec_item WHERE session_id=? AND id=? AND subkey=0", s.t.session, ref.ItemID).Scan(&hash)
	if err != nil && !isNoRows(err) {
		return err
	}
	if err != nil || hash != ref.ContentHash {
		return invalid("content %s is not a stored item with that content", ref.ItemID)
	}
	return nil
}

func (s semRead) Coverage(id string) (domain.CoverageRecord, error) {
	var c domain.CoverageRecord
	return c, s.t.get("coverage", id, 0, &c)
}

// CoverageMembers pages a record's members in canonical key order, which is
// their (Seq, ID) order: members share their record's Seq and their ID is
// their key.
func (s semRead) CoverageMembers(coverageID string, p store.Page) (store.ResultPage[domain.CoverageMember], error) {
	var out store.ResultPage[domain.CoverageMember]
	if p.Limit <= 0 {
		return out, invalid("page limit must be positive")
	}
	sc, err := schemaFor("coverage_member")
	if err != nil {
		return out, err
	}
	rows, err := s.t.query(sc.selectSQL+" WHERE session_id=? AND id=? AND (f_member_semantic_meta_seq>? OR (f_member_semantic_meta_seq=? AND f_member_semantic_meta_id>?)) ORDER BY subkey LIMIT ?",
		s.t.session, coverageID, p.After.Seq, p.After.Seq, p.After.ID, p.Limit+1)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		v, err := sc.scan(rows)
		if err != nil {
			return out, fmt.Errorf("coverage member: %w", err)
		}
		m := v.Interface().(coverageMemberRow).Member
		if len(out.Records) == p.Limit {
			out.More = true
			break
		}
		out.Records = append(out.Records, m)
		out.Next = store.Cursor{Seq: m.Seq, ID: m.ID}
	}
	return out, rows.Err()
}

func (s semRead) CoveragesBySource(itemID string, purpose domain.CoveragePurpose, p store.Page) (store.ResultPage[domain.CoverageRecord], error) {
	var out store.ResultPage[domain.CoverageRecord]
	if p.Limit <= 0 {
		return out, invalid("page limit must be positive")
	}
	rows, err := s.t.query("SELECT seq, coverage_id FROM lookup_coverage_source WHERE session_id=? AND item_id=? AND purpose=? AND (seq>? OR (seq=? AND coverage_id>?)) ORDER BY seq, coverage_id LIMIT ?",
		s.t.session, itemID, string(purpose), p.After.Seq, p.After.Seq, p.After.ID, p.Limit+1)
	if err != nil {
		return out, err
	}
	var ids []string
	for rows.Next() {
		var seq uint64
		var id string
		if err := rows.Scan(&seq, &id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return out, err
	}
	for _, id := range ids {
		if len(out.Records) == p.Limit {
			out.More = true
			break
		}
		c, err := s.Coverage(id)
		if err != nil {
			return out, fmt.Errorf("%w: coverage index names missing coverage %s", domain.ErrIntegrity, id)
		}
		out.Records = append(out.Records, c)
		out.Next = store.Cursor{Seq: c.Seq, ID: c.ID}
	}
	return out, nil
}

// --- Logical exchanges ---

func (s semTx) InsertLogicalExchange(x domain.LogicalExchange) error {
	t := s.t
	if err := t.companion(x.SemanticMeta, x.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("exchange", x.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "exchange", x.ID))
	}
	if x.State != domain.ExchangeOpen && x.State != domain.ExchangeExecuting {
		return transition("exchange %s: a new exchange is OPEN or EXECUTING", x.ID)
	}
	if x.Revision != 1 {
		return invalid("exchange %s: new exchanges start at revision 1", x.ID)
	}
	last, err := s.lastOrdinal(x.ConversationID)
	if err != nil {
		return err
	}
	if x.Ordinal != last+1 {
		return invalid("exchange %s: ordinal %d, want the next ordinal %d", x.ID, x.Ordinal, last+1)
	}
	return t.put("exchange", x.ID, 0, x, false)
}

func (s semRead) lastOrdinal(conversationID string) (uint64, error) {
	var last uint64
	err := s.t.conn.QueryRowContext(s.t.ctx, "SELECT COALESCE(MAX(f_ordinal),0) FROM rec_exchange WHERE session_id=? AND f_conversation_id=?", s.t.session, conversationID).Scan(&last)
	return last, err
}

func (s semTx) PutLogicalExchange(x domain.LogicalExchange, expectedRevision uint64) (domain.LogicalExchange, error) {
	t := s.t
	if err := t.checkSession(x.SessionID); err != nil {
		return domain.LogicalExchange{}, err
	}
	var cur domain.LogicalExchange
	if err := t.get("exchange", x.ID, 0, &cur); err != nil {
		return domain.LogicalExchange{}, err
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
		var a domain.ExchangeAcknowledgment
		if err := t.get("exchange_ack", x.AcknowledgmentID, 0, &a); err != nil || a.ExchangeID != x.ID {
			return domain.LogicalExchange{}, notStored(errors.Join(err, domain.ErrNotFound), "exchange %s: acknowledgment %s is not stored for it", x.ID, x.AcknowledgmentID)
		}
		if a.Cancelled != (x.State == domain.ExchangeCancelled) {
			return domain.LogicalExchange{}, invalid("exchange %s: acknowledgment disagrees with %s", x.ID, x.State)
		}
	}
	return x, t.put("exchange", x.ID, 0, x, true)
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

func (s semTx) InsertExchangeMember(m domain.ExchangeMember) error {
	t := s.t
	if err := t.companion(m.SemanticMeta, m.Validate); err != nil {
		return err
	}
	var x domain.LogicalExchange
	if err := t.get("exchange", m.ExchangeID, 0, &x); err != nil {
		return notStored(err, "exchange member %s: exchange %s is not stored", m.ID, m.ExchangeID)
	}
	if x.State != domain.ExchangeOpen && x.State != domain.ExchangeExecuting {
		return transition("exchange member %s: exchange %s is %s", m.ID, x.ID, x.State)
	}
	if ok, err := t.exists("exchange_member", m.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "exchange member", m.ID))
	}
	var taken domain.ExchangeMember
	if err := t.getWhere("exchange_member", "f_exchange_id=? AND f_position=?", &taken, m.ExchangeID, m.Position); err == nil {
		return invalid("exchange member %s: position %d is taken", m.ID, m.Position)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if err := s.checkContent(m.Source); err != nil {
		return fmt.Errorf("exchange member %s: %w", m.ID, err)
	}
	if m.AdmissionID != "" {
		var a domain.AdmissionManifest
		if err := t.get("admission", m.AdmissionID, 0, &a); err != nil || a.ExchangeID != m.ExchangeID {
			return notStored(errors.Join(err, domain.ErrNotFound), "exchange member %s: admission %s is not stored for its exchange", m.ID, m.AdmissionID)
		}
	}
	return t.atomic(func() error {
		if err := t.put("exchange_member", m.ID, 0, m, false); err != nil {
			return err
		}
		_, err := t.conn.ExecContext(t.ctx, "INSERT OR IGNORE INTO lookup_item_exchange(session_id,item_id,conversation_id,ordinal,exchange_id) VALUES(?,?,?,?,?)",
			t.session, m.Source.ItemID, x.ConversationID, x.Ordinal, x.ID)
		return err
	})
}

func (s semTx) InsertExchangeAcknowledgment(a domain.ExchangeAcknowledgment) error {
	t := s.t
	if err := t.companion(a.SemanticMeta, a.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("exchange", a.ExchangeID); err != nil || !ok {
		return errors.Join(err, invalidIf(!ok, "acknowledgment %s: exchange %s is not stored", a.ID, a.ExchangeID))
	}
	if ok, err := t.exists("exchange_ack", a.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "acknowledgment", a.ID))
	}
	var prior domain.ExchangeAcknowledgment
	if err := t.getWhere("exchange_ack", "f_exchange_id=?", &prior, a.ExchangeID); err == nil {
		return immutable("acknowledgment of exchange", a.ExchangeID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	if !a.Cancelled {
		var m domain.AdmissionManifest
		if err := t.get("admission", a.ManifestID, 0, &m); err != nil || m.ExchangeID != a.ExchangeID || m.CallID != a.ConsumingCallID {
			return notStored(errors.Join(err, domain.ErrNotFound), "acknowledgment %s: manifest %s is not this exchange's consuming inference", a.ID, a.ManifestID)
		}
	}
	return t.put("exchange_ack", a.ID, 0, a, false)
}

func invalidIf(bad bool, format string, args ...any) error {
	if bad {
		return invalid(format, args...)
	}
	return nil
}

func (s semTx) InsertAdmissionManifest(m domain.AdmissionManifest) error {
	t := s.t
	if err := t.companion(m.SemanticMeta, m.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("admission", m.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "admission", m.ID))
	}
	var x domain.LogicalExchange
	if err := t.get("exchange", m.ExchangeID, 0, &x); err != nil || x.ConversationID != m.ConversationID {
		return notStored(errors.Join(err, domain.ErrNotFound), "admission %s: exchange %s is not stored in its conversation", m.ID, m.ExchangeID)
	}
	if ok, err := t.exists("coverage", m.CoverageID); err != nil || !ok {
		return errors.Join(err, invalidIf(!ok, "admission %s: coverage %s is not stored", m.ID, m.CoverageID))
	}
	var ms domain.ConversationMembershipState
	if err := t.get("membership", m.ConversationID, 0, &ms); err != nil || ms.Revision < m.MembershipRevision {
		return notStored(errors.Join(err, domain.ErrNotFound), "admission %s: membership revision %d is not recorded", m.ID, m.MembershipRevision)
	}
	return t.put("admission", m.ID, 0, m, false)
}

// PutConversationMembership creates (expected 0) or advances a
// conversation's membership state; see the memory store for the rules.
func (s semTx) PutConversationMembership(st domain.ConversationMembershipState, expectedRevision uint64) (domain.ConversationMembershipState, error) {
	t := s.t
	if err := t.companion(st.SemanticMeta, st.Validate); err != nil {
		return domain.ConversationMembershipState{}, err
	}
	var cur domain.ConversationMembershipState
	err := t.get("membership", st.ConversationID, 0, &cur)
	found := err == nil
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return domain.ConversationMembershipState{}, err
	}
	if cur.Revision != expectedRevision {
		return domain.ConversationMembershipState{}, fmt.Errorf("membership %s: revision %d, expected %d: %w", st.ConversationID, cur.Revision, expectedRevision, domain.ErrVersionConflict)
	}
	if found && cur.ID != st.ID {
		return domain.ConversationMembershipState{}, immutable("membership", st.ConversationID)
	}
	last, err := s.lastOrdinal(st.ConversationID)
	if err != nil {
		return domain.ConversationMembershipState{}, err
	}
	if st.LastOrdinal > last {
		return domain.ConversationMembershipState{}, invalid("membership %s: ordinal %d is not a recorded exchange", st.ConversationID, st.LastOrdinal)
	}
	if st.LastOrdinal < cur.LastOrdinal || st.ClosedFrontier < cur.ClosedFrontier {
		return domain.ConversationMembershipState{}, transition("membership %s: ordinals cannot move backward", st.ConversationID)
	}
	if st.ClosedFrontier > cur.ClosedFrontier {
		var closed uint64
		if err := t.conn.QueryRowContext(t.ctx, "SELECT COUNT(*) FROM rec_exchange WHERE session_id=? AND f_conversation_id=? AND f_ordinal>? AND f_ordinal<=? AND f_state=?",
			t.session, st.ConversationID, cur.ClosedFrontier, st.ClosedFrontier, string(domain.ExchangeClosed)).Scan(&closed); err != nil {
			return domain.ConversationMembershipState{}, err
		}
		if closed != st.ClosedFrontier-cur.ClosedFrontier {
			return domain.ConversationMembershipState{}, transition("membership %s: the frontier covers an exchange not closed by a consuming acknowledgment", st.ConversationID)
		}
	}
	st.Revision = expectedRevision + 1
	if err := t.put("membership", st.ConversationID, 0, st, found); err != nil {
		return domain.ConversationMembershipState{}, err
	}
	t.semanticSeqRecord = true // the state's Seq is this write's sequence
	return st, nil
}

func (s semRead) LogicalExchange(id string) (domain.LogicalExchange, error) {
	var x domain.LogicalExchange
	return x, s.t.get("exchange", id, 0, &x)
}

func (s semRead) ExchangeAcknowledgment(id string) (domain.ExchangeAcknowledgment, error) {
	var a domain.ExchangeAcknowledgment
	return a, s.t.get("exchange_ack", id, 0, &a)
}

func (s semRead) ConversationMembership(conversationID string) (domain.ConversationMembershipState, error) {
	var m domain.ConversationMembershipState
	return m, s.t.get("membership", conversationID, 0, &m)
}

func (s semRead) ExchangesByConversation(conversationID string, p store.Page) (store.ResultPage[domain.LogicalExchange], error) {
	return pageQuery[domain.LogicalExchange](s.t, "exchange", "f_conversation_id=?", []any{conversationID}, "f_seq", p, false, nil)
}

func (s semRead) ExchangeMembers(exchangeID string, p store.Page) (store.ResultPage[domain.ExchangeMember], error) {
	return pageQuery[domain.ExchangeMember](s.t, "exchange_member", "f_exchange_id=?", []any{exchangeID}, "f_seq", p, false, nil)
}

func (s semRead) MembershipsByItem(itemID string, p store.Page) (store.ResultPage[domain.ExchangeMember], error) {
	return pageQuery[domain.ExchangeMember](s.t, "exchange_member", "f_source_item_id=?", []any{itemID}, "f_seq", p, false, nil)
}

func (s semRead) AdmissionManifest(id string) (domain.AdmissionManifest, error) {
	var m domain.AdmissionManifest
	return m, s.t.get("admission", id, 0, &m)
}

func (s semRead) AdmissionsByExchange(exchangeID string, p store.Page) (store.ResultPage[domain.AdmissionManifest], error) {
	return pageQuery[domain.AdmissionManifest](s.t, "admission", "f_exchange_id=?", []any{exchangeID}, "f_seq", p, false, nil)
}

func (s semRead) OpenExchangesByTask(taskID string, p store.Page) (store.ResultPage[domain.LogicalExchange], error) {
	return pageQuery[domain.LogicalExchange](s.t, "exchange", "f_principal_task_id=? AND f_state IN ('OPEN','EXECUTING')", []any{taskID}, "f_seq", p, false, nil)
}

// ReservingCallsByTask pages the PREPARED, SENT, and UNKNOWN calls whose
// frozen principal names taskID, in (PreparedSeq, CallID) order, through
// the call_reserving_task partial index.
func (s semRead) ReservingCallsByTask(taskID string, p store.Page) (store.ResultPage[domain.CallRecord], error) {
	return pageQuery[domain.CallRecord](s.t, "call", "f_principal_task_id=? AND f_state IN ('PREPARED','SENT','UNKNOWN')", []any{taskID}, "f_prepared_seq", p, false, nil)
}

// --- Checkpoints and owners ---

func (s semTx) InsertCheckpoint(c domain.Checkpoint) error {
	t := s.t
	if err := t.companion(c.SemanticMeta, c.Validate); err != nil {
		return err
	}
	if ok, err := t.exists("checkpoint", c.ID); err != nil || ok {
		return errors.Join(err, immutableIf(ok, "checkpoint", c.ID))
	}
	var prior domain.Checkpoint
	if err := t.getWhere("checkpoint", "f_item_id=?", &prior, c.ItemID); err == nil {
		return immutable("checkpoint item", c.ItemID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	var role string
	err := t.conn.QueryRowContext(t.ctx, "SELECT COALESCE(f_role,'') FROM rec_item WHERE session_id=? AND id=? AND subkey=0", t.session, c.ItemID).Scan(&role)
	if err != nil && !isNoRows(err) {
		return err
	}
	if err != nil || domain.ItemRole(role) != domain.RoleCheckpoint {
		return invalid("checkpoint %s: item %s is not a stored CHECKPOINT item", c.ID, c.ItemID)
	}
	var issuing domain.LogicalExchange
	if err := t.get("exchange", c.IssuingExchangeID, 0, &issuing); err != nil || issuing.ConversationID != c.ConversationID || issuing.Ordinal <= c.CoveredFrontier {
		return notStored(errors.Join(err, domain.ErrNotFound), "checkpoint %s: issuing exchange must be a later round of its conversation", c.ID)
	}
	var gen domain.AdmissionManifest
	if err := t.get("admission", c.GenerationManifestID, 0, &gen); err != nil || gen.ConversationID != c.ConversationID || gen.Purpose != domain.AdmissionGenerationInput {
		return notStored(errors.Join(err, domain.ErrNotFound), "checkpoint %s: generation manifest is not stored for its conversation", c.ID)
	}
	var src domain.CoverageRecord
	if err := t.get("coverage", c.SourceCoverageID, 0, &src); err != nil || src.Purpose != domain.CoverageGenerationInput && src.Purpose != domain.CoverageProvenance {
		return notStored(errors.Join(err, domain.ErrNotFound), "checkpoint %s: source coverage must be stored generation-input or provenance coverage", c.ID)
	}
	var ex domain.CoverageRecord
	if err := t.get("coverage", c.CoveredExchangesID, 0, &ex); err != nil || ex.Purpose != domain.CoverageExchangeReplacement || ex.ConversationID != c.ConversationID ||
		ex.ClosedFrontier != c.CoveredFrontier || ex.MembershipRevision != c.MembershipRevision {
		return notStored(errors.Join(err, domain.ErrNotFound), "checkpoint %s: exchange coverage must be this conversation's closed prefix", c.ID)
	}
	var ms domain.ConversationMembershipState
	if err := t.get("membership", c.ConversationID, 0, &ms); err != nil || ms.Revision < c.MembershipRevision {
		return notStored(errors.Join(err, domain.ErrNotFound), "checkpoint %s: membership revision %d is not recorded", c.ID, c.MembershipRevision)
	}
	if c.PriorCheckpointID != "" {
		var p domain.Checkpoint
		if err := t.get("checkpoint", c.PriorCheckpointID, 0, &p); err != nil || p.ConversationID != c.ConversationID {
			return notStored(errors.Join(err, domain.ErrNotFound), "checkpoint %s: prior checkpoint is not stored in its conversation", c.ID)
		}
	}
	return t.put("checkpoint", c.ID, 0, c, false)
}

func (s semRead) Checkpoint(id string) (domain.Checkpoint, error) {
	var c domain.Checkpoint
	return c, s.t.get("checkpoint", id, 0, &c)
}

// CheckpointsByConversation pages newest first; a checkpoint is visible when
// its item's access boundary permits viewer, checked before the limit.
func (s semRead) CheckpointsByConversation(viewer domain.Principal, conversationID string, p store.Page) (store.ResultPage[domain.Checkpoint], error) {
	if err := viewer.Validate(); err != nil {
		return store.ResultPage[domain.Checkpoint]{}, err
	}
	return pageQuery(s.t, "checkpoint", "f_conversation_id=?", []any{conversationID}, "f_seq", p, true, func(c domain.Checkpoint) (bool, error) {
		it, err := s.t.loadItem(c.ItemID, false)
		if err != nil {
			return false, err
		}
		return it.Access.Permits(viewer), nil
	})
}

func (s semTx) InsertOwnerRegistration(o domain.OwnerRegistration) error {
	t := s.t
	if err := t.companion(o.SemanticMeta, o.Validate); err != nil {
		return err
	}
	var prior domain.OwnerRegistration
	if err := t.getWhere("owner", "f_kind=? AND f_owner_id=?", &prior, string(o.Kind), o.OwnerID); err == nil {
		return immutable("owner registration", o.OwnerID)
	} else if !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return t.put("owner", o.ID, 0, o, false)
}

func (s semRead) OwnerRegistration(kind domain.OwnerKind, ownerID string) (domain.OwnerRegistration, error) {
	var o domain.OwnerRegistration
	return o, s.t.getWhere("owner", "f_kind=? AND f_owner_id=?", &o, string(kind), ownerID)
}

// EarliestExchangeWithItem implements store.MembershipReader: one keyed
// LIMIT 1 search of migration 0035's index.
func (s semRead) EarliestExchangeWithItem(conversationID, itemID string) (domain.LogicalExchange, error) {
	t := s.t
	var id string
	err := t.conn.QueryRowContext(t.ctx, "SELECT exchange_id FROM lookup_item_exchange WHERE session_id=? AND item_id=? AND conversation_id=? ORDER BY ordinal, exchange_id LIMIT 1",
		t.session, itemID, conversationID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.LogicalExchange{}, fmt.Errorf("exchange with item %s: %w", itemID, domain.ErrNotFound)
	}
	if err != nil {
		return domain.LogicalExchange{}, err
	}
	var x domain.LogicalExchange
	if err := t.get("exchange", id, 0, &x); err != nil {
		return x, fmt.Errorf("%w: item exchange index names missing exchange %s", domain.ErrIntegrity, id)
	}
	return x, nil
}
