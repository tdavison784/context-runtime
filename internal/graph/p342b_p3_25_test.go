package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestP3_25_RequestTranscriptEdgeLeavesSupportUnsupported: a request
// transcript's edge never turns into evidence support — the item stays
// unsupported even though the edge exists (P3-25 — the cited
// TestCreationDeclarationSupportQualifiesOnlyToolResultTranscripts cites
// bare transcript rows and never creates an edge or reads a declaration
// back). A keyed AGENT_KEY occurrence carries exactly the edge a real
// context_remember files: DERIVED_FROM provenance to the exchange's USER
// request transcript, linked in the same transaction that created it. That
// edge is accepted and readable. Despite it, the same transcript as
// EVIDENCE_SUPPORT is refused (fail closed, the whole transaction rolls
// back), the creation declaration naming it as support is refused the same
// way, and the read-back shows the truth: the edge-carrying item has no
// creation declaration (unsupported), and no evidence edge exists anywhere.
// A plain semantic EVIDENCE item of the same boundary qualifies both ways, so the
// gate is the transcript's provenance, not the edge's absence.
func TestP3_25_RequestTranscriptEdgeLeavesSupportUnsupported(t *testing.T) {
	eachStore(t, func(t *testing.T, s store.Store) {
		const sess = "sess-p342b-edge"
		actor := principal(sess, domain.AuthorityAgent)
		var transcript, keyed, fact domain.ContextItem
		if err := s.Update(ctx, sess, func(tx store.Tx) error {
			transcript = rawTranscript(sess, "req-transcript", tx.NextSeq(), domain.AuthorityUser, domain.KindUserMessage)
			keyed = agentDirective(sess, "keyed", domain.AgentKeyID("status"), tx.NextSeq())
			fact = taskItem(sess, "evidence-item", tx.NextSeq(), domain.AuthorityUser)
			fact.Role, fact.Kind = domain.RoleSemantic, domain.KindEvidence
			mustInsert(t, tx, transcript, keyed, fact)
			// The edge a real keyed write files: provenance to the request
			// transcript, in the creating transaction.
			_, err := LinkDerivedCoverage(tx, actor, keyed.ID, []string{transcript.ID}, domain.CoverageProvenance, "evt-prov", 4)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.View(ctx, sess, func(tx store.ReadTx) error {
			rels, _ := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: keyed.ID, ToID: transcript.ID})
			if len(rels) != 1 {
				t.Errorf("provenance edge not recorded: %+v", rels)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// Despite that edge, the transcript is not support: a fresh keyed
		// occurrence citing it as EVIDENCE_SUPPORT fails closed and rolls
		// the whole transaction back.
		mustRefuse := func(want error, fn func(tx store.Tx) error) {
			t.Helper()
			if err := s.Update(ctx, sess, fn); !errors.Is(err, want) {
				t.Fatalf("refusal = %v, want %v", err, want)
			}
		}
		mustRefuse(domain.ErrInvalidRecord, func(tx store.Tx) error {
			fresh := agentDirective(sess, "keyed-ev", domain.AgentKeyID("status"), tx.NextSeq())
			mustInsert(t, tx, fresh)
			_, err := LinkDerivedCoverage(tx, actor, fresh.ID, []string{transcript.ID}, domain.CoverageEvidenceSupport, "evt-ev", 4)
			return err
		})
		mustRefuse(domain.ErrInvalidAuthorityPromotion, func(tx store.Tx) error {
			fresh := agentDirective(sess, "keyed-decl", domain.AgentKeyID("status"), tx.NextSeq())
			mustInsert(t, tx, fresh)
			_, err := DeclareCreation(tx, fresh, CreationAcceptance{PolicyVersion: testDeclarationPolicy, SupportIDs: []string{transcript.ID}})
			return err
		})
		// The refusals were atomic: the only edge is still the provenance
		// one, no evidence edge exists anywhere, and the edge-carrying item
		// reads unsupported — no creation declaration.
		if err := s.View(ctx, sess, func(tx store.ReadTx) error {
			rels, _ := tx.Relationships(store.RelationshipFilter{Type: domain.RelDerivedFrom, FromID: keyed.ID, ToID: transcript.ID})
			if len(rels) != 1 {
				t.Errorf("refused support attempts changed the edges: %+v", rels)
			}
			r, _ := store.ReadSemantic(tx)
			if _, err := r.CreationDeclaration(keyed.ID); !errors.Is(err, domain.ErrNotFound) {
				t.Errorf("edge-carrying item carries a creation declaration: %v", err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}

		// The gate is the transcript's provenance, not the edge's absence:
		// a plain semantic EVIDENCE item of the same boundary qualifies
		// both ways, in one creating transaction.
		var declared domain.CreationDeclaration
		if err := s.Update(ctx, sess, func(tx store.Tx) error {
			fresh := agentDirective(sess, "keyed-ok", domain.AgentKeyID("status"), tx.NextSeq())
			mustInsert(t, tx, fresh)
			if _, err := LinkDerivedCoverage(tx, actor, fresh.ID, []string{fact.ID}, domain.CoverageEvidenceSupport, "evt-fact", 4); err != nil {
				return err
			}
			var err error
			declared, err = DeclareCreation(tx, fresh, CreationAcceptance{PolicyVersion: testDeclarationPolicy, SupportIDs: []string{fact.ID}})
			if err != nil {
				return err
			}
			keyed = fresh
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.View(ctx, sess, func(tx store.ReadTx) error {
			r, _ := store.ReadSemantic(tx)
			got, err := r.CreationDeclaration(keyed.ID)
			if err != nil || got.ID != declared.ID || len(got.AcceptedSemantics.SupportIDs) != 1 || got.AcceptedSemantics.SupportIDs[0] != fact.ID {
				t.Errorf("declared support reads back as %+v (%v)", got, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}
