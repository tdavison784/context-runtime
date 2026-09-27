package lifecycle

import (
	"context"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// liveLease gives the active task's agent a live lease on itemID's content.
func liveLease(t *testing.T, db store.Store, id, itemID string) {
	t.Helper()
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		sem, err := store.Semantic(tx)
		if err != nil {
			return err
		}
		it, err := tx.Item(itemID)
		if err != nil {
			return err
		}
		holder := storetest.NewPrincipal("s", domain.AuthorityAgent)
		conv := domain.ConversationIDFor(holder.TaskID, holder.AgentID)
		if _, err := tx.Conversation(conv); err != nil {
			if _, err := tx.PutConversation(storetest.NewConversation("s", conv), 0); err != nil {
				return err
			}
		}
		return sem.InsertRetrievalLease(domain.RetrievalLease{SemanticMeta: domain.SemanticMeta{ID: id, SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
			Holder: holder, ConversationID: conv, TurnID: "turn-2", Source: domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash}, CallAllowance: 2, PolicyVersion: "lease/v1"})
	}); err != nil {
		t.Fatal(err)
	}
}

func residency(t *testing.T, db store.Store, id string) domain.Residency {
	t.Helper()
	var r domain.Residency
	if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
		it, err := tx.Item(id)
		r = it.Residency
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return r
}

// SEC-4.2 / SPEC-4.2: J2 freezes the candidate set, not protections. A lease
// taken after batch 1 still protects its item in a later batch.
func TestLeaseTakenAfterFirstBatchProtects_SEC42(t *testing.T) {
	for _, when := range []string{"before-request", "after-batch-1"} {
		t.Run(when, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				pol := testPolicy()
				pol.MaxGCDecisions = 1
				s, _ := New(db, pol)
				seedEphemeral(t, db, 3, 0)
				if when == "before-request" {
					liveLease(t, db, "lease", "eph-002")
				}
				id := enqueueScratch(t, db, s)
				if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
					_, err := s.ExecuteGCRequest(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), id, 0)
					return err
				}); err != nil {
					t.Fatal(err)
				}
				if when == "after-batch-1" {
					liveLease(t, db, "lease", "eph-002")
				}
				runGC(t, db, s, id, 10)
				if got := residency(t, db, "eph-000"); got != domain.ResidencyArchived {
					t.Fatalf("setup: unleased item %s", got)
				}
				if got := residency(t, db, "eph-002"); got != domain.ResidencyResident {
					t.Errorf("leased item %s", got)
				}
			})
		})
	}
}
