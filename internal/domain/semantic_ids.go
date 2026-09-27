package domain

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
func MutationReceiptID(session string, family MutationFamily, requestID string) (string, error) {
	if !semanticID(session) || !family.Valid() || !semanticID(requestID) {
		return "", invalid("receipt identity: exact session/family/request required")
	}
	return "mut_" + shortHash(NewCanonicalEncoder("context-runtime/mutation-receipt-id/v1").String(session).String(string(family)).String(requestID)), nil
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

func OperationRequestID(session, occurrence string, operation, command uint64) (string, error) {
	if !semanticID(session) || !ValidOccurrenceID(occurrence) {
		return "", invalid("operation request: occurrence required")
	}
	return operationRequestPrefix + shortHash(NewCanonicalEncoder("context-runtime/operation-request-id/v1").String(session).String(occurrence).Uint(operation).Uint(command)), nil
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
