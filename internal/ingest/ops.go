package ingest

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/lifecycle"
	"github.com/tdavison784/context-runtime/internal/obligation"
	"github.com/tdavison784/context-runtime/internal/store"
)

// OperationHandler executes one resolved typed operation of an event's
// ordered stream inside the ingestion transaction (P3-34). actor is the
// operation's authenticated source actor; seq is the operation's own
// sequence, already allocated in tx, at which the handler must authorize
// (P3-1). The operation's References are resolved and its RequestID is the
// ingestion-derived domain.OperationRequestID. A handler never opens or
// reenters a store transaction; ingest poisons tx if it fails.
type OperationHandler interface {
	Execute(tx store.Tx, actor domain.Principal, op domain.SemanticOperation, seq uint64) (OperationOutcome, error)
}

// OperationOutcome is a handler's immutable result: the mutation receipt it
// committed, its frozen typed result, the boundary at which the result may
// be read, and what it created that later operations may alias.
type OperationOutcome struct {
	MutationReceiptID string
	Result            domain.MutationResult
	Access            domain.AccessBoundary
	Created           Created
}

// LifecycleExecutor executes a parsed Resolve or Unpin of a new Phase 3
// event (P3-35) inside the ingestion transaction: the lifecycle service
// implements it. It authorizes actor at seq, which ingest has allocated,
// applies the change with CAS on intent.ExpectedVersion, writes its audit
// and mutation receipt, and returns the frozen result.
type LifecycleExecutor interface {
	Resolve(tx store.Tx, actor domain.Principal, intent domain.ResolveIntent, seq uint64) (LifecycleOutcome, error)
	Unpin(tx store.Tx, actor domain.Principal, intent domain.UnpinIntent, seq uint64) (LifecycleOutcome, error)
}

// LifecycleOutcome is an executed command's committed receipt, the grant
// that authorized it if any, and its frozen item result: the lifecycle
// service's own type, so *lifecycle.Service is a LifecycleExecutor.
type LifecycleOutcome = lifecycle.LifecycleOutcome

var _ LifecycleExecutor = (*lifecycle.Service)(nil)

// GrantExecutor issues and revokes grants for GRANT/REVOKE_GRANT operations
// (P3-11): the lifecycle service derives issuer, session and sequence from
// the authenticated actor and the allocated seq. *lifecycle.Service
// implements it; a LifecycleExecutor that does not leaves grants
// fail-closed.
type GrantExecutor interface {
	IssueGrant(tx store.Tx, actor domain.Principal, intent domain.GrantIntent, seq uint64) (lifecycle.MutationOutcome, error)
	RevokeGrant(tx store.Tx, actor domain.Principal, intent domain.RevokeGrantIntent, seq uint64) (lifecycle.MutationOutcome, error)
}

var _ GrantExecutor = (*lifecycle.Service)(nil)

// grantHandler adapts a GrantExecutor to OperationHandler. The issued or
// revoked grant ID is aliasable (GRANT_ID); the result is readable only at
// the issuing principal's own boundary.
type grantHandler struct{ g GrantExecutor }

func (h grantHandler) Execute(tx store.Tx, actor domain.Principal, op domain.SemanticOperation, seq uint64) (OperationOutcome, error) {
	var out lifecycle.MutationOutcome
	var err error
	if op.Grant != nil {
		out, err = h.g.IssueGrant(tx, actor, *op.Grant, seq)
	} else {
		out, err = h.g.RevokeGrant(tx, actor, *op.RevokeGrant, seq)
	}
	if err != nil {
		return OperationOutcome{}, err
	}
	res := OperationOutcome{MutationReceiptID: out.MutationReceiptID, Result: out.Result, Access: ownBoundary(actor)}
	if op.Grant != nil {
		res.Created.GrantID = op.Grant.GrantID
	}
	return res, nil
}

// ReplaceExecutor executes the explicit typed directive replacement
// (C-1, P3-4): CAS on the expected current occurrence and version, which
// may reuse identical content and starts a new version from the prior's
// creation defaults. *lifecycle.Service implements it.
type ReplaceExecutor interface {
	ReplaceDirective(tx store.Tx, actor domain.Principal, intent domain.ReplaceDirectiveIntent, seq uint64) (lifecycle.MutationOutcome, error)
}

