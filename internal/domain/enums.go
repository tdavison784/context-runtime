package domain

// Enumerations are string-backed so persisted records and diagnostics stay
// readable. Every enum has a Valid method; stores reject invalid values.

// Authority is the trust level assigned to content at ingestion (FR-ING-002).
type Authority string

const (
	AuthoritySystem           Authority = "SYSTEM"
	AuthorityHarness          Authority = "HARNESS"
	AuthorityUser             Authority = "USER"
	AuthorityAgent            Authority = "AGENT"
	AuthorityTool             Authority = "TOOL"
	AuthorityRetrievedContent Authority = "RETRIEVED_CONTENT"
)

// rank orders authorities by precedence. TOOL and RETRIEVED_CONTENT share a
// rank: neither can override the other.
var authorityRank = map[Authority]int{
	AuthoritySystem:           5,
	AuthorityHarness:          4,
	AuthorityUser:             3,
	AuthorityAgent:            2,
	AuthorityTool:             1,
	AuthorityRetrievedContent: 1,
}

// Valid reports whether a is a known authority.
func (a Authority) Valid() bool { _, ok := authorityRank[a]; return ok }

// AtLeast reports whether a may act on content of authority b: a equals b or
// strictly outranks it. Equal-rank authorities that differ (TOOL and
// RETRIEVED_CONTENT) do not dominate each other.
func (a Authority) AtLeast(b Authority) bool {
	if !a.Valid() || !b.Valid() {
		return false
	}
	return a == b || authorityRank[a] > authorityRank[b]
}

// Outranks reports whether a strictly outranks b.
func (a Authority) Outranks(b Authority) bool {
	if !a.Valid() || !b.Valid() {
		return false
	}
	return authorityRank[a] > authorityRank[b]
}

// SortRank is the total order used for deterministic tie-breaking
// (FR-ASM-006): higher sorts first, and TOOL sorts before RETRIEVED_CONTENT.
func (a Authority) SortRank() int {
	switch a {
	case AuthoritySystem:
		return 6
	case AuthorityHarness:
		return 5
	case AuthorityUser:
		return 4
	case AuthorityAgent:
		return 3
	case AuthorityTool:
		return 2
	case AuthorityRetrievedContent:
		return 1
	}
	return 0
}

// CanHoldLifecycleAuthority reports whether a principal with authority a may
// perform lifecycle mutations directly or receive lifecycle grants
// (FR-AUTH-001, FR-AUTH-002).
func (a Authority) CanHoldLifecycleAuthority() bool {
	return a == AuthoritySystem || a == AuthorityHarness || a == AuthorityUser
}

// Kind is the semantic kind of a context item (FR-DOM-002).
type Kind string

const (
	KindGoal             Kind = "goal"
	KindConstraint       Kind = "constraint"
	KindInstruction      Kind = "instruction"
	KindUserMessage      Kind = "user_message"
	KindAssistantMessage Kind = "assistant_message"
	KindConversation     Kind = "conversation"
	KindFact             Kind = "fact"
	KindDecision         Kind = "decision"
	KindTaskState        Kind = "task_state"
	KindToolCall         Kind = "tool_call"
	KindToolResult       Kind = "tool_result"
	KindError            Kind = "error"
	KindArtifact         Kind = "artifact"
	KindReference        Kind = "reference"
	KindSummary          Kind = "summary"
	KindEvidence         Kind = "evidence"
	KindReasoning        Kind = "reasoning"
)

// Category groups kinds into the semantic categories of FR-DOM-006.
type Category string

const (
	CategoryDirective    Category = "directive"
	CategoryKnowledge    Category = "knowledge"
	CategoryEvidence     Category = "evidence"
	CategoryState        Category = "state"
	CategoryConversation Category = "conversation"
)

