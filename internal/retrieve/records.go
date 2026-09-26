package retrieve

import (
	"cmp"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
	"github.com/tdavison784/context-runtime/internal/policy"
)

type recordSeqs struct{ Lease, Coverage, Item, Projection, Result, Event, Receipt uint64 }

type recordInput struct {
	Source       domain.ContextItem
	Observed     domain.ObservedItemState
	Task         domain.TaskState
	Conversation domain.Conversation
	Actor        domain.Principal
	Intent       AdmissionIntent
	Policy       domain.Phase3Policy
	Arguments    []byte
	Allowance    uint64
	AllowStub    bool
	Existing     *domain.RetrievalLease
	Inherited    *domain.ProjectionRecord
	Seqs         recordSeqs
}

type retrievalRecords struct {
	Lease      domain.RetrievalLease
	NewLease   bool
	Coverage   domain.CoverageRecord
	Member     domain.CoverageMember
	Members    []domain.CoverageMember
	Item       domain.ContextItem
	Projection domain.ProjectionRecord
	Result     domain.RetrievalResult
	Event      domain.RetrievalEvent
	Receipt    domain.MutationReceipt
}

func retrievalID(kind, session, request string) string {
	return kind + "_" + domain.NewCanonicalEncoder("context-runtime/retrieval-record/v1").String(kind).String(session).String(request).Hash()
}

func retrievalMeta(kind, session, request string, seq uint64) domain.SemanticMeta {
	return domain.SemanticMeta{ID: retrievalID(kind, session, request), SessionID: session, Seq: seq, SchemaVersion: domain.SemanticSchemaV1}
}