var _ ReplaceExecutor = (*lifecycle.Service)(nil)

// replaceHandler adapts a ReplaceExecutor; an alias of it names the new
// occurrence (the result's first record) at version 1.
type replaceHandler struct{ x ReplaceExecutor }

func (h replaceHandler) Execute(tx store.Tx, actor domain.Principal, op domain.SemanticOperation, seq uint64) (OperationOutcome, error) {
	out, err := h.x.ReplaceDirective(tx, actor, *op.Replace, seq)
	if err != nil {
		return OperationOutcome{}, err
	}
	if out.Result.Records == nil || len(out.Result.Records.IDs) == 0 {
		return OperationOutcome{}, domain.ErrInvalidRecord
	}
	return OperationOutcome{MutationReceiptID: out.MutationReceiptID, Result: out.Result, Access: ownBoundary(actor),
		Created: Created{Items: []ItemVersion{{ID: out.Result.Records.IDs[0], Version: 1}}}}, nil
}

// ItemVersion is an exact item occurrence at a version.
type ItemVersion struct {
	ID      string
	Version uint64
}

// ObligationVersion is an exact obligation version at a revision.
type ObligationVersion struct {
	Ref      domain.ObligationRef
	Revision uint64
}

// Created is what one operation created, the only values an alias of it
// can bind (domain.OperationReference). A caller-supplied ID is never one.
type Created struct {
	Items       []ItemVersion // semantic items (a span: its non-transcript items)
	Evidence    *ItemVersion  // a span's transcript occurrence
	Obligations []ObligationVersion
	GrantID     string
	Workspace   *domain.WorkspaceBindingRef
	RunID       string
}

// typedOperation executes the oi-th, non-span operation: it resolves its
// aliases, derives its request identity, allocates its sequence, and runs
// its handler as the operation's source actor.
func (r *run) typedOperation(oi int, op domain.SemanticOperation) error {
	h, err := r.handler(op.Kind)
	if err != nil {
		return err
	}
	actor, err := r.operationActor(op)
	if err != nil {
		return err
	}
	// Bind a private copy: the event's own operations are the hashed,
	// persisted request and must never change.
	op, err = r.resolveAliases(op.Clone())
	if err != nil {
		return err
	}
	req, err := domain.OperationRequestID(r.p.SessionID, r.occurrence, uint64(oi), 0)
	if err != nil {
		return err
	}
	setRequestID(&op, req)
	if err := op.ValidateResolved(); err != nil {
		return err
	}
	out, err := h.Execute(r.tx, actor, op, r.tx.NextSeq())
	if err != nil {
		return err
	}
	res := out.Result.Clone()
	result := domain.OperationResult{Index: oi, Kind: op.Kind, Alias: op.Alias, MutationReceiptID: out.MutationReceiptID, Access: out.Access, Result: &res}
	if err := result.Validate(); err != nil || out.Access.SessionID != r.p.SessionID {
		return domain.ErrInvalidRecord
	}
	r.opResults = append(r.opResults, result)
	r.mutationReceipts = append(r.mutationReceipts, out.MutationReceiptID)
	r.alias(op.Alias, out.Created)
	return nil
}

// handler is the executor of kind: a configured Ingester.Operations entry,
// else W3's replacement executor for REPLACE, its grant executor for
// GRANT/REVOKE_GRANT, or the obligation
// service's adapter for a W4 kind; anything else fails closed.
func (r *run) handler(kind domain.SemanticOperationKind) (OperationHandler, error) {
	if h, ok := r.g.Operations[kind]; ok && h != nil {
		return h, nil
	}
	if kind == domain.OperationReplace {
		x, ok := r.g.Lifecycle.(ReplaceExecutor)
		if !ok {
			return nil, domain.ErrUnsupportedSchema
		}
		return replaceHandler{x}, nil
	}
	if kind == domain.OperationGrant || kind == domain.OperationRevokeGrant {
		g, ok := r.g.Lifecycle.(GrantExecutor)
		if !ok {
			return nil, domain.ErrUnsupportedSchema
		}
		return grantHandler{g}, nil
	}
	op, ok := obligationOps[kind]
	if !ok {
		return nil, domain.ErrUnsupportedSchema
	}
	svc, err := r.obligations()
	if err != nil {
		return nil, err
	}
	return obligationHandler{svc, op}, nil
}