var kindCategory = map[Kind]Category{
	KindGoal:             CategoryDirective,
	KindConstraint:       CategoryDirective,
	KindInstruction:      CategoryDirective,
	KindFact:             CategoryKnowledge,
	KindDecision:         CategoryKnowledge,
	KindSummary:          CategoryKnowledge,
	KindReference:        CategoryKnowledge,
	KindToolResult:       CategoryEvidence,
	KindError:            CategoryEvidence,
	KindEvidence:         CategoryEvidence,
	KindArtifact:         CategoryEvidence,
	KindTaskState:        CategoryState,
	KindUserMessage:      CategoryConversation,
	KindAssistantMessage: CategoryConversation,
	KindConversation:     CategoryConversation,
	KindToolCall:         CategoryConversation,
	KindReasoning:        CategoryConversation,
}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool { _, ok := kindCategory[k]; return ok }

// Category returns the semantic category of k, or "" for an unknown kind.
func (k Kind) Category() Category { return kindCategory[k] }

// Generation drives retention policy (FR-DOM-004). An item is pinned exactly
// when its generation is PINNED.
type Generation string

const (
	GenerationPinned    Generation = "PINNED"
	GenerationDurable   Generation = "DURABLE"
	GenerationWorking   Generation = "WORKING"
	GenerationEphemeral Generation = "EPHEMERAL"
)

// Valid reports whether g is a known generation.
func (g Generation) Valid() bool {
	switch g {
	case GenerationPinned, GenerationDurable, GenerationWorking, GenerationEphemeral:
		return true
	}
	return false
}

// Scope is the ownership scope of an item (FR-DOM-003).
type Scope string

const (
	ScopeTurn     Scope = "TURN"
	ScopeTask     Scope = "TASK"
	ScopeWorkflow Scope = "WORKFLOW"
	ScopeSession  Scope = "SESSION"
	ScopeAgent    Scope = "AGENT"
)

// Valid reports whether s is a known scope.
func (s Scope) Valid() bool {
	switch s {
	case ScopeTurn, ScopeTask, ScopeWorkflow, ScopeSession, ScopeAgent:
		return true
	}
	return false
}

// Residency is RESIDENT or ARCHIVED (FR-DOM-005). It is independent of goal
// status and currentness.
type Residency string

const (
	ResidencyResident Residency = "RESIDENT"
	ResidencyArchived Residency = "ARCHIVED"
)

// Valid reports whether r is a known residency.
func (r Residency) Valid() bool { return r == ResidencyResident || r == ResidencyArchived }

// GoalStatus is OPEN or RESOLVED and is present only on goals.
type GoalStatus string

const (
	GoalOpen     GoalStatus = "OPEN"
	GoalResolved GoalStatus = "RESOLVED"
)

// Valid reports whether g is a known goal status.
func (g GoalStatus) Valid() bool { return g == GoalOpen || g == GoalResolved }

// RetentionClass is the GC retention class of an item.
type RetentionClass string

const (
	RetentionProtected RetentionClass = "PROTECTED"
	RetentionHigh      RetentionClass = "HIGH"
	RetentionNormal    RetentionClass = "NORMAL"
	RetentionLow       RetentionClass = "LOW"
)

// Valid reports whether r is a known retention class.
func (r RetentionClass) Valid() bool {
	switch r {
	case RetentionProtected, RetentionHigh, RetentionNormal, RetentionLow:
		return true
	}
	return false
}

// RelationshipType is the type of a relationship edge (FR-REL-001).
type RelationshipType string

const (
	RelDerivedFrom RelationshipType = "DERIVED_FROM"
	RelSupersedes  RelationshipType = "SUPERSEDES"
	RelDependsOn   RelationshipType = "DEPENDS_ON"
	RelReferences  RelationshipType = "REFERENCES"
	RelSatisfies   RelationshipType = "SATISFIES"
	RelDuplicateOf RelationshipType = "DUPLICATE_OF"
)

// Valid reports whether t is a known relationship type.
func (t RelationshipType) Valid() bool {
	switch t {
	case RelDerivedFrom, RelSupersedes, RelDependsOn, RelReferences, RelSatisfies, RelDuplicateOf:
		return true
	}
	return false
}

// TaskStatus is the lifecycle status of a task.
type TaskStatus string

const (
	TaskActive    TaskStatus = "ACTIVE"
	TaskCompleted TaskStatus = "COMPLETED"
)

// Valid reports whether s is a known task status.
func (s TaskStatus) Valid() bool { return s == TaskActive || s == TaskCompleted }
