package storetest

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// testSemanticFacet checks that writable and read-only transactions expose
// the Phase 3 facet over the same transaction: a companion written through
// it is visible to that transaction's reads, commits with it, and rolls back
// with it (P3-1).
func testSemanticFacet(t *testing.T, s store.Store) {
	reg := func(seq uint64, id string) domain.OwnerRegistration {
		return domain.OwnerRegistration{SemanticMeta: Meta(sessA, id, seq), Kind: domain.OwnerWorkflow, OwnerID: id, SourceID: "evt", Actor: HarnessPrincipal(sessA)}
	}
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertOwnerRegistration(reg(tx.NextSeq(), "wf-1")))
		_, err := sem.OwnerRegistration(domain.OwnerWorkflow, "wf-1")
		return err
	})
	err := s.Update(ctx, sessA, func(tx store.Tx) error {
		noErr(t, semantic(t, tx).InsertOwnerRegistration(reg(tx.NextSeq(), "wf-2")))
		return errRollback
	})
	wantErr(t, err, errRollback)
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		got, err := r.OwnerRegistration(domain.OwnerWorkflow, "wf-1")
		noErr(t, err)
		assertEqual(t, "OwnerRegistration", got, reg(1, "wf-1"))
		_, err = r.OwnerRegistration(domain.OwnerWorkflow, "wf-2")
		wantErr(t, err, domain.ErrNotFound)
		_, err = r.OwnerRegistration(domain.OwnerAgent, "wf-1")
		wantErr(t, err, domain.ErrNotFound)
		if tx.LastSeq() != 1 {
			t.Errorf("LastSeq = %d, want 1 (rolled-back companion reuses its sequence)", tx.LastSeq())
		}
		return nil
	})
	// Owner registration is immutable per (kind, owner).
	rejected(t, s, sessA, domain.ErrImmutable, func(tx store.Tx) error {
		return semantic(t, tx).InsertOwnerRegistration(reg(tx.NextSeq(), "wf-1"))
	})
	// A companion's sequence must be allocated in its transaction.
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		return semantic(t, tx).InsertOwnerRegistration(reg(1, "wf-3"))
	})
	// A companion naming another session is rejected.
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		r := reg(tx.NextSeq(), "wf-3")
		r.SessionID, r.Actor.SessionID = sessB, sessB
		return semantic(t, tx).InsertOwnerRegistration(r)
	})
	// A semantic write failing after a successful one poisons the whole
	// transaction, across the legacy and semantic APIs.
	err = s.Update(ctx, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem(sessA, "kept?", tx.NextSeq(), "x")))
		_ = semantic(t, tx).InsertOwnerRegistration(reg(1, "wf-4"))
		return nil
	})
	wantErr(t, err, domain.ErrInvalidRecord)
	view(t, s, sessA, func(tx store.ReadTx) error {
		_, err := tx.Item("kept?")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testSemanticCoverage checks normalized coverage storage (P3-6): one record
// plus sorted unique members inserted atomically, exact source references,
// stable member pages, and the reverse source index.
func testSemanticCoverage(t *testing.T, s store.Store) {
	var a, b domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		a, b = NewItem(sessA, "a", tx.NextSeq(), "a"), NewItem(sessA, "b", tx.NextSeq(), "b")
		noErr(t, tx.InsertItem(a))
		return tx.InsertItem(b)
	})
	var cov domain.CoverageRecord
	var members []domain.CoverageMember
	update(t, s, sessA, func(tx store.Tx) error {
		cov, members = NewCoverage(t, sessA, "cov", tx.NextSeq(), domain.CoverageProvenance, ContentRef(a), ContentRef(b))
		return semantic(t, tx).InsertCoverage(cov, members)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		got, err := r.Coverage("cov")
		noErr(t, err)
		assertEqual(t, "Coverage", got, cov)
		p1, err := r.CoverageMembers("cov", store.Page{Limit: 1})
		noErr(t, err)
		if len(p1.Records) != 1 || !p1.More {
			t.Fatalf("first member page = %+v, want one record and More", p1)
		}
		p2, err := r.CoverageMembers("cov", store.Page{After: p1.Next, Limit: 5})
		noErr(t, err)
		if p2.More {
			t.Errorf("second member page reports More")
		}
		assertEqual(t, "CoverageMembers", append(p1.Records, p2.Records...), members)
		for _, id := range []string{"a", "b"} {
			bySrc, err := r.CoveragesBySource(id, domain.CoverageProvenance, store.Page{Limit: 5})
			noErr(t, err)
			assertEqual(t, "CoveragesBySource("+id+")", bySrc.Records, []domain.CoverageRecord{cov})
			other, err := r.CoveragesBySource(id, domain.CoverageEvidenceSupport, store.Page{Limit: 5})
			noErr(t, err)
			if len(other.Records) != 0 {
				t.Errorf("CoveragesBySource(%s, EVIDENCE_SUPPORT) = %+v, want none: purposes are disjoint", id, other.Records)
			}
		}
		_, err = r.CoverageMembers("cov", store.Page{})
		wantErr(t, err, domain.ErrInvalidRecord)
		_, err = r.Coverage("missing")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
	cases := []struct {
		name string
		edit func(seq uint64) (domain.CoverageRecord, []domain.CoverageMember)
		want error
	}{
		{"reused ID", func(seq uint64) (domain.CoverageRecord, []domain.CoverageMember) {
			return NewCoverage(t, sessA, "cov", seq, domain.CoverageProvenance, ContentRef(a))
		}, domain.ErrImmutable},
		{"missing source item", func(seq uint64) (domain.CoverageRecord, []domain.CoverageMember) {
			return NewCoverage(t, sessA, "c2", seq, domain.CoverageProvenance, domain.ItemContentRef{ItemID: "ghost", ContentHash: a.ContentHash})
		}, domain.ErrInvalidRecord},
		{"wrong source content", func(seq uint64) (domain.CoverageRecord, []domain.CoverageMember) {
			return NewCoverage(t, sessA, "c2", seq, domain.CoverageProvenance, domain.ItemContentRef{ItemID: "a", ContentHash: b.ContentHash})
		}, domain.ErrInvalidRecord},
		{"unsorted members", func(seq uint64) (domain.CoverageRecord, []domain.CoverageMember) {
			c, m := NewCoverage(t, sessA, "c2", seq, domain.CoverageProvenance, ContentRef(a), ContentRef(b))
			m[0], m[1] = m[1], m[0]
			return c, m
		}, domain.ErrInvalidRecord},
		{"duplicate member", func(seq uint64) (domain.CoverageRecord, []domain.CoverageMember) {
			c, m := NewCoverage(t, sessA, "c2", seq, domain.CoverageProvenance, ContentRef(a), ContentRef(b))
			m[1] = m[0]
			return c, m
		}, domain.ErrInvalidRecord},
		{"member count disagrees", func(seq uint64) (domain.CoverageRecord, []domain.CoverageMember) {
			c, m := NewCoverage(t, sessA, "c2", seq, domain.CoverageProvenance, ContentRef(a), ContentRef(b))
			return c, m[:1]
		}, domain.ErrInvalidRecord},
		{"forged signature", func(seq uint64) (domain.CoverageRecord, []domain.CoverageMember) {
			c, m := NewCoverage(t, sessA, "c2", seq, domain.CoverageProvenance, ContentRef(a))
			c.Signature = b.ContentHash
			return c, m
		}, domain.ErrInvalidRecord},
		{"member ID is not its key", func(seq uint64) (domain.CoverageRecord, []domain.CoverageMember) {
			c, m := NewCoverage(t, sessA, "c2", seq, domain.CoverageProvenance, ContentRef(a))
			m[0].ID = "m1"
			return c, m
		}, domain.ErrInvalidRecord},
		{"missing nested coverage", func(seq uint64) (domain.CoverageRecord, []domain.CoverageMember) {
			return signCoverage(t, domain.CoverageRecord{SemanticMeta: Meta(sessA, "c2", seq), Purpose: domain.CoverageProvenance,
				Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sessA}},
				[]domain.CoverageMember{{SemanticMeta: Meta(sessA, "", seq), CoverageID: "c2", NestedCoverageID: "nope"}})
		}, domain.ErrInvalidRecord},
		{"missing exchange", func(seq uint64) (domain.CoverageRecord, []domain.CoverageMember) {
			return signCoverage(t, domain.CoverageRecord{SemanticMeta: Meta(sessA, "c2", seq), Purpose: domain.CoverageProvenance,
				Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sessA}},
				[]domain.CoverageMember{{SemanticMeta: Meta(sessA, "", seq), CoverageID: "c2", ExchangeID: "nope"}})
		}, domain.ErrInvalidRecord},
	}
	for _, tc := range cases {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			return semantic(t, tx).InsertCoverage(tc.edit(tx.NextSeq()))
		})
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	// Nested coverage names an earlier record; members of another purpose
	// never appear in its source index.
	update(t, s, sessA, func(tx store.Tx) error {
		seq := tx.NextSeq()
		c, m := signCoverage(t, domain.CoverageRecord{SemanticMeta: Meta(sessA, "outer", seq), Purpose: domain.CoverageGenerationInput,
			Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sessA}},
			[]domain.CoverageMember{{SemanticMeta: Meta(sessA, "", seq), CoverageID: "outer", NestedCoverageID: "cov"}})
		return semantic(t, tx).InsertCoverage(c, m)
	})
	// An edge refers to stored normalized coverage by ID.
	update(t, s, sessA, func(tx store.Tx) error {
		r := NewRelationship(sessA, "derived", domain.RelDerivedFrom, "b", "a", tx.NextSeq())
		r.CoverageID = "cov"
		return tx.InsertRelationship(r)
	})
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		r := NewRelationship(sessA, "derived2", domain.RelDerivedFrom, "b", "a", tx.NextSeq())
		r.CoverageID = "missing"
		return tx.InsertRelationship(r)
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		rels, err := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom})
		noErr(t, err)
		if len(rels) != 1 || rels[0].CoverageID != "cov" {
			t.Errorf("derived edge = %+v, want its coverage reference", rels)
		}
		got, err := readSemantic(t, tx).CoverageMembers("outer", store.Page{Limit: 5})
		noErr(t, err)
		if len(got.Records) != 1 || got.Records[0].NestedCoverageID != "cov" {
			t.Errorf("outer members = %+v, want the nested coverage", got.Records)
		}
		return nil
	})
}

