package lifecycle

import (
	"context"
	"fmt"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func enqueueScratch(t *testing.T, db store.Store, s *Service) string {
	t.Helper()
	var id string
	if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
		var err error
		id, err = s.EnqueueGC(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), domain.GCSupersession, domain.CollectTask, "task", "scratch")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return id
}

func runGC(t *testing.T, db store.Store, s *Service, id string, limit int) {
	t.Helper()
	for n := 0; n < limit; n++ {
		if r, ok := gcResult(t, db, id); ok {
			if r.Outcome != domain.GCCollected {
				t.Fatalf("terminal result: %+v", r)
			}
			return
		}
		if _, err := s.CollectPending(context.Background(), "s", func(domain.GCRequest) (domain.Principal, bool) {
			return storetest.NewPrincipal("s", domain.AuthoritySystem), true
		}, 1); err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("collection did not finish")
}

// J1 / XREV-3.1 / SEC-3.2: exact budget boundaries never discard a candidate.
func TestJ1BudgetBoundaryCollectsEveryCandidate(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxTransactionWork = 16
		s, _ := New(db, pol)
		seedEphemeral(t, db, 10, 0)
		id := enqueueScratch(t, db, s)
		runGC(t, db, s, id, 20)
		if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
			for n := range 10 {
				it, err := tx.Item(fmt.Sprintf("eph-%03d", n))
				if err != nil {
					return err
				}
				if it.Residency != domain.ResidencyArchived {
					t.Errorf("%s skipped", it.ID)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// J2 / SPEC-3.6: later insertions cannot extend the first batch's snapshot.
func TestJ2SnapshotAndCursorStayFrozen(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxGCDecisions = 1
		s, _ := New(db, pol)
		seedEphemeral(t, db, 3, 0)
		id := enqueueScratch(t, db, s)
		var first MutationOutcome
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			var err error
			first, err = s.ExecuteGCRequest(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), id, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			it := storetest.NewItem("s", "late", tx.NextSeq(), "late")
			it.Generation = domain.GenerationEphemeral
			return tx.InsertItem(it)
		}); err != nil {
			t.Fatal(err)
		}
		runGC(t, db, s, id, 8)
		readSemantic(t, db, func(sem store.SemanticReader) error {
			r, err := sem.GCResult(id)
			if err != nil {
				return err
			}
			last, err := sem.CollectReceipt(r.CollectReceiptID)
			if last.SnapshotSeq != first.Result.Collect.SnapshotSeq {
				t.Errorf("snapshot moved: %d -> %d", first.Result.Collect.SnapshotSeq, last.SnapshotSeq)
			}
			return err
		})
		if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
			it, err := tx.Item("late")
			if it.Residency != domain.ResidencyResident {
				t.Error("later insertion collected by old snapshot")
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
}

// J3 / XREV-3.2: limits apply to the complete persisted receipt; large
// requests shrink their item-count batch and eventually finish.
func TestJ3CompleteReceiptFitsAndBatchAdapts(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxReceiptBytes = 4096
		s, _ := New(db, pol)
		id := completeLarge(t, db, s, 60)
		runGC(t, db, s, id, 100)
		readSemantic(t, db, func(sem store.SemanticReader) error {
			r, err := sem.GCResult(id)
			if err != nil {
				return err
			}
			c, err := sem.CollectReceipt(r.CollectReceiptID)
			if err != nil {
				return err
			}
			m, err := sem.MutationReceipt(domain.MutationCollection, c.RequestID)
			if err != nil {
				return err
			}
			_, err = domain.CanonicalSemanticArguments(m, pol.MaxReceiptBytes)
			return err
		})
	})
}

func TestJ3WorkExhaustionHalvesPersistedItemCount(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxGCDecisions, pol.MaxTransactionWork = 8, 16
		s, _ := New(db, pol)
		seedEphemeral(t, db, 10, 0)
		id := enqueueScratch(t, db, s)
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			_, err := s.ExecuteGCRequest(tx, storetest.NewPrincipal("s", domain.AuthoritySystem), id, 0)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		readSemantic(t, db, func(sem store.SemanticReader) error {
			p, err := sem.GCProgress(id)
			if p.BatchSize != 4 || p.Cursor.ID != "eph-000" || p.Batches != 1 {
				t.Errorf("progress: %+v", p)
			}
			return err
		})
		runGC(t, db, s, id, 20)
	})
}

