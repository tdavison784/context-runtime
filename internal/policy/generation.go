package policy

import "github.com/tdavison784/context-runtime/internal/domain"

const GenerationPolicyVersion = "generation/v1"

// GenerationSnapshot must reflect the complete current source-obligation set,
// regardless of status or materialization exceptions, at the item's revision.
type GenerationSnapshot struct {
	Item                                      domain.ItemRevisionRef
	Currentness                               domain.ItemCurrentness
	ObligationsKnown, CurrentObligationSource bool
}

// GenerationChange is the closed P3-10/X5 policy. It grants no mutation authority:
// the service must authorize the action at its actual allocated sequence.
func GenerationChange(it domain.ContextItem, s GenerationSnapshot, action domain.Action, to domain.Generation) (domain.ItemChange, error) {
	if it.Validate() != nil || !s.Currentness.Valid() || !s.ObligationsKnown || s.Item.ItemID != it.ID || s.Item.Version != it.Version || !to.Valid() || it.DirectiveID != "" && s.Currentness == domain.ItemUnkeyed {
		return domain.ItemChange{}, domain.ErrInvalidRecord
	}
	if it.Role != domain.RoleSemantic || it.Kind == domain.KindGoal || it.Kind == domain.KindReasoning || s.CurrentObligationSource {
		return domain.ItemChange{}, domain.ErrInvalidTransition
	}
	from := it.Generation
	allowed := false
	switch action {
	case domain.ActionPromote:
		allowed = from == domain.GenerationEphemeral && to == domain.GenerationWorking || from == domain.GenerationWorking && to == domain.GenerationDurable ||
			from == domain.GenerationDurable && to == domain.GenerationPinned && (it.Kind == domain.KindConstraint || it.Kind == domain.KindInstruction) && (s.Currentness == domain.ItemCurrent || s.Currentness == domain.ItemUnkeyed)
	case domain.ActionDemote:
		allowed = it.Kind.Category() != domain.CategoryDirective && (from == domain.GenerationDurable && to == domain.GenerationWorking || from == domain.GenerationWorking && to == domain.GenerationEphemeral)
	}
	if !allowed {
		return domain.ItemChange{}, domain.ErrInvalidTransition
	}
	retention := map[domain.Generation]domain.RetentionClass{
		domain.GenerationEphemeral: domain.RetentionLow, domain.GenerationWorking: domain.RetentionNormal,
		domain.GenerationDurable: domain.RetentionHigh, domain.GenerationPinned: domain.RetentionProtected,
	}[to]
	return domain.ItemChange{Generation: &to, Retention: &retention}, nil
}