// testSemanticExchanges checks logical exchange records (P3-7): dense
// ordinals per conversation, members only while the exchange is open, the
// legal state machine with acknowledgment evidence, and revision CAS.
func testSemanticExchanges(t *testing.T, s store.Store) {
	conv := domain.ConversationIDFor("task", "agent")
	var in domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		in = NewItem(sessA, "in", tx.NextSeq(), "input")
		noErr(t, tx.InsertItem(in))
		sem := semantic(t, tx)
		noErr(t, sem.InsertLogicalExchange(NewExchange(sessA, "x1", "task", "agent", 1, tx.NextSeq())))
		return sem.InsertExchangeMember(domain.ExchangeMember{SemanticMeta: Meta(sessA, "m1", tx.NextSeq()), ExchangeID: "x1", Position: 1, Role: domain.MemberInput, Source: ContentRef(in)})
	})
	for _, tc := range []struct {
		name string
		x    func(seq uint64) domain.LogicalExchange
		want error
	}{
		{"ordinal gap", func(seq uint64) domain.LogicalExchange { return NewExchange(sessA, "x3", "task", "agent", 3, seq) }, domain.ErrInvalidRecord},
		{"ordinal reused", func(seq uint64) domain.LogicalExchange { return NewExchange(sessA, "x9", "task", "agent", 1, seq) }, domain.ErrInvalidRecord},
		{"ID reused", func(seq uint64) domain.LogicalExchange { return NewExchange(sessA, "x1", "task", "agent", 2, seq) }, domain.ErrImmutable},
		{"created closed", func(seq uint64) domain.LogicalExchange {
			x := NewExchange(sessA, "x2", "task", "agent", 2, seq)
			x.State, x.AcknowledgmentID = domain.ExchangeClosed, "ack"
			return x
		}, domain.ErrInvalidTransition},
		{"revision not 1", func(seq uint64) domain.LogicalExchange {
			x := NewExchange(sessA, "x2", "task", "agent", 2, seq)
			x.Revision = 2
			return x
		}, domain.ErrInvalidRecord},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return semantic(t, tx).InsertLogicalExchange(tc.x(tx.NextSeq())) })
		if !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	member := func(id string, seq, pos uint64) domain.ExchangeMember {
		return domain.ExchangeMember{SemanticMeta: Meta(sessA, id, seq), ExchangeID: "x1", Position: pos, Role: domain.MemberInput, Source: ContentRef(in)}
	}
	for _, tc := range []struct {
		name string
		m    func(seq uint64) domain.ExchangeMember
		want error
	}{
		{"position reused", func(seq uint64) domain.ExchangeMember { return member("m2", seq, 1) }, domain.ErrInvalidRecord},
		{"missing exchange", func(seq uint64) domain.ExchangeMember { m := member("m2", seq, 2); m.ExchangeID = "nope"; return m }, domain.ErrInvalidRecord},
		{"missing source", func(seq uint64) domain.ExchangeMember {
			m := member("m2", seq, 2)
			m.Source.ItemID = "ghost"
			return m
		}, domain.ErrInvalidRecord},
		{"admission missing", func(seq uint64) domain.ExchangeMember { m := member("m2", seq, 2); m.AdmissionID = "nope"; return m }, domain.ErrInvalidRecord},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error { return semantic(t, tx).InsertExchangeMember(tc.m(tx.NextSeq())) })
		if !errors.Is(err, tc.want) {
			t.Errorf("member %s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		open, err := r.OpenExchangesByTask("task", store.Page{Limit: 5})
		noErr(t, err)
		if len(open.Records) != 1 || open.Records[0].ID != "x1" {
			t.Errorf("OpenExchangesByTask = %+v, want x1", open.Records)
		}
		byItem, err := r.MembershipsByItem("in", store.Page{Limit: 5})
		noErr(t, err)
		assertEqual(t, "MembershipsByItem", byItem.Records, []domain.ExchangeMember{member("m1", 3, 1)})
		all, err := r.ExchangesByConversation(conv, store.Page{Limit: 5})
		noErr(t, err)
		if len(all.Records) != 1 {
			t.Errorf("ExchangesByConversation = %+v, want x1", all.Records)
		}
		return nil
	})
	// Closure needs a successful acknowledgment of this exchange backed by
	// its generation manifest; the manifest needs coverage and a membership
	// revision.
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		cov, members := NewCoverage(t, sessA, "gen", tx.NextSeq(), domain.CoverageGenerationInput, ContentRef(in))
		noErr(t, sem.InsertCoverage(cov, members))
		_, err := sem.PutConversationMembership(domain.ConversationMembershipState{SemanticMeta: Meta(sessA, "ms", tx.NextSeq()), ConversationID: conv, Revision: 1, LastOrdinal: 1}, 0)
		noErr(t, err)
		noErr(t, sem.InsertAdmissionManifest(domain.AdmissionManifest{SemanticMeta: Meta(sessA, "adm", tx.NextSeq()), ConversationID: conv, ExchangeID: "x1", CallID: "call-1",
			Principal: AgentPrincipal(sessA, "task", "agent"), TurnID: "turn-1", Purpose: domain.AdmissionGenerationInput, CoverageID: "gen", MembershipRevision: 1, PolicyVersion: domain.Phase3PolicyVersion}))
		x, err := sem.LogicalExchange("x1")
		noErr(t, err)
		x.State = domain.ExchangeExecuting
		_, err = sem.PutLogicalExchange(x, 1)
		return err
	})
	ack := func(seq uint64) domain.ExchangeAcknowledgment {
		return domain.ExchangeAcknowledgment{SemanticMeta: Meta(sessA, "ack", seq), ExchangeID: "x1", ManifestID: "adm", ConsumingCallID: "call-1", Actor: HarnessPrincipal(sessA)}
	}
	for _, tc := range []struct {
		name string
		a    func(seq uint64) domain.ExchangeAcknowledgment
	}{
		{"manifest missing", func(seq uint64) domain.ExchangeAcknowledgment { a := ack(seq); a.ManifestID = "nope"; return a }},
		{"consuming call differs", func(seq uint64) domain.ExchangeAcknowledgment { a := ack(seq); a.ConsumingCallID = "call-2"; return a }},
		{"exchange missing", func(seq uint64) domain.ExchangeAcknowledgment { a := ack(seq); a.ExchangeID = "nope"; return a }},
	} {
		rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error { return semantic(t, tx).InsertExchangeAcknowledgment(tc.a(tx.NextSeq())) })
	}
	closed := func(tx store.Tx) domain.LogicalExchange {
		x, err := semantic(t, tx).LogicalExchange("x1")
		noErr(t, err)
		x.State, x.AcknowledgmentID = domain.ExchangeClosed, "ack"
		return x
	}
	// Closing before the acknowledgment exists fails.
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		_, err := semantic(t, tx).PutLogicalExchange(closed(tx), 2)
		return err
	})
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertExchangeAcknowledgment(ack(tx.NextSeq())))
		x, err := sem.PutLogicalExchange(closed(tx), 2)
		noErr(t, err)
		if x.Revision != 3 {
			t.Errorf("closed exchange Revision = %d, want 3", x.Revision)
		}
		return nil
	})
	// A second acknowledgment, a stale revision, reopening, and new members
	// on a closed exchange all fail.
	rejected(t, s, sessA, domain.ErrImmutable, func(tx store.Tx) error {
		a := ack(tx.NextSeq())
		a.ID = "ack2"
		return semantic(t, tx).InsertExchangeAcknowledgment(a)
	})
	rejected(t, s, sessA, domain.ErrVersionConflict, func(tx store.Tx) error {
		_, err := semantic(t, tx).PutLogicalExchange(closed(tx), 2)
		return err
	})
	rejected(t, s, sessA, domain.ErrInvalidTransition, func(tx store.Tx) error {
		x := closed(tx)
		x.State, x.AcknowledgmentID = domain.ExchangeOpen, ""
		_, err := semantic(t, tx).PutLogicalExchange(x, 3)
		return err
	})
	rejected(t, s, sessA, domain.ErrImmutable, func(tx store.Tx) error {
		x := closed(tx)
		x.Turn = 2
		_, err := semantic(t, tx).PutLogicalExchange(x, 3)
		return err
	})
	rejected(t, s, sessA, domain.ErrInvalidTransition, func(tx store.Tx) error {
		return semantic(t, tx).InsertExchangeMember(member("m9", tx.NextSeq(), 2))
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		open, err := r.OpenExchangesByTask("task", store.Page{Limit: 5})
		noErr(t, err)
		if len(open.Records) != 0 {
			t.Errorf("OpenExchangesByTask after closure = %+v, want none", open.Records)
		}
		adm, err := r.AdmissionsByExchange("x1", store.Page{Limit: 5})
		noErr(t, err)
		if len(adm.Records) != 1 || adm.Records[0].ID != "adm" {
			t.Errorf("AdmissionsByExchange = %+v, want adm", adm.Records)
		}
		got, err := r.ExchangeAcknowledgment("ack")
		noErr(t, err)
		assertEqual(t, "ExchangeAcknowledgment", got, ack(got.Seq))
		return nil
	})
}

