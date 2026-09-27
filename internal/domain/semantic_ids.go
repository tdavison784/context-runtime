package domain

import (
	"strconv"
	"strings"
)

func (m SemanticMeta) SemanticSeq() uint64 { return m.Seq }
func (m SemanticMeta) Clone() SemanticMeta { return m }

const (
	MutationObligationDeclare         MutationFamily = "obligation.declare"
	MutationObligationTransition      MutationFamily = "obligation.transition"
	MutationObligationMaterialization MutationFamily = "obligation.materialization"
	MutationObligationReevaluate      MutationFamily = "obligation.reevaluate"
	MutationWorkspaceBind             MutationFamily = "workspace.bind"
	MutationResourceRegister          MutationFamily = "resource.register"
	MutationResourceReport            MutationFamily = "resource.report"
	MutationResourceResync            MutationFamily = "resource.resync"
	MutationObservationRun            MutationFamily = "observation.run"
	MutationObservationReport         MutationFamily = "observation.report"
)

func (f MutationFamily) HashDomain() string {
	switch f {
	case MutationObligationDeclare, MutationObligationTransition, MutationObligationMaterialization, MutationObligationReevaluate, MutationWorkspaceBind, MutationResourceRegister, MutationResourceReport, MutationResourceResync, MutationObservationRun, MutationObservationReport:
		return "context-runtime/w4/" + string(f) + "/v1"
	case MutationLifecycle, MutationObligation, MutationGrantFamily, MutationResource, MutationTool, MutationRetrieval, MutationCollection, MutationMembership:
		return "context-runtime/mutation-request/v3"
	}
	return ""
}

// SeqAllocator reports whether seq was allocated in the current
// transaction; store.Tx satisfies it.
type SeqAllocator interface{ Allocated(seq uint64) bool }

// MutationReceiptKey is the pure receipt identity of (session, family,
// requestID), for stores deriving the key of a record they hold. Services
// derive it through MutationReceiptID, which also proves a runtime request
// is theirs to use.
func MutationReceiptKey(session string, family MutationFamily, requestID string) (string, error) {
	if !semanticID(session) || !family.Valid() || !semanticID(requestID) {
		return "", invalid("receipt identity: exact session/family/request required")
	}
	return "mut_" + shortHash(NewCanonicalEncoder("context-runtime/mutation-receipt-id/v1").String(session).String(string(family)).String(requestID)), nil
}

// MutationReceiptID is the receipt identity of (session, family, requestID)
// for owner, the authenticated principal whose receipt it is. It is checked
// before any receipt lookup. A request ID in the runtime req_ namespace is
// accepted only if it was derived for owner at an event sequence allocated
// in this very transaction (H5, SEC-2.2): no caller can name another
// principal's, a future, or a past runtime request, so there is neither an
// oracle nor a squat. Receipt ID values are unchanged.
func MutationReceiptID(tx SeqAllocator, owner Principal, family MutationFamily, requestID string) (string, error) {
	if err := validateIngestPrincipal(owner); err != nil {
		return "", err
	}
	if err := RuntimeRequestOwnedBy(owner, requestID); err != nil {
		return "", err
	}
	if seq, ok := runtimeRequestSeq(requestID); ok && (tx == nil || !tx.Allocated(seq)) {
		return "", invalid("receipt identity: runtime request ID is not this transaction's")
	}
	return MutationReceiptKey(owner.SessionID, family, requestID)
}

// RuntimeRequestOwnedBy fails when requestID is in the runtime req_
// namespace but was not derived for owner; any other ID passes. Stores use
// it as a commit-time check on the receipts they hold.
func RuntimeRequestOwnedBy(owner Principal, requestID string) error {
	if strings.HasPrefix(requestID, operationRequestPrefix) && !runtimeRequestIDFor(owner, requestID) {
		return invalid("receipt identity: runtime request ID not derived for this principal")
	}
	return nil
}

func ApplicabilityProofID(target ObligationRef, transitionID string) (string, error) {
	if err := target.Validate(); err != nil {
		return "", err
	}
	if !semanticID(transitionID) {
		return "", invalid("proof identity: satisfying transition required")
	}
	return "proof_" + shortHash(NewCanonicalEncoder("context-runtime/proof-id/v1").String(target.Target().AuthorizationKey).String(transitionID)), nil
}

const operationRequestPrefix = "req_"

