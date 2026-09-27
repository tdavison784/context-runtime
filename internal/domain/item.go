package domain

import (
	"fmt"
	"math"
	"slices"
	"time"
)

// PartType is the type of one immutable content part.
type PartType string

const (
	PartText     PartType = "text"
	PartImage    PartType = "image"
	PartDocument PartType = "document"
)

// Valid reports whether t is a known part type.
func (t PartType) Valid() bool { return t == PartText || t == PartImage || t == PartDocument }

// ContentPart is one immutable part of an item's content. Text parts carry
// their text inline; image and document parts reference an immutable blob by
// content hash (FR-ING-007).
type ContentPart struct {
	Type      PartType
	Text      string
	MediaType string
	BlobHash  string // required for image and document parts
	BlobSize  uint64 // byte length of the referenced blob
}

// Validate checks the part's structural rules.
func (p ContentPart) Validate() error {
	switch p.Type {
	case PartText:
		if p.BlobHash != "" || p.BlobSize != 0 {
			return invalid("text part must not reference a blob")
		}
	case PartImage, PartDocument:
		if p.Text != "" {
			return invalid("%s part must not carry inline text", p.Type)
		}
		if !ValidHash(p.BlobHash) {
			return invalid("%s part requires a valid blob hash", p.Type)
		}
		if p.MediaType == "" {
			return invalid("%s part requires a media type", p.Type)
		}
	default:
		return invalid("invalid part type %q", p.Type)
	}
	return nil
}

// ItemRole separates a span's verbatim transcript snapshot from semantic
// items (D8). A TRANSCRIPT item is the immutable audit and pending-input
// envelope of one span: it is never a directive, never a requirement kind,
// and never becomes a current requirement merely because its authority is
// SYSTEM or HARNESS; accepted directives and uncovered trusted instruction
// text become separate semantic items DERIVED_FROM it. The zero value is a
// semantic item, which keeps records that predate roles semantic.
type ItemRole string

const (
	RoleSemantic   ItemRole = ""
	RoleTranscript ItemRole = "TRANSCRIPT"
	RoleCheckpoint ItemRole = "CHECKPOINT"
	RoleProjection ItemRole = "PROJECTION"
)

// Valid reports whether r is a known role.
func (r ItemRole) Valid() bool {
	return r == RoleSemantic || r == RoleTranscript || r == RoleCheckpoint || r == RoleProjection
}

// SourceKind says what a source locator names.
type SourceKind string

const (
	SourcePath  SourceKind = "path"
	SourceURL   SourceKind = "url"
	SourceItem  SourceKind = "item"
	SourceEvent SourceKind = "event"
	SourceTool  SourceKind = "tool"
)

// SourceRef identifies where content came from. Paths and URLs are locators,
// never snapshots; ContentHash records the snapshot that was ingested.
type SourceRef struct {
	Kind        SourceKind
	Locator     string
	ContentHash string
	ToolCallID  string
}

// ContextItem is the unit of semantic state (SDD section 7). Content, kind,
// authority, IDs, scope, access boundary, source, and sequence number are
// immutable (FR-DOM-008). Generation, residency, goal status, retention,
// usage counters, and Version change only through audited lifecycle events.
// Relationship edges live only in Relationship records (FR-REL-001).
type ContextItem struct {
	ID          string
	EventID     string
	DirectiveID string
	Namespace   DirectiveNamespace // empty only for frozen pre-Phase-3 records
	// Section is the directive section that created the item, if any.
	Section DirectiveSection
	// Role is TRANSCRIPT for a span's verbatim snapshot (D8).
	Role ItemRole
	Seq  uint64

	SessionID  string
	WorkflowID string
	TaskID     string
	AgentID    string
	TurnID     string

	Kind       Kind
	Generation Generation
	Authority  Authority
	Scope      Scope
	Access     AccessBoundary
	Residency  Residency
	GoalStatus *GoalStatus
	Retention  RetentionClass

	Parts         []ContentPart
	ContentHash   string
	SemanticBytes uint64
	Importance    int64 // fixed-point, scale fixed by ADR 5

	CreatedAt    time.Time // audit only; never a semantic scoring input
	LastUsedCall uint64
	AccessCount  int
	TTLTurns     *int
	// CreatedTurn is the owning task's (TaskID) turn number when the item
	// was created, or 0 before any turn opened. TTL counts from it (D18).
	CreatedTurn uint64

	Tags   []string
	Source *SourceRef
	// SourceRanges locate the transcript bytes a derived item was formed
	// from (D8); empty for transcripts and items not derived from a span.
	SourceRanges []SourceRange

	// Version starts at 1 and increments with every lifecycle change; stores
	// use it for compare-and-swap.
	Version uint64
}

