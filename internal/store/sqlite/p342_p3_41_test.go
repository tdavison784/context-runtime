package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// TestP3_41_NoDefaultGeneratedGrantsLeasesMembership closes the P3-42 table
// row "no default-generated grants/leases/membership": upgrading a Phase 2
// database through every Phase 3 migration invents none of the records the
// legacy binary never wrote — grants appear exactly as pre-written, and the
// tables the Phase 3 migrations themselves introduce (retrieval leases,
// exchanges, exchange members, conversation membership) stay empty — even
// while the migration Go steps that do write (0026 obligation
// reconciliation, 0034 creation declarations) demonstrably execute.
func TestP3_41_NoDefaultGeneratedGrantsLeasesMembership(t *testing.T) {
	l := openLegacy(t, 17) // the last Phase 2 schema: 0018 on is Phase 3

	// A keyed directive whose receipt snapshot and claim match: drives
	// 0034's creation-declaration step.
	known := storetest.NewItem("s", "known", 1, "rule")
	known.DirectiveID, known.Section, known.Kind = "known", domain.SectionPinned, domain.KindConstraint
	l.insert("item", known, nil)
	l.insert("receipt_item", receiptItem{SessionID: "s", OccurrenceID: "occ-known", Ordinal: 0, Item: known}, nil)
	claim := storetest.NewObligation("s", "o-known", 1, 2, "known")
	claim.Claim = "lint.clean"
	l.insert("obligation", claim, nil)
	fact := storetest.NewItem("s", "fact", 3, "plain fact")
	l.insert("item", fact, nil)

	// A matcher-satisfied obligation: drives 0026's reconciliation step.
	matcher := storetest.NewObligation("s", "o-matcher", 1, 4, "fact")
	matcher.Status, matcher.EvidenceIDs, matcher.Revision = domain.ObligationSatisfied, []string{"ev"}, 2
	l.insert("obligation", matcher, nil)
	tr := storetest.NewTransition("s", "t-matcher", "o-matcher", 1, 5, domain.ObligationUnresolved, domain.ObligationSatisfied)
	tr.EvidenceIDs, tr.Matcher, tr.GrantID = []string{"ev"}, &domain.MatcherRef{Name: "tests_pass", Version: "1"}, "g"
	l.insert("obligation_transition", tr, nil)

	// The one pre-written grant, a conversation, and an event envelope.
	l.insert("grant", storetest.NewGrant("s", "g", 6, "fact"), nil)
	conv := storetest.NewConversation("s", domain.ConversationIDFor("task", "agent"))
	l.insert("conversation", conv, nil)
	env, _ := storetest.NewIngestion("s", "evt", domain.CallerOccurrenceID("s", "evt"), 7)
	l.insert("envelope", env, nil)

	s := l.upgrade()

	// The write steps ran, so the empty tables below are a real result, not
	// a skipped migration.
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		if tx.LastSeq() != 101 {
			t.Errorf("LastSeq = %d, want 101 (the reconciliation wrote at the next sequence)", tx.LastSeq())
		}
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		if d, err := r.CreationDeclaration("known"); err != nil || !d.LegacyKnown {
			t.Errorf("0034 creation declaration did not run: %+v %v", d, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Raw counts: only the pre-written grant exists, and every table the
	// Phase 3 migrations introduce stays empty.
	for table, want := range map[string]int{
		"rec_grant": 1, "rec_retrieval_lease": 0, "rec_exchange": 0, "rec_exchange_member": 0, "rec_membership": 0,
	} {
		var got int
		if err := s.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&got); err != nil || got != want {
			t.Errorf("%s after upgrade = %d (want %d), %v", table, got, want, err)
		}
	}

	// The public readers agree.
	if err := s.View(context.Background(), "s", func(tx store.ReadTx) error {
		r, err := store.ReadSemantic(tx)
		if err != nil {
			t.Fatal(err)
		}
		gs, err := r.GrantsFor(domain.ActionResolve, domain.ItemGrantTarget("s", "fact"), 5)
		if err != nil || len(gs) != 1 || gs[0].ID != "g" {
			t.Errorf("grants for fact = %+v, %v; want only the pre-written g", gs, err)
		}
		for _, it := range []domain.ContextItem{known, fact} {
			leases, err := r.LeasesBySource(domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash}, store.Page{Limit: 5})
			if err != nil || len(leases.Records) != 0 {
				t.Errorf("item %s gained leases: %+v %v", it.ID, leases.Records, err)
			}
		}
		if _, err := r.ConversationMembership(conv.ConversationID); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("conversation gained membership: %v", err)
		}
		exchanges, err := r.ExchangesByConversation(conv.ConversationID, store.Page{Limit: 5})
		if err != nil || len(exchanges.Records) != 0 {
			t.Errorf("conversation gained exchanges: %+v %v", exchanges.Records, err)
		}
		members, err := r.MembershipsByItem("fact", store.Page{Limit: 5})
		if err != nil || len(members.Records) != 0 {
			t.Errorf("item fact gained exchange membership: %+v %v", members.Records, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