// buildRetrievalRecords freezes one immutable source snapshot into a complete
// historical TOOL projection. The old lease is never changed on coalescing.
func buildRetrievalRecords(in recordInput) (retrievalRecords, error) {
	var out retrievalRecords
	if _, err := validateAdmission(in.Actor, in.Intent, in.Task, in.Conversation, in.Policy); err != nil {
		return out, err
	}
	if in.Allowance == 0 || in.Source.Validate() != nil || in.Observed.Validate() != nil ||
		in.Observed.Source != (domain.ItemContentRef{ItemID: in.Source.ID, ContentHash: in.Source.ContentHash}) ||
		in.Intent.Rehydrate.ItemID != in.Source.ID || in.Seqs.Coverage == 0 || in.Seqs.Item == 0 ||
		in.Seqs.Projection == 0 || in.Seqs.Result == 0 || in.Seqs.Event == 0 || in.Seqs.Receipt == 0 {
		return out, domain.ErrInvalidRecord
	}
	if in.Source.Role == domain.RoleProjection {
		if in.Inherited == nil || in.Inherited.Validate() != nil || in.Inherited.ItemID != in.Source.ID ||
			in.Inherited.Access != in.Source.Access || in.Inherited.Origin.Holder != in.Actor ||
			in.Source.Source == nil || in.Source.Source.Kind != domain.SourceItem ||
			in.Source.Source.Locator != in.Inherited.Source.ItemID || in.Source.Source.ContentHash != in.Inherited.Source.ContentHash {
			return out, domain.ErrIncompleteCoverage
		}
	} else if in.Inherited != nil {
		return out, domain.ErrInvalidRecord
	}
	session, request := in.Actor.SessionID, in.Intent.Rehydrate.RequestID
	if in.Existing != nil {
		if in.Seqs.Lease != 0 || !policy.LeaseLive(*in.Existing, policy.LeaseSnapshot{Seq: in.Seqs.Coverage, Source: in.Observed.Source, Task: in.Task, Conversation: in.Conversation}, in.Actor, in.Task.TurnID) {
			return out, domain.ErrLeaseExpired
		}
		out.Lease = *in.Existing
	} else {
		if in.Seqs.Lease == 0 {
			return out, domain.ErrInvalidRecord
		}
		out.NewLease = true
		out.Lease = domain.RetrievalLease{SemanticMeta: retrievalMeta("lease", session, request, in.Seqs.Lease), Holder: in.Actor,
			ConversationID: in.Conversation.ConversationID, TurnID: in.Task.TurnID, Source: in.Observed.Source,
			IssuedCompletedInferenceIndex: in.Conversation.LogicalCalls, CallAllowance: in.Allowance, PolicyVersion: in.Policy.Version}
	}
	item, version, omitted, err := buildProjection(ProjectionInput{Source: in.Source, Observed: in.Observed,
		Origin: in.Intent.Origin, Task: in.Task, ItemID: retrievalID("item", session, request), RequestID: request,
		Seq: in.Seqs.Item, MaxBytes: in.Policy.MaxToolResultBytes, AllowStub: in.AllowStub})
	if err != nil {
		return out, err
	}
	out.Item = item
	out.Coverage = domain.CoverageRecord{SemanticMeta: retrievalMeta("coverage", session, request, in.Seqs.Coverage),
		Purpose: domain.CoverageLeaseDependency, Access: item.Access, ConversationID: in.Conversation.ConversationID}
	source := in.Observed.Source
	out.Member = domain.CoverageMember{SemanticMeta: retrievalMeta("member", session, request, in.Seqs.Coverage),
		CoverageID: out.Coverage.ID, Source: &source, LeaseID: out.Lease.ID}
	out.Members = []domain.CoverageMember{out.Member}
	if in.Inherited != nil {
		oldSource := in.Inherited.Source
		out.Members = append(out.Members,
			domain.CoverageMember{SemanticMeta: retrievalMeta("original-member", session, request, in.Seqs.Coverage),
				CoverageID: out.Coverage.ID, Source: &oldSource, LeaseID: in.Inherited.LeaseID},
			domain.CoverageMember{SemanticMeta: retrievalMeta("nested-member", session, request, in.Seqs.Coverage),
				CoverageID: out.Coverage.ID, NestedCoverageID: in.Inherited.DependencyCoverageID})
	}
	slices.SortFunc(out.Members, func(a, b domain.CoverageMember) int {
		x, _ := a.Key()
		y, _ := b.Key()
		return cmp.Compare(x, y)
	})
	out.Coverage.MemberCount = uint64(len(out.Members))
	out.Coverage.Signature, err = domain.CoverageSignature(out.Coverage, out.Members)
	if err != nil {
		return retrievalRecords{}, err
	}
	out.Projection = domain.ProjectionRecord{SemanticMeta: retrievalMeta("projection", session, request, in.Seqs.Projection),
		ItemID: item.ID, Source: source, LeaseID: out.Lease.ID, RetrievalResultID: retrievalID("result", session, request),
		DependencyCoverageID: out.Coverage.ID, Origin: in.Intent.Origin.Clone(), Access: item.Access,
		DeliveryPolicyVersion: version, OmittedBytes: omitted}
	out.Result = domain.RetrievalResult{SemanticMeta: retrievalMeta("result", session, request, in.Seqs.Result),
		RequestID: request, LeaseID: out.Lease.ID, ProjectionID: out.Projection.ID,
		RetrievalEventID: retrievalID("event", session, request), Origin: in.Intent.Origin.Clone(),
		Observed: in.Observed.Clone(), Access: item.Access, PolicyVersion: in.Policy.Version}
	invocationID := ""
	if in.Intent.Origin.Invocation != nil {
		invocationID, err = in.Intent.Origin.Invocation.ID()
		if err != nil {
			return retrievalRecords{}, err
		}
	}
	out.Event = domain.RetrievalEvent{SemanticMeta: retrievalMeta("event", session, request, in.Seqs.Event),
		RequestID: request, Principal: in.Actor, TriggeringActor: in.Actor, InvocationID: invocationID,
		ResultID: out.Result.ID, Source: &source}
	hash, err := domain.MutationRequestHash(in.Actor, domain.MutationRetrieval, in.Intent.Method, in.Arguments)
	if err != nil {
		return retrievalRecords{}, err
	}
	receiptID, err := domain.MutationReceiptID(session, domain.MutationRetrieval, request)
	if err != nil {
		return retrievalRecords{}, err
	}
	out.Receipt = domain.MutationReceipt{SemanticMeta: domain.SemanticMeta{ID: receiptID, SessionID: session, Seq: in.Seqs.Receipt, SchemaVersion: domain.SemanticSchemaV1},
		Family: domain.MutationRetrieval, RequestID: request, Principal: in.Actor, CanonicalMethod: in.Intent.Method,
		CanonicalArguments: in.Arguments, RequestHashVersion: domain.RequestHashV3, RequestHash: hash,
		PolicyVersion: in.Policy.Version, Result: domain.MutationResult{Tool: &domain.ToolResult{RetrievalResultID: out.Result.ID}}}
	for _, validate := range []func() error{out.Lease.Validate, out.Coverage.Validate, out.Member.Validate, out.Item.ValidateSemantic,
		out.Projection.Validate, out.Result.Validate, out.Event.Validate, out.Receipt.Validate} {
		if err := validate(); err != nil {
			return retrievalRecords{}, err
		}
	}
	for _, member := range out.Members {
		if err := member.Validate(); err != nil {
			return retrievalRecords{}, err
		}
	}
	if err := out.Result.ValidateOriginEvent(out.Event); err != nil {
		return retrievalRecords{}, err
	}
	return out, nil
}