// IsPinned reports whether the item is pinned (FR-DOM-004).
func (it ContextItem) IsPinned() bool { return it.Generation == GenerationPinned }

// Clone returns a deep copy so callers cannot mutate stored state through
// shared slices or pointers.
func (it ContextItem) Clone() ContextItem {
	out := it
	out.Parts = slices.Clone(it.Parts)
	out.Tags = slices.Clone(it.Tags)
	out.SourceRanges = cloneSourceRanges(it.SourceRanges)
	if it.GoalStatus != nil {
		gs := *it.GoalStatus
		out.GoalStatus = &gs
	}
	if it.TTLTurns != nil {
		ttl := *it.TTLTurns
		out.TTLTurns = &ttl
	}
	if it.Source != nil {
		src := *it.Source
		out.Source = &src
	}
	return out
}

// Validate checks structural invariants that every stored item satisfies.
// It verifies the content hash and SemanticBytes against the parts, so a
// store never accepts an item whose identity disagrees with its content.
func (it ContextItem) Validate() error {
	if it.Namespace != "" {
		if err := it.validateNamespace(); err != nil {
			return err
		}
	}
	if it.ID == "" {
		return invalid("item: ID is required")
	}
	if it.SessionID == "" {
		return invalid("item %s: session ID is required", it.ID)
	}
	if it.Seq == 0 {
		return invalid("item %s: sequence number is required", it.ID)
	}
	if !it.Kind.Valid() {
		return invalid("item %s: invalid kind %q", it.ID, it.Kind)
	}
	if !it.Generation.Valid() {
		return invalid("item %s: invalid generation %q", it.ID, it.Generation)
	}
	if !it.Authority.Valid() {
		return invalid("item %s: invalid authority %q", it.ID, it.Authority)
	}
	if !it.Scope.Valid() {
		return invalid("item %s: invalid scope %q", it.ID, it.Scope)
	}
	if !it.Residency.Valid() {
		return invalid("item %s: invalid residency %q", it.ID, it.Residency)
	}
	if !it.Section.Valid() {
		return invalid("item %s: invalid directive section %q", it.ID, it.Section)
	}
	if it.Section != SectionNone && it.DirectiveID == "" {
		return invalid("item %s: a directive-section item requires a directive ID", it.ID)
	}
	// Directive sections are parsed only from SYSTEM, HARNESS, and marked
	// USER spans (FR-ING-004); agent, tool, and retrieved content never
	// carry one.
	if it.Section != SectionNone && !it.Authority.CanHoldLifecycleAuthority() {
		return invalid("item %s: a %s item cannot carry a directive section", it.ID, it.Authority)
	}
	if !it.Retention.Valid() {
		return invalid("item %s: invalid retention %q", it.ID, it.Retention)
	}
	if err := it.validateRole(); err != nil {
		return err
	}
	if err := it.Access.Validate(); err != nil {
		return fmt.Errorf("item %s: %w", it.ID, err)
	}
	if it.Access.Scope != it.Scope || it.Access.SessionID != it.SessionID {
		return invalid("item %s: access boundary disagrees with scope or session", it.ID)
	}
	if it.Access.TaskID != "" && it.Access.TaskID != it.TaskID {
		return invalid("item %s: access boundary task disagrees with item task", it.ID)
	}
	if it.Access.WorkflowID != "" && it.Access.WorkflowID != it.WorkflowID {
		return invalid("item %s: access boundary workflow disagrees with item workflow", it.ID)
	}
	if it.Access.AgentID != "" && it.Access.AgentID != it.AgentID {
		return invalid("item %s: access boundary agent disagrees with item agent", it.ID)
	}
	if it.Scope == ScopeTurn && it.TurnID == "" {
		return invalid("item %s: TURN scope requires a turn ID", it.ID)
	}
	if (it.Kind == KindGoal) != (it.GoalStatus != nil) {
		return invalid("item %s: goal status is required for goals and forbidden otherwise", it.ID)
	}
	if it.GoalStatus != nil && !it.GoalStatus.Valid() {
		return invalid("item %s: invalid goal status %q", it.ID, *it.GoalStatus)
	}
	if it.TTLTurns != nil && (*it.TTLTurns <= 0 || *it.TTLTurns > MaxTTLTurns) {
		return invalid("item %s: TTL must be a count of turns in 1..%d", it.ID, MaxTTLTurns)
	}
	// ADR 6: TTL counts turns of the originating task. Without one the TTL
	// could never be counted and the item would never expire (fail open).
	if it.TTLTurns != nil && it.TaskID == "" {
		return invalid("item %s: TTL requires an originating task", it.ID)
	}
	if len(it.Parts) == 0 {
		return invalid("item %s: at least one content part is required", it.ID)
	}
	for i, p := range it.Parts {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("item %s part %d: %w", it.ID, i, err)
		}
	}
	for _, r := range it.SourceRanges {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("item %s: %w", it.ID, err)
		}
		if r.TranscriptID == it.ID {
			return invalid("item %s: an item cannot be its own source range", it.ID)
		}
	}
	if want := ContentHash(it.Parts); it.ContentHash != want {
		return invalid("item %s: content hash does not match parts", it.ID)
	}
	if want := SemanticBytes(it.Parts); it.SemanticBytes != want {
		return invalid("item %s: semantic bytes do not match parts", it.ID)
	}
	if it.Version == 0 {
		return invalid("item %s: version must start at 1", it.ID)
	}
	return nil
}

