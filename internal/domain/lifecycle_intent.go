package domain

import "slices"

// Intents contain caller requests only. Services derive actor, sequence, grant,
// audit ID, status history, and committed result in the transaction (P3-1/8).
type ItemMutationIntent struct {
	RequestID, ItemID string
	ExpectedVersion   uint64
}

func (i ItemMutationIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.ItemID) || i.ExpectedVersion == 0 {
		return invalid("item intent: request, occurrence, and expected version required")
	}
	return nil
}

type ResolveIntent = ItemMutationIntent
type UnpinIntent = ItemMutationIntent
type ArchiveIntent = ItemMutationIntent
type UnarchiveIntent = ItemMutationIntent

type PromoteIntent struct {
	ItemMutationIntent
	Generation Generation
}
type DemoteIntent = PromoteIntent

func (i PromoteIntent) Validate() error {
	if err := i.ItemMutationIntent.Validate(); err != nil {
		return err
	}
	if !i.Generation.Valid() {
		return invalid("generation intent: unknown generation")
	}
	return nil
}

type CompleteTaskIntent struct{ RequestID, TaskID string }

func (i CompleteTaskIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.TaskID) {
		return invalid("completion intent: request and task required")
	}
	return nil
}

type ReplaceDirectiveIntent struct {
	ItemMutationIntent // exact expected current occurrence and lifecycle version
	Parts              []ContentPart
	AcceptedAttributes []string `canonical:"set"`
}

func (i ReplaceDirectiveIntent) Clone() ReplaceDirectiveIntent {
	i.Parts = slices.Clone(i.Parts)
	i.AcceptedAttributes = slices.Clone(i.AcceptedAttributes)
	return i
}
func (i ReplaceDirectiveIntent) Validate() error {
	if err := i.ItemMutationIntent.Validate(); err != nil {
		return err
	}
	if len(i.Parts) == 0 {
		return invalid("replacement intent: content required")
	}
	for _, p := range i.Parts {
		if err := p.Validate(); err != nil {
			return err
		}
	}
	return nil
}

type GrantIntent struct {
	RequestID, GrantID string
	Action             Action
	Targets            []GrantTarget `canonical:"set"`
	Grantee            *Principal
	Matcher            *MatcherRef
	ExpiresAtSeq       uint64
}

func (i GrantIntent) Clone() GrantIntent {
	i.Targets = slices.Clone(i.Targets)
	if i.Grantee != nil {
		v := *i.Grantee
		i.Grantee = &v
	}
	if i.Matcher != nil {
		v := *i.Matcher
		i.Matcher = &v
	}
	return i
}
func (i GrantIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.GrantID) || !i.Action.Delegable() || len(i.Targets) == 0 || (i.Grantee == nil) == (i.Matcher == nil) {
		return invalid("grant intent: unsupported action or incomplete target/grantee")
	}
	seen := map[string]bool{}
	for _, t := range i.Targets {
		if err := t.Validate(); err != nil {
			return err
		}
		if !i.Action.ValidForTarget(t.Kind) {
			return invalid("grant intent: action does not apply to target kind")
		}
		if seen[t.AuthorizationKey] {
			return invalid("grant intent: duplicate target")
		}
		seen[t.AuthorizationKey] = true
	}
	if i.Grantee != nil {
		if err := i.Grantee.Validate(); err != nil {
			return err
		}
		if !i.Grantee.Authority.CanHoldLifecycleAuthority() {
			return ErrInvalidAuthorityPromotion
		}
	}
	if i.Matcher != nil && (i.Action != ActionAssertObligation || !semanticID(i.Matcher.Name) || !semanticID(i.Matcher.Version)) {
		return invalid("grant intent: matcher only asserts")
	}
	return nil
}

type RevokeGrantIntent struct{ RequestID, GrantID string }

func (i RevokeGrantIntent) Validate() error {
	if !semanticID(i.RequestID) || !semanticID(i.GrantID) {
		return invalid("revocation intent: request and grant required")
	}
	return nil
}
