package tools

import (
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// TestTC_P3_6_ToolAcknowledgmentIsNeverEvidenceSupportOnBothStores closes the
// memory-only half of the P3-42 row "semantic support excludes transcript-
// only provenance" (ADR 8 :1234). TestToolAcknowledgmentIsNeverEvidenceSupport
// builds one memory store through toolFixture; the same scenario now runs on
// both backends: the tool's acknowledgment item never qualifies as evidence
// support, and citing it as the evidence of a keyed write is refused with the
// closed invalid-argument error (FR-DOM-006, P3-25).
func TestTC_P3_6_ToolAcknowledgmentIsNeverEvidenceSupportOnBothStores(t *testing.T) {
	p24Stores(t, func(t *testing.T, st store.Store) {
		i := seedToolFixture(t, st)
		s := testService(t)
		first := remember(t, st, s, i, keyed("r1", "db", "postgres"))
		var ack string
		update(t, st, func(tx store.Tx) error {
			sem, _ := store.Semantic(tx)
			members, err := sem.ExchangeMembers(i.ExchangeID, store.Page{Limit: 8})
			if err != nil || len(members.Records) != 3 {
				t.Fatalf("members: %+v, %v", members, err)
			}
			ack = members.Records[2].Source.ItemID
			it, _ := tx.Item(ack)
			if it.QualifiesAsEvidenceSupport() {
				t.Fatalf("acknowledgment qualifies as evidence: %+v", it)
			}
			return nil
		})
		next := addToolCall(t, st, i, "t2")
		err := st.Update(testContext, "s", func(tx store.Tx) error {
			_, err := s.Remember(tx, dispatcher(next), Request[domain.KeyedWriteIntent]{next, keyed("r2", "db", "postgres", ack)}, tx.NextSeq())
			return err
		})
		var te *Error
		if !errors.As(FixedError(err), &te) || te.Code() != domain.ToolErrorInvalidArgument {
			t.Fatalf("acknowledgment accepted as evidence for %s: %v", first.ItemID, err)
		}
	})
}
