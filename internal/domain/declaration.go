package domain

import "slices"

const DeclarationEncodingV1 = "context-runtime/creation-declaration/v1"

// CreationSemantics captures creation defaults, never current lifecycle state.
// AcceptedAttributes and SupportIDs are sorted unique sets; rejected attributes
// are absent. ObligationDeclarationHash identifies the complete typed declaration.
type CreationSemantics struct {
	Key                                    CurrentKey
	Authority                              Authority
	WorkflowID, AgentID                    string
	Section                                DirectiveSection
	Kind                                   Kind
	ContentHash, ObligationDeclarationHash string
	AcceptedAttributes, SupportIDs         []string
	Generation                             Generation
	Retention                              RetentionClass
	Residency                              Residency
	GoalStatus                             *GoalStatus
	OriginTaskID, OriginTurnID             string
	CreatedTurn                            uint64
	TTLTurns                               *int
}

func (s CreationSemantics) Clone() CreationSemantics {
	s.AcceptedAttributes = slices.Clone(s.AcceptedAttributes)
	s.SupportIDs = slices.Clone(s.SupportIDs)
	if s.GoalStatus != nil {
		v := *s.GoalStatus
		s.GoalStatus = &v
	}
	if s.TTLTurns != nil {
		v := *s.TTLTurns
		s.TTLTurns = &v
	}
	return s
}

func sortedUnique(ss []string) bool {
	for i, s := range ss {
		if s == "" || i > 0 && ss[i-1] >= s {
			return false
		}
	}
	return true
}

func (s CreationSemantics) Signature(policy string) (string, error) {
	if err := s.Key.Validate(); err != nil {
		return "", err
	}
	if !semanticID(policy) || !s.Authority.Valid() || !s.Section.Valid() || !s.Kind.Valid() || !s.Generation.Valid() || !s.Retention.Valid() || !s.Residency.Valid() || !ValidHash(s.ContentHash) || !sortedUnique(s.AcceptedAttributes) || !sortedUnique(s.SupportIDs) {
		return "", invalid("declaration: invalid creation semantics")
	}
	if s.ObligationDeclarationHash != "" && !ValidHash(s.ObligationDeclarationHash) {
		return "", invalid("declaration: invalid obligation signature")
	}
	if s.GoalStatus != nil && !s.GoalStatus.Valid() {
		return "", invalid("declaration: invalid goal status")
	}
	if s.TTLTurns != nil && (*s.TTLTurns <= 0 || *s.TTLTurns > MaxTTLTurns) {
		return "", invalid("declaration: invalid TTL")
	}
	if (s.Key.Access.Scope == ScopeTurn || s.TTLTurns != nil) && (s.OriginTaskID != s.Key.TaskID || s.OriginTaskID == "" || s.OriginTurnID == "" || s.CreatedTurn == 0) {
		return "", invalid("declaration: eligibility origin required")
	}
	key, _ := s.Key.CanonicalHash()
	e := NewCanonicalEncoder(DeclarationEncodingV1).String(policy).String(key).String(string(s.Authority)).String(s.WorkflowID).String(s.AgentID)
	e.String(string(s.Section)).String(string(s.Kind)).String(s.ContentHash).Strings(s.AcceptedAttributes).String(s.ObligationDeclarationHash).Strings(s.SupportIDs)
	e.String(string(s.Generation)).String(string(s.Retention)).String(string(s.Residency)).Uint(boolUint(s.GoalStatus != nil))
	if s.GoalStatus != nil {
		e.String(string(*s.GoalStatus))
	}
	e.String(s.OriginTaskID).String(s.OriginTurnID).Uint(s.CreatedTurn).Uint(boolUint(s.TTLTurns != nil))
	if s.TTLTurns != nil {
		e.Int(int64(*s.TTLTurns))
	}
	return e.Hash(), nil
}

// LegacyKnown=false preserves unknown identity without manufacturing defaults.
// No ordinary restatement may match an unknown declaration (P3-4/41).
type CreationDeclaration struct {
	SemanticMeta
	ItemID, PolicyVersion, Signature string
	LegacyKnown                      bool
	AcceptedSemantics                CreationSemantics
}

func (d CreationDeclaration) Clone() CreationDeclaration {
	d.AcceptedSemantics = d.AcceptedSemantics.Clone()
	return d
}
func (d CreationDeclaration) Validate() error {
	if err := d.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !semanticID(d.ItemID) || !semanticID(d.PolicyVersion) {
		return invalid("declaration: item and policy required")
	}
	if !d.LegacyKnown {
		if d.Signature != "" {
			return invalid("declaration: unknown identity carries signature")
		}
		return nil
	}
	h, err := d.AcceptedSemantics.Signature(d.PolicyVersion)
	if err != nil {
		return err
	}
	if d.AcceptedSemantics.Key.SessionID != d.SessionID || h != d.Signature {
		return ErrIntegrity
	}
	return nil
}
func (d CreationDeclaration) Same(other CreationDeclaration) bool {
	return d.LegacyKnown && other.LegacyKnown && d.Validate() == nil && other.Validate() == nil && d.SessionID == other.SessionID && d.PolicyVersion == other.PolicyVersion && d.Signature == other.Signature
}
