// Package policy implements pure semantic defaults and attribute rules. It has
// no parser, store, provider, or principal-authentication dependencies.
package policy

import (
	"fmt"
	"github.com/tdavison784/context-runtime/internal/domain"
)

// Defaults supplies initial semantic metadata. Mandatory is a policy property;
// currentness and eligibility still gate mandatory inclusion at planning time.
type Defaults struct {
	Kind       domain.Kind
	Generation domain.Generation
	Scope      domain.Scope
	Retention  domain.RetentionClass
	Residency  domain.Residency
	GoalStatus *domain.GoalStatus
	Mandatory  bool
}

// ForSection returns FR-DIR-003 defaults as independent values.
func ForSection(section domain.DirectiveSection) (Defaults, error) {
	d := Defaults{Scope: domain.ScopeTask, Residency: domain.ResidencyResident}
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

// ForTranscript returns D8 defaults. Ingestion intersects the selected scope's
// boundary with the original span boundary; defaults never broaden its access.
func ForTranscript(authority domain.Authority) (Defaults, error) {
	d := Defaults{Scope: domain.ScopeTask, Residency: domain.ResidencyResident, Generation: domain.GenerationWorking, Retention: domain.RetentionNormal}
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
		d.Kind = domain.KindInstruction
		d.Generation = domain.GenerationDurable
		d.Retention = domain.RetentionHigh
		if authority == domain.AuthoritySystem {
			d.Scope = domain.ScopeSession
		}
	default:
		return Defaults{}, fmt.Errorf("%w: invalid transcript authority", domain.ErrInvalidRecord)
	}
	return d, nil
}
