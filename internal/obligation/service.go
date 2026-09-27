package obligation

import (
	"errors"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/store"
)

// ObservationStateRule names the registered rule that derives current-state
// items from typed observations (P3-22, FR-REL-007).
const ObservationStateRule = "obs-state/1"

// Service runs obligation, resource, and observation mutations. It holds only
// immutable configuration: every method takes the caller's transaction and
// authenticated principal, and every *Tx method runs inside that transaction
// without reentering the store (P3-1).
type Service struct {
	policy domain.Phase3Policy
	reg    *Registry
}

// New validates the policy manifest against the rules this build implements.
// A policy naming another claim, matcher, or observation-state version is
// refused rather than silently reinterpreted (P3-42).
func New(policy domain.Phase3Policy, reg *Registry) (*Service, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	if reg == nil || policy.Claim != ClaimPatternVersion || policy.Matcher != MatcherRegistryVersion || policy.ObservationState != ObservationStateRule {
		return nil, domain.ErrUnsupportedSchema
	}
	return &Service{policy: policy.Clone(), reg: reg}, nil // no aliasing of caller slices
}

// request is one mutation's stable identity and canonical arguments (P3-2).
type request struct {
	family domain.MutationFamily
	id     string
	method string
	args   []byte
}

func (s *Service) newRequest(family domain.MutationFamily, id, method string, intent any) (request, error) {
	args, err := domain.CanonicalSemanticArguments(intent, s.policy.MaxReceiptBytes)
	if err != nil {
		return request{}, err
	}
	return request{family: family, id: id, method: method, args: args}, nil
}

// replay returns the committed result of a known request before any current
// state is consulted. A different principal, method, or argument set under
// the same identity is ErrEventIDConflict; replay never recomputes an effect.
func replay(r store.SemanticReader, actor domain.Principal, req request) (domain.MutationResult, bool, error) {
	rec, err := r.MutationReceipt(req.family, req.id)
	if errors.Is(err, domain.ErrNotFound) {
		return domain.MutationResult{}, false, nil
	}
	if err != nil {
		return domain.MutationResult{}, false, err
	}
	if err := rec.CheckReplay(actor, req.family, req.method, req.args); err != nil {
		return domain.MutationResult{}, false, domain.ErrEventIDConflict
	}
	return rec.Result.Clone(), true, nil
}

// recordReceipt stores the immutable receipt of a committed mutation.
func (s *Service) recordReceipt(sem store.SemanticTx, actor domain.Principal, req request, seq uint64, result domain.MutationResult) error {
	id, err := domain.MutationReceiptID(actor, req.family, req.id)
	if err != nil {
		return err
	}
	hash, err := domain.MutationRequestHash(actor, req.family, req.method, req.args)
	if err != nil {
		return err
	}
	return sem.InsertMutationReceipt(domain.MutationReceipt{
		SemanticMeta:       domain.SemanticMeta{ID: id, SessionID: actor.SessionID, SchemaVersion: domain.SemanticSchemaV1, Seq: seq},
		Family:             req.family,
		RequestID:          req.id,
		Principal:          actor,
		CanonicalMethod:    req.method,
		CanonicalArguments: req.args,
		RequestHashVersion: domain.RequestHashV3,
		RequestHash:        hash,
		PolicyVersion:      s.policy.Version,
		Result:             result,
	})
}

// begin opens a transaction-scoped mutation: it validates the actor, binds the
// semantic facet, and checks the allocated sequence. Replay is the caller's
// next step, before any state is read.
func begin(tx store.Tx, actor domain.Principal, seq uint64) (store.SemanticTx, error) {
	if err := actor.Validate(); err != nil {
		return nil, err
	}
	if actor.SessionID != tx.SessionID() {
		return nil, domain.ErrNotFound
	}
	sem, err := store.Semantic(tx)
	if err != nil {
		return nil, err
	}
	if seq == 0 || !tx.Allocated(seq) {
		return nil, domain.ErrInvalidRecord
	}
	return sem, nil
}

// writes poisons the transaction on the first error after a constituent write
// (P3-1), so a caller ignoring the error cannot commit a partial effect.
type writes struct {
	tx      store.Tx
	started bool
}

func (w *writes) start() { w.started = true }

func (w *writes) fail(err error) error {
	if err != nil && w.started {
		w.tx.Poison(err)
	}
	return err
}
