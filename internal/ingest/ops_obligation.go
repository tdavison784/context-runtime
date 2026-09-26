package ingest

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/store"
)

// W4 operation adapters (P3-12..23 through P3-34). Each typed operation of a
// W4 family runs the obligation service's transaction method as the
// operation's source actor at its allocated sequence; the service writes and
// replays its own mutation receipt, which ingest names by the family's
// canonical receipt ID.

// obligationOp is one W4 operation kind: its receipt family, the service
// call, and what it creates for later aliases.
type obligationOp struct {
	family  domain.MutationFamily
	run     func(s *obligation.Service, tx store.Tx, actor domain.Principal, op domain.SemanticOperation, seq uint64) (domain.MutationResult, error)
	created func(tx store.Tx, op domain.SemanticOperation, res domain.MutationResult) (Created, error)
}

var obligationOps = map[domain.SemanticOperationKind]obligationOp{
	domain.OperationDeclareObligation: {domain.MutationObligationDeclare, func(s *obligation.Service, tx store.Tx, a domain.Principal, op domain.SemanticOperation, seq uint64) (domain.MutationResult, error) {
		return s.DeclareObligationTx(tx, a, *op.DeclareObligation, seq)
	}, declaredObligation},
	domain.OperationTransition: {domain.MutationObligationTransition, func(s *obligation.Service, tx store.Tx, a domain.Principal, op domain.SemanticOperation, seq uint64) (domain.MutationResult, error) {
		return s.ApplyTransitionTx(tx, a, *op.Transition, seq)
	}, nil},
	domain.OperationMaterialization: {domain.MutationObligationMaterialization, func(s *obligation.Service, tx store.Tx, a domain.Principal, op domain.SemanticOperation, seq uint64) (domain.MutationResult, error) {
		return s.SetMaterializationTx(tx, a, *op.Materialization, seq)
	}, nil},
	domain.OperationReevaluate: {domain.MutationObligationReevaluate, func(s *obligation.Service, tx store.Tx, a domain.Principal, op domain.SemanticOperation, seq uint64) (domain.MutationResult, error) {
		return s.ReevaluateTx(tx, a, *op.Reevaluate, seq)
	}, nil},
	domain.OperationRegisterResource: {domain.MutationResourceRegister, func(s *obligation.Service, tx store.Tx, a domain.Principal, op domain.SemanticOperation, seq uint64) (domain.MutationResult, error) {
		return s.RegisterResourceTx(tx, a, *op.RegisterResource, seq)
	}, nil},
	domain.OperationReportResource: {domain.MutationResourceReport, func(s *obligation.Service, tx store.Tx, a domain.Principal, op domain.SemanticOperation, seq uint64) (domain.MutationResult, error) {
		return s.ReportResourceChangeTx(tx, a, *op.ReportResource, seq)
	}, nil},
	domain.OperationWorkspace: {domain.MutationWorkspaceBind, func(s *obligation.Service, tx store.Tx, a domain.Principal, op domain.SemanticOperation, seq uint64) (domain.MutationResult, error) {
		return s.BindWorkspaceTx(tx, a, *op.Workspace, seq)
	}, func(_ store.Tx, op domain.SemanticOperation, _ domain.MutationResult) (Created, error) {
		return Created{Workspace: &domain.WorkspaceBindingRef{ID: op.Workspace.BindingID, Version: op.Workspace.Version}}, nil
	}},
	domain.OperationRegisterRun: {domain.MutationObservationRun, func(s *obligation.Service, tx store.Tx, a domain.Principal, op domain.SemanticOperation, seq uint64) (domain.MutationResult, error) {
		return s.RegisterRunTx(tx, a, *op.RegisterRun, seq)
	}, func(_ store.Tx, _ domain.SemanticOperation, res domain.MutationResult) (Created, error) {
		if res.Records == nil || len(res.Records.IDs) != 1 {
			return Created{}, domain.ErrInvalidRecord
		}
		return Created{RunID: res.Records.IDs[0]}, nil
	}},
	domain.OperationObservation: {domain.MutationObservationReport, func(s *obligation.Service, tx store.Tx, a domain.Principal, op domain.SemanticOperation, seq uint64) (domain.MutationResult, error) {
		return s.ReportObservationTx(tx, a, *op.Observation, seq)
	}, nil},
}

