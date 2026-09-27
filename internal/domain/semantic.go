package domain

import (
	"errors"
	"slices"
)

// Independent versions: request identity must never depend on execution policy
// or on the storage envelope version (P3-2/40/41).
const (
	RequestHashV2                     = "ingest-payload/v2"
	RequestHashV3                     = "ingest-payload/v3"
	SemanticSchemaV1                  = "semantic-record/v1"
	Phase3PolicyVersion               = "phase3-policy/v1"
	DefaultMaxCheckpointSemanticBytes = 16 * 1024
)

var (
	ErrUnsupportedSchema    = errors.New("unsupported semantic schema")
	ErrResourceLimit        = errors.New("semantic resource limit exceeded")
	ErrUnknownApplicability = errors.New("resource applicability unknown")
	ErrIncompleteCoverage   = errors.New("incomplete coverage")
	ErrLeaseExpired         = errors.New("retrieval lease expired")
)

// SemanticMeta belongs to every new companion record. Seq is allocated in the
// creating transaction and must not be shared with a TargetCall audit.
type SemanticMeta struct {
	ID, SessionID, SchemaVersion string
	Seq                          uint64
}

func (m SemanticMeta) Validate() error {
	if !semanticID(m.ID) || !semanticID(m.SessionID) || m.Seq == 0 || m.SchemaVersion != SemanticSchemaV1 {
		return invalid("semantic record: invalid identity, sequence, or schema")
	}
	return nil
}

func semanticID(s string) bool {
	return s != "" && len(s) <= MaxOwnerIDBytes && printableASCII(s, false)
}

func semanticBoundary(session string, a AccessBoundary) error {
	if err := a.Validate(); err != nil {
		return err
	}
	if a.SessionID != session {
		return invalid("semantic record: boundary session mismatch")
	}
	return nil
}

func semanticActor(session string, p Principal) error {
	if err := validateIngestPrincipal(p); err != nil {
		return err
	}
	if p.SessionID != session {
		return invalid("semantic record: actor session mismatch")
	}
	return nil
}

func encodeBoundary(e *CanonicalEncoder, a AccessBoundary) {
	e.String(string(a.Scope)).String(a.SessionID).String(a.WorkflowID).String(a.TaskID).String(a.AgentID)
}

func encodePrincipal(e *CanonicalEncoder, p Principal) {
	e.String(p.SessionID).String(p.WorkflowID).String(p.TaskID).String(p.AgentID).String(string(p.Authority))
}

// Phase3Policy records finite effective limits and the exact code registries
// used by an operation. There is no zero/unlimited interpretation (P3-39/42).
type Phase3Policy struct {
	MaxPageSize, MaxReceiptBytes, MaxGCDecisions                                     int
	CheckpointGeneration                                                             Generation
	CheckpointRetention                                                              RetentionClass
	Version, Claim, Matcher, ObservationState, Eligibility, Locator, Coverage, Dedup string
	MaxOperations, MaxMetadataBytes, MaxTargets, MaxEvidence, MaxCoverageMembers     int
	MaxTransactionWork, MaxToolResultBytes, MaxCheckpointSemanticBytes               int
	DefaultLeaseCalls, MaxLeaseCalls                                                 uint64
	// GCTriggers is the explicit enabled trigger set, sorted and unique. A
	// trigger outside it never starts a collection; there is no implicit
	// "all triggers" interpretation of an empty or missing set.
	GCTriggers []GCTrigger
}

// DefaultGCTriggers returns a fresh canonical set of every registered trigger.
func DefaultGCTriggers() []GCTrigger {
	return []GCTrigger{GCManual, GCPolicy, GCSupersession, GCTaskCompletion, GCTTL}
}

func (p Phase3Policy) Clone() Phase3Policy {
	p.GCTriggers = slices.Clone(p.GCTriggers)
	return p
}

// GCTriggerEnabled reports whether t is in the policy's enabled set.
func (p Phase3Policy) GCTriggerEnabled(t GCTrigger) bool {
	return t.Valid() && slices.Contains(p.GCTriggers, t)
}

func (p Phase3Policy) Validate() error {
	for _, v := range []string{p.Version, p.Claim, p.Matcher, p.ObservationState, p.Eligibility, p.Locator, p.Coverage, p.Dedup} {
		if !semanticID(v) {
			return invalid("semantic policy: missing registry version")
		}
	}
	for _, n := range []int{p.MaxPageSize, p.MaxReceiptBytes, p.MaxGCDecisions, p.MaxOperations, p.MaxMetadataBytes, p.MaxTargets, p.MaxEvidence, p.MaxCoverageMembers, p.MaxTransactionWork, p.MaxToolResultBytes, p.MaxCheckpointSemanticBytes} {
		if n <= 0 {
			return invalid("semantic policy: limits must be finite and positive")
		}
	}
	if !p.CheckpointGeneration.Valid() || !p.CheckpointRetention.Valid() || p.CheckpointGeneration == GenerationPinned {
		return invalid("semantic policy: explicit non-pinned checkpoint defaults required")
	}
	if p.DefaultLeaseCalls == 0 || p.MaxLeaseCalls < p.DefaultLeaseCalls {
		return invalid("semantic policy: invalid lease allowance")
	}
	if len(p.GCTriggers) == 0 {
		return invalid("semantic policy: explicit enabled GC trigger set required")
	}
	for i, t := range p.GCTriggers {
		if !t.Valid() || i > 0 && p.GCTriggers[i-1] >= t {
			return invalid("semantic policy: GC triggers must be known, sorted and unique")
		}
	}
	return nil
}