// QualifiesAsEvidenceSupport is the structural precondition for citing it
// as evidence SUPPORT (keyed writes, completion claims, EVIDENCE_SUPPORT
// coverage). It is necessary, not sufficient: internal/graph also requires
// a projection's source to qualify and a TOOL tool_result transcript to
// carry trusted provenance (SEC-1.3). It admits only evidence-category kinds
// (FR-DOM-006), never AGENT or RETRIEVED_CONTENT authority, never a
// checkpoint, and among transcripts only a TOOL tool_result; USER, AGENT,
// SYSTEM and HARNESS conversation transcripts stay provenance-only.
func (it ContextItem) QualifiesAsEvidenceSupport() bool {
	if it.Kind.Category() != CategoryEvidence || it.Role == RoleCheckpoint || !it.Role.Valid() {
		return false
	}
	if it.Authority == AuthorityAgent || it.Authority == AuthorityRetrievedContent || !it.Authority.Valid() {
		return false
	}
	if it.Role == RoleTranscript {
		return it.Authority == AuthorityTool && it.Kind == KindToolResult
	}
	return true
}

// validateRole fails closed on a transcript that could pose as a
// requirement: no directive identity, no directive-category kind, no pinned
// generation or protected retention, and no source ranges (it is the source).
func (it ContextItem) validateRole() error {
	if !it.Role.Valid() {
		return invalid("item %s: invalid role %q", it.ID, it.Role)
	}
	if it.Role != RoleTranscript {
		return nil
	}
	if it.Section != SectionNone || it.DirectiveID != "" || len(it.SourceRanges) != 0 ||
		it.Kind.Category() == CategoryDirective || it.Generation == GenerationPinned || it.Retention == RetentionProtected {
		return invalid("item %s: a transcript cannot carry directive identity or requirement status", it.ID)
	}
	return nil
}

// MaxTTLTurns is the largest accepted TTL (R1): math.MaxInt32, so a TTL is
// representable in int on every Go platform and in every store.
const MaxTTLTurns = math.MaxInt32

// ValidateTurnOwnership checks the D18 creation rule ingestion enforces on
// every new item: a TURN-scoped or TTL-bound item needs an owning task and a
// turn that has opened, so expiry is never computed against a substitute
// turn counter. Records that predate CreatedTurn are not checked by Validate.
func (it ContextItem) ValidateTurnOwnership() error {
	if (it.Scope == ScopeTurn || it.TTLTurns != nil) && (it.TaskID == "" || it.CreatedTurn == 0) {
		return invalid("item %s: TURN scope and TTL require an owning task turn", it.ID)
	}
	return nil
}

// TTLLive reports whether an item created at turn created with a TTL of n
// turns is live at turn current of the same owning task (D18): created >= 1,
// current >= created, and current-created < n. The difference form cannot
// overflow. A creation turn of 0 (an item that predates turn metadata, or
// one created before any turn opened) is never live: TTL eligibility is not
// invented for records that lack their origin (M8, R18). TURN scope expires
// at the next turn regardless of a larger TTL; callers check scope separately.
func TTLLive(created, current uint64, n int) bool {
	return n > 0 && created > 0 && current >= created && current-created < uint64(n)
}

// ItemRef names an item for plans, manifests, and relationships.
type ItemRef struct {
	ID      string
	Version uint64
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRecord, fmt.Sprintf(format, args...))
}
