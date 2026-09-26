// Package policy implements pure semantic defaults and attribute rules. It has
// no parser, store, provider, or principal-authentication dependencies.
package policy

import (
	"fmt"
	"slices"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// Version identifies classification-policy v1: the defaults table below, the
// section kind allow-lists, and the attribute rules. Ingestion records it in
// every receipt and on every rule-produced edge (M2). Any behavior change
// requires a new version.
const Version = "policy/v1"

// Defaults supplies initial semantic metadata. Mandatory is a policy property;
// currentness and eligibility still gate mandatory inclusion at planning time.
// Scope is the requested scope: ingestion intersects its boundary with the
// authenticated span boundary and rejects an impossible combination, so a
// default never broadens a span's access (D8).
type Defaults struct {
	Role       domain.ItemRole
	Kind       domain.Kind
	Generation domain.Generation
	Scope      domain.Scope
	Retention  domain.RetentionClass
	Residency  domain.Residency
	GoalStatus *domain.GoalStatus
	Mandatory  bool
}

// Classification-policy v1 (M2). Classification is by authenticated span
// authority and accepted directive section only. Ordinary text is never
// inspected: words such as MUST, SYSTEM, PASS, or role labels in JSON/XML
// never create goals, pins, obligations, grants, lifecycle commands, or tool
// outcomes, and no event field can request a kind, generation, retention,
// scope, or mandatory status outside a parsed, authorized section.
//
//	Source                          Role        Kind               Generation Scope    Retention Mandatory
//	transcript, USER span           TRANSCRIPT  user_message       WORKING    TASK     NORMAL    no
//	transcript, AGENT span          TRANSCRIPT  assistant_message  WORKING    TASK     NORMAL    no
//	transcript, TOOL span           TRANSCRIPT  tool_result        EPHEMERAL  TURN     LOW       no
//	transcript, RETRIEVED_CONTENT   TRANSCRIPT  evidence           EPHEMERAL  TURN     LOW       no
//	transcript, SYSTEM span         TRANSCRIPT  conversation       WORKING    SESSION  NORMAL    no
//	transcript, HARNESS span        TRANSCRIPT  conversation       WORKING    TASK     NORMAL    no
//	residual text, SYSTEM span      semantic    instruction        DURABLE    SESSION  HIGH      yes
//	residual text, HARNESS span     semantic    instruction        DURABLE    TASK     HIGH      no
//	Goal section                    semantic    goal (OPEN)        DURABLE    TASK     PROTECTED yes
//	Pinned section                  semantic    constraint         PINNED     TASK     PROTECTED yes
//	Working section                 semantic    task_state         WORKING    TASK     NORMAL    no
//	Remember section                semantic    fact               DURABLE    TASK     HIGH      no
//	References section              semantic    reference          WORKING    TASK     NORMAL    no
//	Ephemeral section               semantic    evidence           EPHEMERAL  TURN     LOW       no
//
// Every span yields exactly one TRANSCRIPT item holding its verbatim parts.
// A SYSTEM or HARNESS transcript is kind conversation, not instruction: it is
// an audit and pending-input envelope that can never be read as system policy
// (FR-DOM-007), even by a consumer that ignores Role. Trusted text outside
// accepted directive sections ("residual") becomes one separate semantic
// instruction item DERIVED_FROM the transcript; a later replacement retires
// that item, never the immutable transcript. USER, AGENT, TOOL, and
// RETRIEVED_CONTENT spans yield no residual item. Only SYSTEM-authority
// instructions are mandatory by kind (FR-DOM-007). All items start RESIDENT.
//
// Section kind= allow-lists (FR-DIR-003): Pinned constraint|instruction;
// Working task_state|conversation; Remember fact|decision|summary; Ephemeral
// evidence|tool_result. Goal and References take no kind=.
var sectionKinds = map[domain.DirectiveSection][]domain.Kind{
	domain.SectionPinned:    {domain.KindConstraint, domain.KindInstruction},
	domain.SectionWorking:   {domain.KindTaskState, domain.KindConversation},
	domain.SectionRemember:  {domain.KindFact, domain.KindDecision, domain.KindSummary},
	domain.SectionEphemeral: {domain.KindEvidence, domain.KindToolResult},
}

// SectionKinds returns the kinds a section's kind= may select, in table order.
func SectionKinds(section domain.DirectiveSection) []domain.Kind {
	return slices.Clone(sectionKinds[section])
}

// ForSection returns FR-DIR-003 defaults as independent values.
func ForSection(section domain.DirectiveSection) (Defaults, error) {
	d := Defaults{Role: domain.RoleSemantic, Scope: domain.ScopeTask, Residency: domain.ResidencyResident}
	switch section {
	case domain.SectionGoal:
		d.Kind = domain.KindGoal
		d.Generation = domain.GenerationDurable
		d.Retention = domain.RetentionProtected
		status := domain.GoalOpen
		d.GoalStatus = &status
		d.Mandatory = true
	case domain.SectionPinned:
		d.Kind = domain.KindConstraint
		d.Generation = domain.GenerationPinned
		d.Retention = domain.RetentionProtected
		d.Mandatory = true
	case domain.SectionWorking:
		d.Kind = domain.KindTaskState
		d.Generation = domain.GenerationWorking
		d.Retention = domain.RetentionNormal
	case domain.SectionRemember:
		d.Kind = domain.KindFact
		d.Generation = domain.GenerationDurable
		d.Retention = domain.RetentionHigh
	case domain.SectionReferences:
		d.Kind = domain.KindReference
		d.Generation = domain.GenerationWorking
		d.Retention = domain.RetentionNormal
	case domain.SectionEphemeral:
		d.Kind = domain.KindEvidence
		d.Generation = domain.GenerationEphemeral
		d.Scope = domain.ScopeTurn
		d.Retention = domain.RetentionLow
	default:
		return Defaults{}, fmt.Errorf("%w: content section required", domain.ErrInvalidRecord)
	}
	return d, nil
}

// ForTranscript returns the transcript row for a span's authority.
func ForTranscript(authority domain.Authority) (Defaults, error) {
	d := Defaults{Role: domain.RoleTranscript, Scope: domain.ScopeTask, Residency: domain.ResidencyResident, Generation: domain.GenerationWorking, Retention: domain.RetentionNormal}
	switch authority {
	case domain.AuthorityUser:
		d.Kind = domain.KindUserMessage
	case domain.AuthorityAgent:
		d.Kind = domain.KindAssistantMessage
	case domain.AuthorityTool, domain.AuthorityRetrievedContent:
		d.Kind = domain.KindEvidence
		if authority == domain.AuthorityTool {
			d.Kind = domain.KindToolResult
		}
		d.Generation = domain.GenerationEphemeral
		d.Scope = domain.ScopeTurn
		d.Retention = domain.RetentionLow
	case domain.AuthoritySystem, domain.AuthorityHarness:
		d.Kind = domain.KindConversation
		if authority == domain.AuthoritySystem {
			d.Scope = domain.ScopeSession
		}
	default:
		return Defaults{}, fmt.Errorf("%w: invalid transcript authority", domain.ErrInvalidRecord)
	}
	return d, nil
}

// ForResidual returns the residual-instruction row. Only SYSTEM and HARNESS
// spans have one; any other authority is an error, so low-authority text can
// never become a semantic instruction.
func ForResidual(authority domain.Authority) (Defaults, error) {
	d := Defaults{Role: domain.RoleSemantic, Kind: domain.KindInstruction, Generation: domain.GenerationDurable, Retention: domain.RetentionHigh, Residency: domain.ResidencyResident}
	switch authority {
	case domain.AuthoritySystem:
		d.Scope = domain.ScopeSession
		d.Mandatory = true
	case domain.AuthorityHarness:
		d.Scope = domain.ScopeTask
	default:
		return Defaults{}, fmt.Errorf("%w: only SYSTEM and HARNESS spans have residual instructions", domain.ErrInvalidRecord)
	}
	return d, nil
}
