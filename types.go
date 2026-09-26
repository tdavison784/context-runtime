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

// Authorities in precedence order (FR-ING-002).
const (
	AuthoritySystem           = domain.AuthoritySystem
	AuthorityHarness          = domain.AuthorityHarness
	AuthorityUser             = domain.AuthorityUser
	AuthorityAgent            = domain.AuthorityAgent
	AuthorityTool             = domain.AuthorityTool
	AuthorityRetrievedContent = domain.AuthorityRetrievedContent
)