// testSemanticMembershipFrontier checks the conversation membership CAS
// (P3-7): the closed frontier only advances over exchanges closed by a
// consuming acknowledgment, and neither counter moves backward.
func testSemanticMembershipFrontier(t *testing.T, s store.Store) {
	conv := domain.ConversationIDFor("task", "agent")
	state := func(seq, last, frontier uint64) domain.ConversationMembershipState {
		return domain.ConversationMembershipState{SemanticMeta: Meta(sessA, "ms", seq), ConversationID: conv, Revision: 1, LastOrdinal: last, ClosedFrontier: frontier}
	}
	var in domain.ContextItem
	update(t, s, sessA, func(tx store.Tx) error {
		in = NewItem(sessA, "in", tx.NextSeq(), "input")
		noErr(t, tx.InsertItem(in))
		sem := semantic(t, tx)
		noErr(t, sem.InsertLogicalExchange(NewExchange(sessA, "x1", "task", "agent", 1, tx.NextSeq())))
		noErr(t, sem.InsertLogicalExchange(NewExchange(sessA, "x2", "task", "agent", 2, tx.NextSeq())))
		got, err := sem.PutConversationMembership(state(tx.NextSeq(), 2, 0), 0)
		noErr(t, err)
		if got.Revision != 1 {
			t.Errorf("created membership Revision = %d, want 1", got.Revision)
		}
		return nil
	})
	rejected(t, s, sessA, domain.ErrVersionConflict, func(tx store.Tx) error {
		_, err := semantic(t, tx).PutConversationMembership(state(tx.NextSeq(), 2, 0), 0)
		return err
	})
	// x1 is still open, so the frontier cannot cover it.
	rejected(t, s, sessA, domain.ErrInvalidTransition, func(tx store.Tx) error {
		_, err := semantic(t, tx).PutConversationMembership(state(tx.NextSeq(), 2, 1), 1)
		return err
	})
	// LastOrdinal beyond the recorded exchanges is not membership.
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		_, err := semantic(t, tx).PutConversationMembership(state(tx.NextSeq(), 3, 0), 1)
		return err
	})
	closeX := func(sem store.SemanticTx, tx store.Tx, id string, cancelled bool) {
		x, err := sem.LogicalExchange(id)
		noErr(t, err)
		a := domain.ExchangeAcknowledgment{SemanticMeta: Meta(sessA, "ack-"+id, tx.NextSeq()), ExchangeID: id, Actor: HarnessPrincipal(sessA)}
		if cancelled {
			a.Cancelled, a.CancellationReason = true, domain.ExchangeExplicitCancellation
			x.State = domain.ExchangeCancelled
		} else {
			cov, members := NewCoverage(t, sessA, "gen-"+id, tx.NextSeq(), domain.CoverageGenerationInput, ContentRef(in))
			noErr(t, sem.InsertCoverage(cov, members))
			noErr(t, sem.InsertAdmissionManifest(domain.AdmissionManifest{SemanticMeta: Meta(sessA, "adm-"+id, tx.NextSeq()), ConversationID: conv, ExchangeID: id, CallID: "call-" + id,
				Principal: AgentPrincipal(sessA, "task", "agent"), TurnID: "turn-1", Purpose: domain.AdmissionGenerationInput, CoverageID: "gen-" + id, MembershipRevision: 1, PolicyVersion: domain.Phase3PolicyVersion}))
			a.ManifestID, a.ConsumingCallID = "adm-"+id, "call-"+id
			x.State = domain.ExchangeClosed
		}
		noErr(t, sem.InsertExchangeAcknowledgment(a))
		x.AcknowledgmentID = a.ID
		_, err = sem.PutLogicalExchange(x, x.Revision)
		noErr(t, err)
	}
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		closeX(sem, tx, "x1", false)
		closeX(sem, tx, "x2", true)
		got, err := sem.PutConversationMembership(state(tx.NextSeq(), 2, 1), 1)
		noErr(t, err)
		if got.Revision != 2 || got.ClosedFrontier != 1 {
			t.Errorf("membership = %+v, want revision 2 frontier 1", got)
		}
		return nil
	})
	// A cancelled exchange is not coverage.
	rejected(t, s, sessA, domain.ErrInvalidTransition, func(tx store.Tx) error {
		_, err := semantic(t, tx).PutConversationMembership(state(tx.NextSeq(), 2, 2), 2)
		return err
	})
	// The frontier never moves backward.
	rejected(t, s, sessA, domain.ErrInvalidTransition, func(tx store.Tx) error {
		_, err := semantic(t, tx).PutConversationMembership(state(tx.NextSeq(), 2, 0), 2)
		return err
	})
	// A Put's sequence must be allocated in its own transaction.
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		_, err := semantic(t, tx).PutConversationMembership(state(1, 2, 1), 2)
		return err
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		got, err := readSemantic(t, tx).ConversationMembership(conv)
		noErr(t, err)
		if got.Revision != 2 || got.ClosedFrontier != 1 || got.LastOrdinal != 2 {
			t.Errorf("ConversationMembership = %+v", got)
		}
		_, err = readSemantic(t, tx).ConversationMembership("other")
		wantErr(t, err, domain.ErrNotFound)
		return nil
	})
}