// J4 / SEC-3.1 / SPEC-3.3: a long dead-lease history skips only its item.
func TestJ4DeadLeaseHistoryDoesNotFailRequest(t *testing.T) {
	eachStore(t, func(t *testing.T, db store.Store) {
		pol := testPolicy()
		pol.MaxTransactionWork, pol.MaxGCDecisions = 32, 8
		s, _ := New(db, pol)
		id := completeLarge(t, db, s, 10)
		if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
			sem, err := store.Semantic(tx)
			if err != nil {
				return err
			}
			it, err := tx.Item("scratch-000")
			if err != nil {
				return err
			}
			holder := storetest.NewPrincipal("s", domain.AuthorityAgent)
			if _, err := tx.PutConversation(storetest.NewConversation("s", domain.ConversationIDFor(holder.TaskID, holder.AgentID)), 0); err != nil {
				return err
			}
			for n := range 80 {
				if err := sem.InsertRetrievalLease(domain.RetrievalLease{SemanticMeta: domain.SemanticMeta{ID: fmt.Sprintf("dead-%d", n), SessionID: "s", SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()}, Holder: holder,
					ConversationID: domain.ConversationIDFor(holder.TaskID, holder.AgentID), TurnID: "turn", Source: domain.ItemContentRef{ItemID: it.ID, ContentHash: it.ContentHash}, CallAllowance: 1, PolicyVersion: "lease/v1"}); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		runGC(t, db, s, id, 30)
		skipped := false
		readSemantic(t, db, func(sem store.SemanticReader) error {
			req, err := sem.GCRequest(id)
			if err != nil {
				return err
			}
			for batch := uint64(1); batch < 30; batch++ {
				request, _ := domain.GCBatchRequestID(req.RequestID, batch)
				c, err := sem.CollectReceipt(collectReceiptID("s", request))
				if err != nil {
					break
				}
				for _, d := range c.Decisions {
					if d.Target.ItemID == "scratch-000" && string(d.Code) == "SKIP_RESOURCE_LIMIT" {
						skipped = true
					}
				}
			}
			return nil
		})
		if !skipped {
			t.Error("missing item-level closed skip reason")
		}
		if err := db.View(context.Background(), "s", func(tx store.ReadTx) error {
			for n := 1; n < 10; n++ {
				it, err := tx.Item(fmt.Sprintf("scratch-%03d", n))
				if err != nil {
					return err
				}
				if it.Residency != domain.ResidencyArchived {
					t.Errorf("later item %s stranded", it.ID)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

type gcItemFaultTx struct {
	target string
	store.Tx
	fault error
	calls *int
}

func (tx gcItemFaultTx) SemanticTransaction() (store.SemanticTx, error) {
	sem, err := store.Semantic(tx.Tx)
	return gcItemFaultSem{SemanticTx: sem, fault: tx.fault, calls: tx.calls, target: tx.target}, err
}

type gcItemFaultSem struct {
	target string
	store.SemanticTx
	fault error
	calls *int
}

func (s gcItemFaultSem) MembershipsByItem(id string, p store.Page) (store.ResultPage[domain.ExchangeMember], error) {
	if id == s.target || s.target == "" && id == "eph-000" {
		*s.calls++
		return store.ResultPage[domain.ExchangeMember]{}, s.fault
	}
	return s.SemanticTx.MembershipsByItem(id, p)
}

func TestJ4ItemFailuresHaveBoundedRetriesAndClosedReasons(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		want     domain.GCDecisionCode
		attempts int
	}{
		{"permanent", domain.ErrInvalidRecord, domain.GCSkipInvalidItem, 1},
		{"integrity", domain.ErrIntegrity, domain.GCSkipIntegrity, 1},
		{"transient", fmt.Errorf("temporary read failure"), domain.GCSkipAttemptsExhausted, maxGCAttempts},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eachStore(t, func(t *testing.T, db store.Store) {
				s, _ := New(db, testPolicy())
				seedEphemeral(t, db, 2, 0)
				id := enqueueScratch(t, db, s)
				calls := 0
				var decisions []domain.GCDecision
				for pass := 0; pass < 6; pass++ {
					if _, ok := gcResult(t, db, id); ok {
						break
					}
					if err := db.Update(context.Background(), "s", func(tx store.Tx) error {
						out, err := s.ExecuteGCRequest(gcItemFaultTx{Tx: tx, fault: tc.err, calls: &calls}, storetest.NewPrincipal("s", domain.AuthoritySystem), id, 0)
						if err == nil {
							decisions = append(decisions, out.Result.Collect.Decisions...)
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
				}
				r, ok := gcResult(t, db, id)
				if !ok || r.Outcome != domain.GCCollected || calls != tc.attempts || len(decisions) != 2 || decisions[0].Code != tc.want || decisions[1].Code != domain.GCArchive {
					t.Fatalf("result %+v calls %d decisions %+v", r, calls, decisions)
				}
			})
		})
	}
}
