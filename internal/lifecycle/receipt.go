package lifecycle

import (
	"errors"
	"math"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// A committed request is checked under a bound derived from its recorded size.
// The frozen encoder reserves conservative per-field/set-member overhead, so
// twice the encoded size plus framing room is needed even for an exact retry.
// Tightening
// current policy must not reject exact retries or disclose another principal's
// result. Oversized conflicting requests stop encoding at the stored bound.
func checkReplay(r domain.MutationReceipt, p domain.Principal, family domain.MutationFamily, method string, intent any) error {
	if r.Principal != p || r.Family != family || r.CanonicalMethod != method {
		return domain.ErrEventIDConflict
	}
	if len(r.CanonicalArguments) > (math.MaxInt-256)/2 {
		return domain.ErrIntegrity
	}
	args, err := domain.CanonicalSemanticArguments(intent, 2*len(r.CanonicalArguments)+256)
	if err != nil {
		return domain.ErrEventIDConflict
	}
	return r.CheckReplay(p, family, method, args)
}

func (s *Service) begin(tx store.Tx, p domain.Principal, family domain.MutationFamily, method, requestID string, intent any) (store.SemanticTx, []byte, *domain.MutationReceipt, error) {
	// The frozen request encoder validates bounded authenticated owner IDs
	// before hashing. Validate the header before any persistence lookup or effect;
	// the real argument hash is generated only when committing a fresh receipt.
	if _, err := domain.MutationRequestHash(p, family, method, []byte{0}); err != nil {
		return nil, nil, nil, err
	}
	if p.SessionID != tx.SessionID() {
		return nil, nil, nil, domain.ErrNotFound
	}
	if _, err := domain.MutationReceiptID(p.SessionID, family, requestID); err != nil {
		return nil, nil, nil, err
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return nil, nil, nil, err
	}
	r, err := sem.MutationReceipt(family, requestID)
	if err == nil {
		if err := checkReplay(r, p, family, method, intent); err != nil {
			return nil, nil, nil, err
		}
		r = r.Clone()
		return sem, nil, &r, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return nil, nil, nil, err
	}
	args, err := domain.CanonicalSemanticArguments(intent, s.policy.MaxMetadataBytes)
	return sem, args, nil, err
}

func (s *Service) finish(tx store.Tx, sem store.SemanticTx, p domain.Principal, family domain.MutationFamily, method, requestID string, args []byte, result domain.MutationResult) error {
	id, err := domain.MutationReceiptID(p.SessionID, family, requestID)
	if err != nil {
		return err
	}
	hash, err := domain.MutationRequestHash(p, family, method, args)
	if err != nil {
		return err
	}
	r := domain.MutationReceipt{SemanticMeta: domain.SemanticMeta{ID: id, SessionID: p.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: tx.NextSeq()},
		Family: family, RequestID: requestID, Principal: p, CanonicalMethod: method, CanonicalArguments: args,
		RequestHashVersion: domain.RequestHashV3, RequestHash: hash, PolicyVersion: s.policy.Version, Result: result}
	if _, err := domain.CanonicalSemanticArguments(r, s.policy.MaxReceiptBytes); err != nil {
		return err
	}
	return sem.InsertMutationReceipt(r)
}