// obligations is the obligation service for this run: the configured one,
// else one built from the run's recorded policy and the frozen registry.
func (r *run) obligations() (*obligation.Service, error) {
	if r.obl != nil {
		return r.obl, nil
	}
	if r.pol == nil {
		return nil, domain.ErrUnsupportedSchema
	}
	svc := r.g.Obligations
	if svc == nil {
		var err error
		if svc, err = obligation.New(*r.pol, obligation.DefaultRegistry()); err != nil {
			return nil, err
		}
	}
	r.obl = svc
	return svc, nil
}

// operationActor is the principal an operation runs as: the source actor
// of the earlier span it names, or of the envelope. It never exceeds the
// caller (D15): a USER span inside a SYSTEM event lends only USER.
func (r *run) operationActor(op domain.SemanticOperation) (domain.Principal, error) {
	a := r.e.Kind.Authority()
	if op.SourceSpanIndex != nil {
		si := *op.SourceSpanIndex
		if si < 0 || si >= len(r.e.Spans) {
			return domain.Principal{}, domain.ErrInvalidRecord
		}
		if _, done := r.transcripts[si]; !done {
			return domain.Principal{}, domain.ErrInvalidRecord
		}
		a = r.e.Spans[si].Authority
	}
	return domain.SourceActor(r.p, a)
}

// alias records what an aliased operation created.
func (r *run) alias(name string, c Created) {
	if name == "" {
		return
	}
	if r.created == nil {
		r.created = map[string]Created{}
	}
	r.created[name] = c
}

// spanCreated is what span si created, for an alias of its span operation:
// its non-transcript items at their created versions, its transcript as
// evidence, and the obligations declared by those items.
func (r *run) spanCreated(from int) (Created, error) {
	var c Created
	for _, it := range r.items[from:] {
		if it.Role == domain.RoleTranscript {
			if c.Evidence == nil {
				c.Evidence = &ItemVersion{it.ID, it.Version}
			}
			continue
		}
		c.Items = append(c.Items, ItemVersion{it.ID, it.Version})
		obs, err := r.tx.ObligationsBySource(it.ID, r.lookupLimit())
		if err != nil {
			return Created{}, err
		}
		for _, o := range obs {
			c.Obligations = append(c.Obligations, ObligationVersion{domain.ObligationRef{SessionID: o.SessionID, ObligationID: o.ObligationID, Version: o.Version}, o.Revision})
		}
	}
	return c, nil
}

func (r *run) lookupLimit() int { return r.g.lookupLimit() }

// resolveAliases binds every reference of op to the exact value an earlier
// operation of this event created, rejecting a literal already in the slot
// and any alias that does not name exactly one value of the slot's type.
func (r *run) resolveAliases(op domain.SemanticOperation) (domain.SemanticOperation, error) {
	for _, ref := range op.References {
		c, ok := r.created[ref.Alias]
		if !ok {
			return op, domain.ErrInvalidRecord
		}
		if !bindReference(r.p.SessionID, &op, ref, c) {
			return op, domain.ErrInvalidRecord
		}
	}
	op.References = nil
	return op, nil
}

func one[T any](vs []T) (T, bool) {
	var zero T
	if len(vs) != 1 {
		return zero, false
	}
	return vs[0], true
}

