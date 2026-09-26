package domain

// GrantTarget names an immutable occurrence or an exact obligation Version,
// never the obligation's mutable Revision or a latest-version alias (P3-5).
type GrantTargetKind string

const (
	GrantTargetItem       GrantTargetKind = "ITEM_OCCURRENCE"
	GrantTargetObligation GrantTargetKind = "OBLIGATION_VERSION"
	GrantTargetEncodingV1                 = "context-runtime/grant-target/v1"
)

type GrantTarget struct {
	Kind                            GrantTargetKind
	SessionID, ItemID, ObligationID string
	Version                         uint64
	AuthorizationKey                string
}

func (t GrantTarget) CanonicalKey() (string, error) {
	if !semanticID(t.SessionID) {
		return "", invalid("grant target: session required")
	}
	switch t.Kind {
	case GrantTargetItem:
		if !semanticID(t.ItemID) || t.ObligationID != "" || t.Version != 0 {
			return "", invalid("grant target: invalid item target")
		}
	case GrantTargetObligation:
		if !semanticID(t.ObligationID) || t.ItemID != "" || t.Version == 0 {
			return "", invalid("grant target: exact obligation version required")
		}
	default:
		return "", invalid("grant target: unknown kind")
	}
	return NewCanonicalEncoder(GrantTargetEncodingV1).String(string(t.Kind)).String(t.SessionID).String(t.ItemID).String(t.ObligationID).Uint(t.Version).Hash(), nil
}

func (t GrantTarget) Validate() error {
	k, err := t.CanonicalKey()
	if err != nil {
		return err
	}
	if t.AuthorizationKey != k {
		return invalid("grant target: canonical key mismatch")
	}
	return nil
}

func ItemGrantTarget(session, item string) GrantTarget {
	t := GrantTarget{Kind: GrantTargetItem, SessionID: session, ItemID: item}
	t.AuthorizationKey, _ = t.CanonicalKey()
	return t
}

func ObligationGrantTarget(session, obligation string, version uint64) GrantTarget {
	t := GrantTarget{Kind: GrantTargetObligation, SessionID: session, ObligationID: obligation, Version: version}
	t.AuthorizationKey, _ = t.CanonicalKey()
	return t
}

// ObligationRef is an exact version reference; expected revision belongs to
// the consuming intent, not this identity.
type ObligationRef struct {
	SessionID, ObligationID string
	Version                 uint64
}

func (r ObligationRef) Validate() error { return r.Target().Validate() }
func (r ObligationRef) Target() GrantTarget {
	return ObligationGrantTarget(r.SessionID, r.ObligationID, r.Version)
}

// Phase 3 actions never authorize a generic ItemChange. CompleteTask remains
// reserved and is deliberately excluded from delegable actions (P3-9/10/18).
const (
	ActionPromote                      Action = "promote"
	ActionDemote                       Action = "demote"
	ActionArchive                      Action = "archive"
	ActionUnarchive                    Action = "unarchive"
	ActionDeclareObligation            Action = "declare_obligation"
	ActionSetObligationMaterialization Action = "set_obligation_materialization"
)

func (a Action) Delegable() bool { return a.Valid() && a != ActionCompleteTask }

func (t MutationTarget) AuthorizationID() string {
	if t.Ref != (GrantTarget{}) {
		return t.Ref.AuthorizationKey
	}
	return t.ID
}

func grantNamesTarget(g MutationGrant, t MutationTarget) bool {
	if t.Ref != (GrantTarget{}) {
		if t.Ref.Validate() != nil {
			return false
		}
		for _, target := range g.Targets {
			if target == t.Ref {
				return true
			}
		}
		// Legacy occurrence grants retain meaning; stable obligation IDs do not.
		if t.Ref.Kind != GrantTargetItem {
			return false
		}
		for _, id := range g.TargetIDs {
			if id == t.Ref.ItemID {
				return true
			}
		}
		return false
	}
	for _, id := range g.TargetIDs {
		if id == t.ID {
			return true
		}
	}
	return false
}
