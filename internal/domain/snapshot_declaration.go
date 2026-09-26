package domain

import "slices"

type SnapshotDeclarationMember struct{ ItemID, DeclarationID, Signature string }

// Whole ordered creation snapshot identity is independent of subsequent goal,
// pin, residency, usage, and proof changes (P3-4).
type SnapshotDeclaration struct {
	SemanticMeta
	TaskID        string
	Authority     Authority
	Access        AccessBoundary
	PolicyVersion string
	Members       []SnapshotDeclarationMember
	Signature     string
	LegacyKnown   bool
}

func (s SnapshotDeclaration) Clone() SnapshotDeclaration {
	s.Members = slices.Clone(s.Members)
	return s
}
func (s SnapshotDeclaration) CanonicalSignature() (string, error) {
	if !s.LegacyKnown || !s.Authority.Valid() || !semanticID(s.PolicyVersion) {
		return "", invalid("snapshot declaration: unknown creation identity")
	}
	if err := semanticBoundary(s.SessionID, s.Access); err != nil {
		return "", err
	}
	e := NewCanonicalEncoder("context-runtime/snapshot-declaration/v1").String(s.SessionID).String(s.TaskID).String(string(s.Authority)).String(s.PolicyVersion)
	encodeBoundary(e, s.Access)
	e.Uint(uint64(len(s.Members)))
	seen := map[string]bool{}
	for _, m := range s.Members {
		if !semanticID(m.ItemID) || !semanticID(m.DeclarationID) || !ValidHash(m.Signature) || seen[m.ItemID] {
			return "", invalid("snapshot declaration: invalid or duplicate member")
		}
		seen[m.ItemID] = true
		e.String(m.Signature)
	}
	return e.Hash(), nil
}
func (s SnapshotDeclaration) Validate() error {
	if err := s.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !s.LegacyKnown {
		if s.Signature != "" || len(s.Members) != 0 {
			return invalid("snapshot declaration: unknown identity has members")
		}
		return nil
	}
	h, err := s.CanonicalSignature()
	if err != nil {
		return err
	}
	if h != s.Signature {
		return ErrIntegrity
	}
	return nil
}

// SemanticChange supports later materialization without deriving current
// requirement state from receipt creation snapshots or duplicate raw text.
type SemanticChange struct {
	SemanticMeta
	Target                              GrantTarget
	SourceAuthority                     Authority
	Actor                               Principal
	Access                              AccessBoundary
	Action                              Action
	Cause                               TransitionCause
	BeforeRevision, AfterRevision       uint64
	BeforeStatus, AfterStatus           string
	BeforeCurrentness, AfterCurrentness ItemCurrentness
	AuditID, CauseID, GrantID           string
}

func (c SemanticChange) Validate() error {
	if err := c.SemanticMeta.Validate(); err != nil {
		return err
	}
	if err := c.Target.Validate(); err != nil {
		return err
	}
	if c.Target.SessionID != c.SessionID || !c.SourceAuthority.Valid() || !c.Action.Valid() || c.Cause != "" && !c.Cause.Valid() || c.BeforeRevision == 0 || c.AfterRevision <= c.BeforeRevision || !semanticID(c.AuditID) {
		return invalid("semantic change: invalid target or causal revisions")
	}
	if err := semanticActor(c.SessionID, c.Actor); err != nil {
		return err
	}
	return semanticBoundary(c.SessionID, c.Access)
}
