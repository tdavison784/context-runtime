package domain

import "strings"

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

// MutationReceiptID is the receipt identity of (session, family, requestID).
// It takes the authenticated principal that owns the receipt: a request ID
// in the runtime req_ namespace is accepted only if it was derived for that
// exact principal, and is rejected before any receipt lookup otherwise. A
// foreign principal presenting another's derived ID therefore learns
// nothing and can never occupy it (G3, SEC-1.2). Receipt ID values are
// unchanged by the principal binding.
func MutationReceiptID(p Principal, family MutationFamily, requestID string) (string, error) {
	if err := validateIngestPrincipal(p); err != nil {
		return "", err
	}
	if !family.Valid() || !semanticID(requestID) {
		return "", invalid("receipt identity: exact session/family/request required")
	}
	if strings.HasPrefix(requestID, operationRequestPrefix) && !runtimeRequestIDFor(p, requestID) {
		return "", invalid("receipt identity: runtime request ID not derived for this principal")
	}
	return "mut_" + shortHash(NewCanonicalEncoder("context-runtime/mutation-receipt-id/v1").String(p.SessionID).String(string(family)).String(requestID)), nil
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
// command of an event occurrence, owned by principal p (the principal whose
// mutation receipt it names). The ID is req_<inner>.<tag>: inner binds the
// principal, occurrence and ordinals; tag binds inner to the principal, so
// MutationReceiptID can verify ownership without a secret (G3, SEC-1.2).
func OperationRequestID(p Principal, occurrence string, operation, command uint64) (string, error) {
	if err := validateIngestPrincipal(p); err != nil {
		return "", err
	}
	if !ValidOccurrenceID(occurrence) {
		return "", invalid("operation request: occurrence required")
	}
	e := NewCanonicalEncoder("context-runtime/operation-request-id/v2")
	encodePrincipal(e, p)
	inner := shortHash(e.String(occurrence).Uint(operation).Uint(command))
	return operationRequestPrefix + inner + "." + operationRequestTag(p, inner), nil
}

func operationRequestTag(p Principal, inner string) string {
	e := NewCanonicalEncoder("context-runtime/operation-request-binding/v1")
	encodePrincipal(e, p)
	return shortHash(e.String(inner))
}

// runtimeRequestIDFor reports whether id is a runtime request ID derived
// for exactly p.
func runtimeRequestIDFor(p Principal, id string) bool {
	body, ok := strings.CutPrefix(id, operationRequestPrefix)
	if !ok {
		return false
	}
	inner, tag, ok := strings.Cut(body, ".")
	return ok && len(inner) == 32 && isLowerHex(inner) && tag == operationRequestTag(p, inner)
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