// testSemanticCheckpoints checks the dedicated checkpoint marker (P3-27): a
// CHECKPOINT-role item, distinct source and exchange coverage, a closed
// frontier excluding the issuing round, and access-filtered newest-first
// lookup.
func testSemanticCheckpoints(t *testing.T, s store.Store) {
	conv := domain.ConversationIDFor("task", "agent")
	var in domain.ContextItem
	checkpointItem := func(id string, seq uint64) domain.ContextItem {
		it := NewItem(sessA, id, seq, "summary "+id)
		it.Kind, it.Role = domain.KindSummary, domain.RoleCheckpoint
		it.Authority, it.Scope = domain.AuthorityAgent, domain.ScopeAgent
		it.Access = domain.AccessBoundary{Scope: domain.ScopeAgent, SessionID: sessA, TaskID: "task", AgentID: "agent"}
		return it
	}
	update(t, s, sessA, func(tx store.Tx) error {
		in = NewItem(sessA, "in", tx.NextSeq(), "input")
		noErr(t, tx.InsertItem(in))
		sem := semantic(t, tx)
		for i, id := range []string{"x1", "x2", "x3"} {
			noErr(t, sem.InsertLogicalExchange(NewExchange(sessA, id, "task", "agent", uint64(i+1), tx.NextSeq())))
		}
		_, err := sem.PutConversationMembership(domain.ConversationMembershipState{SemanticMeta: Meta(sessA, "ms", tx.NextSeq()), ConversationID: conv, Revision: 1, LastOrdinal: 3}, 0)
		noErr(t, err)
		src, srcMembers := NewCoverage(t, sessA, "src", tx.NextSeq(), domain.CoverageGenerationInput, ContentRef(in))
		noErr(t, sem.InsertCoverage(src, srcMembers))
		noErr(t, sem.InsertAdmissionManifest(domain.AdmissionManifest{SemanticMeta: Meta(sessA, "gen", tx.NextSeq()), ConversationID: conv, ExchangeID: "x3", CallID: "call-3",
			Principal: AgentPrincipal(sessA, "task", "agent"), TurnID: "turn-1", Purpose: domain.AdmissionGenerationInput, CoverageID: "src", MembershipRevision: 1, PolicyVersion: domain.Phase3PolicyVersion}))
		seq := tx.NextSeq()
		ex, exMembers := signCoverage(t, domain.CoverageRecord{SemanticMeta: Meta(sessA, "prefix", seq), Purpose: domain.CoverageExchangeReplacement,
			Access: domain.AccessBoundary{Scope: domain.ScopeSession, SessionID: sessA}, ConversationID: conv, MembershipRevision: 1, ClosedFrontier: 2},
			[]domain.CoverageMember{{SemanticMeta: Meta(sessA, "", seq), CoverageID: "prefix", ExchangeID: "x1"}, {SemanticMeta: Meta(sessA, "", seq), CoverageID: "prefix", ExchangeID: "x2"}})
		noErr(t, sem.InsertCoverage(ex, exMembers))
		noErr(t, tx.InsertItem(checkpointItem("ck1", tx.NextSeq())))
		return tx.InsertItem(checkpointItem("ck2", tx.NextSeq()))
	})
	checkpoint := func(id, item string, seq uint64) domain.Checkpoint {
		return domain.Checkpoint{SemanticMeta: Meta(sessA, id, seq), ItemID: item, ConversationID: conv, IssuingExchangeID: "x3", GenerationManifestID: "gen",
			SnapshotSeq: seq - 1, MembershipRevision: 1, CoveredFrontier: 2, SourceCoverageID: "src", CoveredExchangesID: "prefix", PolicyVersion: domain.Phase3PolicyVersion}
	}
	for _, tc := range []struct {
		name string
		edit func(c *domain.Checkpoint)
	}{
		{"item is not a checkpoint", func(c *domain.Checkpoint) { c.ItemID = "in" }},
		{"item missing", func(c *domain.Checkpoint) { c.ItemID = "ghost" }},
		{"issuing round inside the frontier", func(c *domain.Checkpoint) { c.IssuingExchangeID = "x2" }},
		{"frontier disagrees with coverage", func(c *domain.Checkpoint) { c.CoveredFrontier = 1 }},
		{"exchange coverage has the wrong purpose", func(c *domain.Checkpoint) { c.CoveredExchangesID = "src"; c.SourceCoverageID = "prefix" }},
		{"generation manifest missing", func(c *domain.Checkpoint) { c.GenerationManifestID = "nope" }},
		{"prior checkpoint missing", func(c *domain.Checkpoint) { c.PriorCheckpointID = "nope" }},
		{"another conversation", func(c *domain.Checkpoint) { c.ConversationID = domain.ConversationIDFor("task", "other") }},
	} {
		rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
			c := checkpoint("bad", "ck1", tx.NextSeq())
			tc.edit(&c)
			return semantic(t, tx).InsertCheckpoint(c)
		})
	}
	update(t, s, sessA, func(tx store.Tx) error {
		sem := semantic(t, tx)
		noErr(t, sem.InsertCheckpoint(checkpoint("c1", "ck1", tx.NextSeq())))
		c2 := checkpoint("c2", "ck2", tx.NextSeq())
		c2.PriorCheckpointID = "c1"
		return sem.InsertCheckpoint(c2)
	})
	rejected(t, s, sessA, domain.ErrImmutable, func(tx store.Tx) error {
		return semantic(t, tx).InsertCheckpoint(checkpoint("c3", "ck1", tx.NextSeq()))
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		owner := AgentPrincipal(sessA, "task", "agent")
		p1, err := r.CheckpointsByConversation(owner, conv, store.Page{Limit: 1})
		noErr(t, err)
		if len(p1.Records) != 1 || p1.Records[0].ID != "c2" || !p1.More {
			t.Fatalf("first checkpoint page = %+v, want newest c2 and More", p1)
		}
		p2, err := r.CheckpointsByConversation(owner, conv, store.Page{After: p1.Next, Limit: 5})
		noErr(t, err)
		if len(p2.Records) != 1 || p2.Records[0].ID != "c1" || p2.More {
			t.Errorf("second checkpoint page = %+v, want c1 only", p2)
		}
		// Another agent cannot see the agent-private checkpoints, and learns
		// nothing from the page shape.
		other, err := r.CheckpointsByConversation(AgentPrincipal(sessA, "task", "other"), conv, store.Page{Limit: 5})
		noErr(t, err)
		if len(other.Records) != 0 || other.More {
			t.Errorf("other agent's checkpoint page = %+v, want empty", other)
		}
		got, err := r.Checkpoint("c1")
		noErr(t, err)
		assertEqual(t, "Checkpoint", got, checkpoint("c1", "ck1", got.Seq))
		return nil
	})
}

