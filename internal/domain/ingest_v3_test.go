package domain

import (
	"errors"
	"testing"
)

func semanticPolicy() Phase3Policy {
	return Phase3Policy{Version: Phase3PolicyVersion, Claim: "claim/1", Matcher: "matcher/1", ObservationState: "obs-state/1", Eligibility: "eligibility/1", Locator: "resource-locator/1", Coverage: "coverage/1", Dedup: "declaration/1", MaxOperations: 64, MaxMetadataBytes: 65536, MaxTargets: 64, MaxEvidence: 64, MaxCoverageMembers: 1024, MaxTransactionWork: 4096, MaxToolResultBytes: 65536, MaxCheckpointSemanticBytes: DefaultMaxCheckpointSemanticBytes, DefaultLeaseCalls: 2, MaxLeaseCalls: 8}
}
func TestV3HashOrdersOperationsAndConflictsWithV2Identity(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthorityHarness}
	e := Event{EventID: "event", Kind: EventHarness, ResourceControl: true, Operations: []SemanticOperation{
		{Kind: OperationRegisterResource, RegisterResource: &RegisterResourceIntent{RequestID: "register", ResourceID: "repo", Reporter: p, Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}}},
		{Kind: OperationReportResource, ReportResource: &ReportResourceChangeIntent{RequestID: "report", ResourceID: "repo", ResultingAuthoritativeRevision: 1, WorkspaceFingerprint: HashBytes(nil), AllPaths: true}},
	}}
	policy := semanticPolicy()
	h, err := e.PayloadHashFor(RequestHashV3, p, Limits{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	copy := e.Clone()
	copy.Operations[0], copy.Operations[1] = copy.Operations[1], copy.Operations[0]
	h2, err := copy.PayloadHashFor(RequestHashV3, p, Limits{}, policy)
	if err != nil || h == h2 {
		t.Fatal("operation order omitted", err)
	}
	if _, err := e.PayloadHashFor(RequestHashV2, p, Limits{}, policy); !errors.Is(err, ErrEventIDConflict) {
		t.Fatal("phase 3 fields projected away", err)
	}
	copy = e.Clone()
	copy.Operations[1].ReportResource.WorkspaceFingerprint = HashBytes([]byte("edit"))
	h2, _ = copy.PayloadHashFor(RequestHashV3, p, Limits{}, policy)
	if h == h2 {
		t.Fatal("typed report field omitted")
	}
}
func TestV3SpanCoverageAndSourceAuthority(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthorityHarness}
	access := AccessBoundary{Scope: ScopeSession, SessionID: "s"}
	e := Event{Kind: EventHarness, Spans: []Span{{Authority: AuthorityTool, Access: access, Parts: []InputPart{{Type: PartText, Text: "untrusted"}}}}, Operations: []SemanticOperation{{Kind: OperationSpan, Span: &SpanIngestIntent{Index: 0}}}}
	if err := e.ValidateV3(); err != nil {
		t.Fatal(err)
	}
	source := 0
	e.Operations = append(e.Operations, SemanticOperation{Kind: OperationRegisterResource, SourceSpanIndex: &source, RegisterResource: &RegisterResourceIntent{RequestID: "r", ResourceID: "repo", Reporter: p, Access: access}})
	if !errors.Is(e.ValidateV3(), ErrInvalidAuthorityPromotion) {
		t.Fatal("TOOL source promoted by envelope")
	}
	e.Operations = e.Operations[:1]
	e.Operations[0].Span.Index = 1
	if e.ValidateV3() == nil {
		t.Fatal("missing span accepted")
	}
}

func TestV3EnvelopeVerifiesRecordedSchemaAndClonesPolicy(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthorityHarness}
	policy := semanticPolicy()
	e := Event{EventID: "event", Kind: EventHarness, Spans: []Span{{Authority: AuthorityHarness, Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}, Parts: []InputPart{{Type: PartText, Text: "hello"}}}}}
	h, err := e.PayloadHashFor(RequestHashV3, p, Limits{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	env := EventEnvelope{SessionID: "s", OccurrenceID: CallerOccurrenceID("s", "event"), EventID: "event", Principal: p, Event: e, PayloadHash: h, SchemaVersion: EventEnvelopeSchemaV2, RequestHashVersion: RequestHashV3, SemanticPolicy: &policy}
	if err := env.Validate(); err != nil {
		t.Fatal(err)
	}
	copy := env.Clone()
	copy.SemanticPolicy.MaxOperations = 1
	if env.SemanticPolicy.MaxOperations == 1 {
		t.Fatal("envelope aliases recorded policy")
	}
	env.SchemaVersion = EventEnvelopeSchemaVersion
	if env.Validate() == nil {
		t.Fatal("v3 envelope decoded as legacy")
	}
}
