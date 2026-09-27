package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
	"github.com/tdavison784/context-runtime/internal/store/storetest"
)

// SEC-2.8: a runtime-derived request ID of another principal gives the same
// error whether or not that principal's lifecycle receipt exists, so
// lifecycle receipts are no existence oracle; the owner still replays.
func TestLifecycleDerivedRequestIDIsNoExistenceOracle(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		seedItem(t, db, storetest.NewItem("s", "fact", 0, "fact"))
		s, _ := New(db, testPolicy())
		owner := storetest.NewPrincipal("s", domain.AuthorityUser)
		registered, err := domain.OperationRequestID(owner, domain.CallerOccurrenceID("s", "event-1"), 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		absent, _ := domain.OperationRequestID(owner, domain.CallerOccurrenceID("s", "event-1"), 2, 0)
		first, err := s.ArchiveStandalone(ctx, owner, domain.ArchiveIntent{RequestID: registered, ItemID: "fact", ExpectedVersion: 1})
		if err != nil {
			t.Fatal(err)
		}
		foreign := owner
		foreign.AgentID = "intruder"
		probe := func(requestID string) error {
			_, err := s.ArchiveStandalone(ctx, foreign, domain.ArchiveIntent{RequestID: requestID, ItemID: "fact", ExpectedVersion: 1})
			return err
		}
		existing, missing := probe(registered), probe(absent)
		if existing == nil || missing == nil || existing.Error() != missing.Error() || errors.Is(existing, domain.ErrEventIDConflict) {
			t.Fatalf("existence oracle: existing=%v missing=%v", existing, missing)
		}
		again, err := s.ArchiveStandalone(ctx, owner, domain.ArchiveIntent{RequestID: registered, ItemID: "fact", ExpectedVersion: 1})
		if err != nil || again.AuditID != first.AuditID {
			t.Fatalf("owner replay: %+v %v", again, err)
		}
	})
}

// legacyReceiptTx serves one committed receipt under a pre-derivation
// runtime request ID, as a database upgraded from an earlier derivation
// would hold (DUR-2.8); real stores refuse to insert such IDs today.
type legacyReceiptTx struct {
	store.Tx
	legacyID string
	receipt  domain.MutationReceipt
}
type legacyReceiptSem struct {
	store.SemanticTx
	x legacyReceiptTx
}

func (t legacyReceiptTx) SemanticTransaction() (store.SemanticTx, error) {
	sem, err := store.Semantic(t.Tx)
	return legacyReceiptSem{SemanticTx: sem, x: t}, err
}
func (t legacyReceiptTx) SemanticReadBackend() store.SemanticReader {
	r, _ := store.ReadSemantic(t.Tx)
	return r
}
func (s legacyReceiptSem) MutationReceipt(f domain.MutationFamily, requestID string) (domain.MutationReceipt, error) {
	if f == domain.MutationLifecycle && requestID == s.x.legacyID {
		return s.x.receipt, nil
	}
	return s.SemanticTx.MutationReceipt(f, requestID)
}

// DUR-2.8 with SEC-2.8: the exact-replay lookup precedes the reserved
// namespace check, so an owner's legacy receipt replays; any other
// principal is checked for request-ID ownership before that receipt's
// existence can matter.
func TestLegacyRuntimeRequestIDReplaysForItsOwnerOnly(t *testing.T) {
	ctx := context.Background()
	eachStore(t, func(t *testing.T, db store.Store) {
		seedItem(t, db, storetest.NewItem("s", "fact", 0, "fact"))
		s, _ := New(db, testPolicy())
		owner := storetest.NewPrincipal("s", domain.AuthorityUser)
		src := domain.ArchiveIntent{RequestID: "legacy-src", ItemID: "fact", ExpectedVersion: 1}
		first, err := s.ArchiveStandalone(ctx, owner, src)
		if err != nil {
			t.Fatal(err)
		}
		const legacyID = "req_predates_derivation"
		var receipt domain.MutationReceipt
		readSemantic(t, db, func(sem store.SemanticReader) error {
			var err error
			receipt, err = sem.MutationReceipt(domain.MutationLifecycle, "legacy-src")
			return err
		})
		// A faithful legacy receipt: its frozen request names the legacy ID.
		legacy := src
		legacy.RequestID = legacyID
		receipt.RequestID = legacyID
		if receipt.CanonicalArguments, err = domain.CanonicalSemanticArguments(legacy, testPolicy().MaxMetadataBytes); err != nil {
			t.Fatal(err)
		}
		if receipt.RequestHash, err = domain.MutationRequestHash(owner, domain.MutationLifecycle, receipt.CanonicalMethod, receipt.CanonicalArguments); err != nil {
			t.Fatal(err)
		}
		archive := func(p domain.Principal) (LifecycleOutcome, error) {
			var out LifecycleOutcome
			err := db.Update(ctx, "s", func(tx store.Tx) error {
				i := src
				i.RequestID = legacyID
				var err error
				out, err = s.Archive(legacyReceiptTx{Tx: tx, legacyID: legacyID, receipt: receipt}, p, i, 0)
				return err
			})
			return out, err
		}
		if out, err := archive(owner); err != nil || out.Result.AuditID != first.AuditID {
			t.Fatalf("owner's legacy receipt did not replay: %+v %v", out, err)
		}
		foreign := owner
		foreign.AgentID = "intruder"
		if _, err := archive(foreign); err == nil || errors.Is(err, domain.ErrEventIDConflict) {
			t.Fatalf("foreign principal saw the legacy receipt: %v", err)
		}
	})
}