// bindReference sets ref's slot in op from c. Every slot must be empty in
// the literal operation: an alias never overrides a caller value.
func bindReference(session string, op *domain.SemanticOperation, ref domain.OperationReference, c Created) bool {
	item, oneItem := one(c.Items)
	ob, oneOb := one(c.Obligations)
	switch ref.Slot {
	case domain.OperationSourceItem:
		switch {
		case !oneItem:
			return false
		case op.DeclareObligation != nil && op.DeclareObligation.SourceItemID == "" && op.DeclareObligation.ExpectedSourceVersion == 0:
			op.DeclareObligation.SourceItemID, op.DeclareObligation.ExpectedSourceVersion = item.ID, item.Version
		case op.Workspace != nil && op.Workspace.SourceItemID == "":
			op.Workspace.SourceItemID = item.ID
		default:
			return false
		}
	case domain.OperationExpectedItem:
		if !oneItem || op.Replace == nil || op.Replace.ItemID != "" || op.Replace.ExpectedVersion != 0 {
			return false
		}
		op.Replace.ItemID, op.Replace.ExpectedVersion = item.ID, item.Version
	case domain.OperationEvidenceItem:
		if c.Evidence == nil {
			return false
		}
		switch {
		case op.Observation != nil && op.Observation.EvidenceItemID == "":
			op.Observation.EvidenceItemID = c.Evidence.ID
		case op.Transition != nil:
			for _, id := range op.Transition.EvidenceIDs {
				if id == c.Evidence.ID {
					return false
				}
			}
			op.Transition.EvidenceIDs = append(op.Transition.EvidenceIDs, c.Evidence.ID)
		default:
			return false
		}
	case domain.OperationObligationTarget:
		if !oneOb {
			return false
		}
		var target *domain.ObligationRef
		var revision *uint64
		switch {
		case op.Transition != nil:
			target, revision = &op.Transition.Target, &op.Transition.ExpectedRevision
		case op.Materialization != nil:
			target, revision = &op.Materialization.Target, &op.Materialization.ExpectedRevision
		case op.Reevaluate != nil:
			target, revision = &op.Reevaluate.Target, &op.Reevaluate.ExpectedRevision
		default:
			return false
		}
		if *target != (domain.ObligationRef{}) || *revision != 0 {
			return false
		}
		*target, *revision = ob.Ref, ob.Revision
	case domain.OperationGrantTarget:
		if op.Grant == nil || ref.Index >= len(op.Grant.Targets) || op.Grant.Targets[ref.Index] != (domain.GrantTarget{}) {
			return false
		}
		forOb, forItem := op.Grant.Action.ValidForTarget(domain.GrantTargetObligation), op.Grant.Action.ValidForTarget(domain.GrantTargetItem)
		switch {
		case forOb && oneOb && !(forItem && oneItem):
			op.Grant.Targets[ref.Index] = domain.ObligationGrantTarget(ob.Ref.SessionID, ob.Ref.ObligationID, ob.Ref.Version)
		case forItem && oneItem && !(forOb && oneOb):
			op.Grant.Targets[ref.Index] = domain.ItemGrantTarget(session, item.ID)
		default:
			return false
		}
	case domain.OperationWorkspaceBinding:
		if c.Workspace == nil {
			return false
		}
		switch {
		case op.RegisterRun != nil && op.RegisterRun.Binding == (domain.WorkspaceBindingRef{}):
			op.RegisterRun.Binding = *c.Workspace
		case op.DeclareObligation != nil && op.DeclareObligation.WorkspaceBinding == nil:
			w := *c.Workspace
			op.DeclareObligation.WorkspaceBinding = &w
		default:
			return false
		}
	case domain.OperationRun:
		if c.RunID == "" || op.Observation == nil || op.Observation.RunID != "" {
			return false
		}
		op.Observation.RunID = c.RunID
	case domain.OperationGrantReference:
		if c.GrantID == "" || op.RevokeGrant == nil || op.RevokeGrant.GrantID != "" {
			return false
		}
		op.RevokeGrant.GrantID = c.GrantID
	default:
		return false
	}
	return true
}

// setRequestID gives op's intent its ingestion-derived request identity.
// Typed operations share their event's identity (P3-34): a submitted
// payload's RequestID must be empty (domain validation rejects any other),
// and ValidateResolved then requires the derived one.
func setRequestID(op *domain.SemanticOperation, id string) {
	switch {
	case op.Grant != nil:
		op.Grant.RequestID = id
	case op.RevokeGrant != nil:
		op.RevokeGrant.RequestID = id
	case op.Transition != nil:
		op.Transition.RequestID = id
	case op.DeclareObligation != nil:
		op.DeclareObligation.RequestID = id
	case op.Materialization != nil:
		op.Materialization.RequestID = id
	case op.Reevaluate != nil:
		op.Reevaluate.RequestID = id
	case op.RegisterResource != nil:
		op.RegisterResource.RequestID = id
	case op.ReportResource != nil:
		op.ReportResource.RequestID = id
	case op.Workspace != nil:
		op.Workspace.RequestID = id
	case op.RegisterRun != nil:
		op.RegisterRun.RequestID = id
	case op.Observation != nil:
		op.Observation.RequestID = id
	case op.Checkpoint != nil:
		op.Checkpoint.RequestID = id
	case op.Replace != nil:
		op.Replace.RequestID = id
	}
}
