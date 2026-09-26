package graph

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

func (f *membershipFaultWriter) InsertAdmissionManifest(m domain.AdmissionManifest) error {
	return f.after(f.SemanticTx.InsertAdmissionManifest(m))
}

// Admission (3 writes) and acknowledgment (4 writes) roll back as one unit
// even when the application ignores the service error.
func TestMembershipAdmissionAndAcknowledgmentPoisonEveryPartialWrite(t *testing.T) {
	for _, ack := range []bool{false, true} {
		writes := 3
		if ack {
			writes = 4
		}
		for failAt := 1; failAt <= writes; failAt++ {
			s, service, actor, registration := membershipTestStore(t)
			var r membershipRound
			var admit domain.AdmitExchangeIntent
			var acknowledge domain.AcknowledgeExchangeIntent
			update(t, s, "s", func(tx store.Tx) error {
				var err error
				if r, err = registerMembershipRound(tx, service, actor, registration, "1", true); err != nil {
					return err
				}
				if _, err = membershipCall(tx, r.x, "consume"); err != nil {
					return err
				}
				coverage, err := service.RecordAdmissionCoverage(tx, actor, registration.Principal, "input", []domain.ItemContentRef{storetest.ContentRef(r.toolResult)})
				if err != nil {
					return err
				}
				sem, _ := store.Semantic(tx)
				state, _ := sem.ConversationMembership(r.x.ConversationID)
				admit = domain.AdmitExchangeIntent{RequestID: "admit", ExchangeID: r.x.ID, CoverageID: coverage.ID, CallID: "consume", Purpose: domain.AdmissionGenerationInput, ExpectedMembershipRevision: state.Revision}
				if !ack {
					return nil
				}
				admitted, err := service.AdmitExchange(tx, actor, admit)
				acknowledge = domain.AcknowledgeExchangeIntent{RequestID: "ack", ExchangeID: r.x.ID, ConsumingCallID: "consume", ExpectedRevision: r.x.Revision}
				if err == nil {
					acknowledge.ManifestID = admitted.IDs[0]
				}
				return err
			})
			run := func(tx store.Tx) error {
				if ack {
					_, err := service.AcknowledgeExchange(tx, actor, acknowledge)
					return err
				}
				_, err := service.AdmitExchange(tx, actor, admit)
				return err
			}
			var before uint64
			err := s.Update(ctx, "s", func(tx store.Tx) error {
				before = tx.LastSeq()
				sem, _ := store.Semantic(tx)
				if err := run(membershipFaultTx{tx, &membershipFaultWriter{SemanticTx: sem, failAt: failAt}}); !errors.Is(err, errMembershipWrite) {
					t.Fatalf("ack=%v write %d: %v", ack, failAt, err)
				}
				return nil
			})
			if !errors.Is(err, errMembershipWrite) {
				t.Fatalf("ack=%v write %d committed: %v", ack, failAt, err)
			}
			update(t, s, "s", func(tx store.Tx) error {
				sem, _ := store.Semantic(tx)
				x, _ := sem.LogicalExchange(r.x.ID)
				admissions, _ := sem.AdmissionsByExchange(r.x.ID, store.Page{Limit: 4})
				if tx.LastSeq() != before || x.State != domain.ExchangeExecuting || !ack && len(admissions.Records) != 0 {
					t.Fatalf("ack=%v write %d: partial state %+v", ack, failAt, x)
				}
				return run(tx)
			})
		}
	}
}