// testSemanticReceipts checks immutable mutation and tool receipts (P3-2,
// P3-24): exact identity keys, immutability, and the tool receipt's link to
// its mutation receipt.
func testSemanticReceipts(t *testing.T, s store.Store) {
	principal := AgentPrincipal(sessA, "task", "agent")
	args := []byte("canonical-args")
	receipt := func(seq uint64, requestID string) domain.MutationReceipt {
		id, err := domain.MutationReceiptID(principal, domain.MutationTool, requestID)
		noErr(t, err)
		h, err := domain.MutationRequestHash(principal, domain.MutationTool, "context_remember", args)
		noErr(t, err)
		return domain.MutationReceipt{SemanticMeta: Meta(sessA, id, seq), Family: domain.MutationTool, RequestID: requestID, Principal: principal,
			CanonicalMethod: "context_remember", CanonicalArguments: args, RequestHashVersion: domain.RequestHashV3, RequestHash: h,
			PolicyVersion: domain.Phase3PolicyVersion, Result: domain.MutationResult{Tool: &domain.ToolResult{Keyed: &domain.KeyedWriteResult{ItemID: "k1", CanonicalItemID: "k1"}}}}
	}
	inv := domain.ToolInvocation{SessionID: sessA, ConversationID: domain.ConversationIDFor("task", "agent"), CallID: "call-1", ToolCallID: "tc-1", ExchangeID: "x1", TurnID: "turn-1", Principal: principal}
	invID, err := inv.ID()
	noErr(t, err)
	var mr domain.MutationReceipt
	toolReceipt := func(seq uint64) domain.ToolExecutionReceipt {
		return domain.ToolExecutionReceipt{SemanticMeta: Meta(sessA, invID, seq), Invocation: inv, Method: "context_remember", MutationReceiptID: mr.ID,
			RequestHash: mr.RequestHash, Result: domain.ToolResult{Keyed: &domain.KeyedWriteResult{ItemID: "k1", CanonicalItemID: "k1"}}}
	}
	update(t, s, sessA, func(tx store.Tx) error {
		noErr(t, tx.InsertItem(NewItem(sessA, "k1", tx.NextSeq(), "remembered")))
		mr = receipt(tx.NextSeq(), "req-1")
		sem := semantic(t, tx)
		noErr(t, sem.InsertMutationReceipt(mr))
		return sem.InsertToolExecutionReceipt(toolReceipt(tx.NextSeq()))
	})
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		got, err := r.MutationReceipt(domain.MutationTool, "req-1")
		noErr(t, err)
		assertEqual(t, "MutationReceipt", got, mr)
		_, err = r.MutationReceipt(domain.MutationLifecycle, "req-1")
		wantErr(t, err, domain.ErrNotFound)
		tr, err := r.ToolExecutionReceipt(invID)
		noErr(t, err)
		assertEqual(t, "ToolExecutionReceipt", tr, toolReceipt(3))
		return nil
	})
	rejected(t, s, sessA, domain.ErrImmutable, func(tx store.Tx) error {
		return semantic(t, tx).InsertMutationReceipt(receipt(tx.NextSeq(), "req-1"))
	})
	rejected(t, s, sessA, domain.ErrImmutable, func(tx store.Tx) error {
		return semantic(t, tx).InsertToolExecutionReceipt(toolReceipt(tx.NextSeq()))
	})
	rejected(t, s, sessA, domain.ErrInvalidRecord, func(tx store.Tx) error {
		r := receipt(tx.NextSeq(), "req-2")
		r.ID = "mut_forged"
		return semantic(t, tx).InsertMutationReceipt(r)
	})
	for _, tc := range []struct {
		name string
		edit func(r *domain.ToolExecutionReceipt)
	}{
		{"ID is not the invocation ID", func(r *domain.ToolExecutionReceipt) { r.ID = "tool-forged" }},
		{"mutation receipt missing", func(r *domain.ToolExecutionReceipt) { r.MutationReceiptID = "mut_missing" }},
		{"request hash disagrees", func(r *domain.ToolExecutionReceipt) { r.RequestHash = domain.HashBytes([]byte("other")) }},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			r := toolReceipt(tx.NextSeq())
			r.Invocation.ToolCallID = "tc-2"
			id, err := r.Invocation.ID()
			noErr(t, err)
			r.ID = id
			tc.edit(&r)
			return semantic(t, tx).InsertToolExecutionReceipt(r)
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("tool receipt %s: error = %v, want ErrInvalidRecord", tc.name, err)
		}
	}
	// Every record a result names must be stored by commit (P3-2), in the
	// same transaction or earlier.
	for _, tc := range []struct {
		name   string
		result domain.MutationResult
	}{
		{"keyed item missing", domain.MutationResult{Tool: &domain.ToolResult{Keyed: &domain.KeyedWriteResult{ItemID: "ghost", CanonicalItemID: "ghost"}}}},
		{"checkpoint missing", domain.MutationResult{Tool: &domain.ToolResult{CheckpointID: "c-missing"}}},
		{"item audit missing", domain.MutationResult{Item: &domain.ItemMutationResult{ItemID: "k1", BeforeVersion: 1, AfterVersion: 2, AuditID: "l-missing",
			Before: domain.ObservedItemState{Source: domain.ItemContentRef{ItemID: "k1", ContentHash: fpA}, Version: 1, Currentness: domain.ItemUnkeyed,
				Generation: domain.GenerationWorking, Residency: domain.ResidencyResident, Authority: domain.AuthorityUser, Expiry: domain.ExpiryLive},
			After: domain.ObservedItemState{Source: domain.ItemContentRef{ItemID: "k1", ContentHash: fpA}, Version: 2, Currentness: domain.ItemUnkeyed,
				Generation: domain.GenerationWorking, Residency: domain.ResidencyResident, Authority: domain.AuthorityUser, Expiry: domain.ExpiryLive}}}},
		{"granted record missing", domain.MutationResult{Records: &domain.RecordResult{Kind: "GRANT", IDs: []string{"g-missing"}}}},
	} {
		err := s.Update(ctx, sessA, func(tx store.Tx) error {
			r := receipt(tx.NextSeq(), "req-"+tc.name[:4])
			r.Result = tc.result
			return semantic(t, tx).InsertMutationReceipt(r)
		})
		if !errors.Is(err, domain.ErrInvalidRecord) {
			t.Errorf("result %s: error = %v, want ErrInvalidRecord", tc.name, err)
		}
	}
	// A result may name a record the same transaction stores after it.
	update(t, s, sessA, func(tx store.Tx) error {
		r := receipt(tx.NextSeq(), "req-later")
		r.Result = domain.MutationResult{Tool: &domain.ToolResult{Keyed: &domain.KeyedWriteResult{ItemID: "k2", CanonicalItemID: "k2"}}}
		noErr(t, semantic(t, tx).InsertMutationReceipt(r))
		return tx.InsertItem(NewItem(sessA, "k2", tx.NextSeq(), "written after its receipt"))
	})
	// Records returned by the facet are deep copies.
	view(t, s, sessA, func(tx store.ReadTx) error {
		r := readSemantic(t, tx)
		got, err := r.MutationReceipt(domain.MutationTool, "req-1")
		noErr(t, err)
		got.CanonicalArguments[0] = 'X'
		got.Result.Tool.Keyed.ItemID = "mutated"
		again, err := r.MutationReceipt(domain.MutationTool, "req-1")
		noErr(t, err)
		assertEqual(t, "MutationReceipt after caller mutation", again, mr)
		return nil
	})
}