// OperationRequestID derives the runtime request ID of one operation or
// command of an event occurrence (H5, SEC-2.2). authenticated is the
// principal that ingested the event; owner is the principal whose mutation
// receipt the request names (the lowered source actor or the dispatcher);
// eventSeq is the event's own sequence, allocated in the ingesting
// transaction. The ID is req_<eventSeq>_<inner>.<tag>: inner binds both
// principals, the occurrence, the event sequence and the ordinals, and tag
// binds eventSeq and inner to owner, so MutationReceiptID can verify both
// ownership and that the request belongs to the current transaction.
func OperationRequestID(authenticated, owner Principal, occurrence string, eventSeq, operation, command uint64) (string, error) {
	if err := validateIngestPrincipal(authenticated); err != nil {
		return "", err
	}
	if err := validateIngestPrincipal(owner); err != nil {
		return "", err
	}
	if !ValidOccurrenceID(occurrence) || eventSeq == 0 || authenticated.SessionID != owner.SessionID {
		return "", invalid("operation request: occurrence, event sequence and one session required")
	}
	e := NewCanonicalEncoder("context-runtime/operation-request-id/v3")
	encodePrincipal(e, authenticated)
	encodePrincipal(e, owner)
	inner := shortHash(e.String(occurrence).Uint(eventSeq).Uint(operation).Uint(command))
	return operationRequestPrefix + strconv.FormatUint(eventSeq, 10) + "_" + inner + "." + operationRequestTag(owner, eventSeq, inner), nil
}

func operationRequestTag(owner Principal, eventSeq uint64, inner string) string {
	e := NewCanonicalEncoder("context-runtime/operation-request-binding/v2")
	encodePrincipal(e, owner)
	return shortHash(e.Uint(eventSeq).String(inner))
}

// runtimeRequestParts splits req_<seq>_<inner>.<tag>.
func runtimeRequestParts(id string) (seq uint64, inner, tag string, ok bool) {
	body, ok := strings.CutPrefix(id, operationRequestPrefix)
	if !ok {
		return 0, "", "", false
	}
	num, rest, ok := strings.Cut(body, "_")
	if !ok {
		return 0, "", "", false
	}
	seq, err := strconv.ParseUint(num, 10, 64)
	if err != nil || seq == 0 || strconv.FormatUint(seq, 10) != num {
		return 0, "", "", false
	}
	inner, tag, ok = strings.Cut(rest, ".")
	if !ok || len(inner) != 32 || !isLowerHex(inner) {
		return 0, "", "", false
	}
	return seq, inner, tag, true
}

// runtimeRequestSeq is the event sequence a well-formed runtime request ID
// is bound to.
func runtimeRequestSeq(id string) (uint64, bool) {
	seq, _, _, ok := runtimeRequestParts(id)
	return seq, ok
}

// runtimeRequestIDFor reports whether id is a runtime request ID derived
// for exactly owner.
func runtimeRequestIDFor(owner Principal, id string) bool {
	seq, inner, tag, ok := runtimeRequestParts(id)
	return ok && tag == operationRequestTag(owner, seq, inner)
}

// These records contain value fields only. Explicit Clone methods give stores
// a uniform contract without special-casing scalar record families (W2-C1).
func (r CoverageRecord) Clone() CoverageRecord                           { return r }
func (r LogicalExchange) Clone() LogicalExchange                         { return r }
func (r ExchangeMember) Clone() ExchangeMember                           { return r }
func (r ExchangeAcknowledgment) Clone() ExchangeAcknowledgment           { return r }
func (r ConversationMembershipState) Clone() ConversationMembershipState { return r }
func (r AdmissionManifest) Clone() AdmissionManifest                     { return r }
func (r Checkpoint) Clone() Checkpoint                                   { return r }
func (r OwnerRegistration) Clone() OwnerRegistration                     { return r }
func (r ResourceBinding) Clone() ResourceBinding                         { return r }
func (r ResourceState) Clone() ResourceState                             { return r }
func (r WorkspaceBinding) Clone() WorkspaceBinding                       { return r }
func (r SubjectState) Clone() SubjectState                               { return r }
func (r ObservationRecord) Clone() ObservationRecord                     { return r }
func (r ResourcePathState) Clone() ResourcePathState                     { return r }
func (r AssertionRecord) Clone() AssertionRecord                         { return r }
func (r RetrievalLease) Clone() RetrievalLease                           { return r }
func (r GCRequest) Clone() GCRequest                                     { return r }
func (r GCResult) Clone() GCResult                                       { return r }
func (r SemanticChange) Clone() SemanticChange                           { return r }