// declaredObligation is the exact current version a HARNESS declaration
// created for its source and slot, read back from the store.
func declaredObligation(tx store.Tx, op domain.SemanticOperation, _ domain.MutationResult) (Created, error) {
	in := op.DeclareObligation
	obs, err := tx.ObligationsBySource(in.SourceItemID, 2*maxDeclarationSlots)
	if err != nil {
		return Created{}, err
	}
	for _, o := range obs {
		if o.Current && o.DeclarationSlot == in.DeclarationSlot {
			return Created{Obligations: []ObligationVersion{{domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version}, o.Revision}}}, nil
		}
	}
	return Created{}, domain.ErrInvalidRecord
}

// maxDeclarationSlots bounds one source's declaration slots (the Pinned slot
// plus W4's HARNESS slots).
const maxDeclarationSlots = 1025

// obligationHandler adapts one W4 kind to OperationHandler.
type obligationHandler struct {
	svc *obligation.Service
	op  obligationOp
}

func (h obligationHandler) Execute(tx store.Tx, actor domain.Principal, op domain.SemanticOperation, seq uint64) (OperationOutcome, error) {
	res, err := h.op.run(h.svc, tx, actor, op, seq)
	if err != nil {
		return OperationOutcome{}, err
	}
	id, err := domain.MutationReceiptID(actor.SessionID, h.op.family, requestIDOf(op))
	if err != nil {
		return OperationOutcome{}, err
	}
	out := OperationOutcome{MutationReceiptID: id, Result: res, Access: ownBoundary(actor)}
	if h.op.created != nil {
		if out.Created, err = h.op.created(tx, op, res); err != nil {
			return OperationOutcome{}, err
		}
	}
	return out, nil
}

// ownBoundary is the narrowest boundary of p's own owners: a W4 result is
// readable only by the principal that performed it (fail closed); detailed
// authorized inspection goes through the services' access-filtered reads.
func ownBoundary(p domain.Principal) domain.AccessBoundary {
	scope := domain.ScopeSession
	switch {
	case p.AgentID != "":
		scope = domain.ScopeAgent
	case p.TaskID != "":
		scope = domain.ScopeTask
	case p.WorkflowID != "":
		scope = domain.ScopeWorkflow
	}
	return domain.AccessBoundary{Scope: scope, SessionID: p.SessionID, WorkflowID: p.WorkflowID, TaskID: p.TaskID, AgentID: p.AgentID}
}

// requestIDOf is a resolved operation's ingestion-derived request ID.
func requestIDOf(op domain.SemanticOperation) string {
	switch {
	case op.Grant != nil:
		return op.Grant.RequestID
	case op.RevokeGrant != nil:
		return op.RevokeGrant.RequestID
	case op.Transition != nil:
		return op.Transition.RequestID
	case op.DeclareObligation != nil:
		return op.DeclareObligation.RequestID
	case op.Materialization != nil:
		return op.Materialization.RequestID
	case op.Reevaluate != nil:
		return op.Reevaluate.RequestID
	case op.RegisterResource != nil:
		return op.RegisterResource.RequestID
	case op.ReportResource != nil:
		return op.ReportResource.RequestID
	case op.Workspace != nil:
		return op.Workspace.RequestID
	case op.RegisterRun != nil:
		return op.RegisterRun.RequestID
	case op.Observation != nil:
		return op.Observation.RequestID
	case op.Checkpoint != nil:
		return op.Checkpoint.RequestID
	case op.Replace != nil:
		return op.Replace.RequestID
	}
	return ""
}
