package contextruntime

import "github.com/tdavison784/context-runtime/internal/domain"

// Domain types re-exported for harnesses in other modules (SDD section 6).
type (
	Principal      = domain.Principal
	Authority      = domain.Authority
	Kind           = domain.Kind
	Generation     = domain.Generation
	Scope          = domain.Scope
	Residency      = domain.Residency
	GoalStatus     = domain.GoalStatus
	RetentionClass = domain.RetentionClass
	AccessBoundary = domain.AccessBoundary
	ContentPart    = domain.ContentPart
	SourceRef      = domain.SourceRef
	ContextItem    = domain.ContextItem
	ItemRef        = domain.ItemRef
)

// Ingestion input types (R3). Only the caller-constructed Event/Span input
// shape is public; ingestion results, receipts, diagnostics, and lifecycle
// command records stay internal until the phase that implements
// Runtime.Ingest settles the SDD section 8 signature.
type (
	Event     = domain.Event
	EventKind = domain.EventKind
	Span      = domain.Span
	InputPart = domain.InputPart
	PartType  = domain.PartType
)

// Event kinds (D15, D18).
const (
	EventSystem           = domain.EventSystem
	EventHarness          = domain.EventHarness
	EventUser             = domain.EventUser
	EventAgent            = domain.EventAgent
	EventTool             = domain.EventTool
	EventRetrievedContent = domain.EventRetrievedContent
)

// Part types (FR-ING-007).
const (
	PartText     = domain.PartText
	PartImage    = domain.PartImage
	PartDocument = domain.PartDocument
)

// Authorities in precedence order (FR-ING-002).
const (
	AuthoritySystem           = domain.AuthoritySystem
	AuthorityHarness          = domain.AuthorityHarness
	AuthorityUser             = domain.AuthorityUser
	AuthorityAgent            = domain.AuthorityAgent
	AuthorityTool             = domain.AuthorityTool
	AuthorityRetrievedContent = domain.AuthorityRetrievedContent
)
