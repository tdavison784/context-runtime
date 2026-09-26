package domain

// Resource reporting authority is independent of task ownership and lifecycle.
type ResourceBinding struct {
	SemanticMeta
	ResourceID string
	Owner      Principal
	Reporter   Principal
	Access     AccessBoundary
}

func (b ResourceBinding) Validate() error {
	if err := b.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !semanticID(b.ResourceID) {
		return invalid("resource binding: resource required")
	}
	for _, p := range []Principal{b.Owner, b.Reporter} {
		if err := semanticActor(b.SessionID, p); err != nil {
			return err
		}
		if p.Authority != AuthoritySystem && p.Authority != AuthorityHarness {
			return ErrInvalidAuthorityPromotion
		}
	}
	return semanticBoundary(b.SessionID, b.Access)
}

type ResourceFreshness string

const (
	ResourceKnown   ResourceFreshness = "KNOWN"
	ResourceUnknown ResourceFreshness = "UNKNOWN"
)

type ResourceState struct {
	SemanticMeta
	ResourceID, BindingID, LastUpdateID string
	AuthoritativeRevision, Revision     uint64
	WorkspaceFingerprint                string
	Freshness                           ResourceFreshness
}

func (s ResourceState) Validate() error {
	if err := s.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !semanticID(s.ResourceID) || !semanticID(s.BindingID) || !semanticID(s.LastUpdateID) || s.Revision == 0 || s.AuthoritativeRevision == 0 {
		return invalid("resource state: identity and revisions required")
	}
	if s.Freshness != ResourceKnown && s.Freshness != ResourceUnknown {
		return invalid("resource state: unknown freshness")
	}
	if s.Freshness == ResourceKnown && !ValidHash(s.WorkspaceFingerprint) || s.Freshness == ResourceUnknown && s.WorkspaceFingerprint != "" {
		return invalid("resource state: fingerprint disagrees with freshness")
	}
	return nil
}

type ResourceUpdate struct {
	SemanticMeta
	ResourceID, RequestID                                         string
	Reporter                                                      Principal
	ExpectedAuthoritativeRevision, ResultingAuthoritativeRevision uint64
	WorkspaceFingerprint                                          string
	Freshness                                                     ResourceFreshness
	Resynchronization                                             bool
	AllPaths                                                      bool
	ChangedPaths                                                  []string // canonical resource-relative paths, sorted unique
}

func (u ResourceUpdate) Clone() ResourceUpdate {
	u.ChangedPaths = append([]string(nil), u.ChangedPaths...)
	return u
}
func (u ResourceUpdate) Validate() error {
	if err := u.SemanticMeta.Validate(); err != nil {
		return err
	}
	if !semanticID(u.ResourceID) || !semanticID(u.RequestID) || u.ResultingAuthoritativeRevision <= u.ExpectedAuthoritativeRevision || !sortedUnique(u.ChangedPaths) || u.AllPaths && len(u.ChangedPaths) > 0 {
		return invalid("resource update: invalid revision or change coverage")
	}
	if err := semanticActor(u.SessionID, u.Reporter); err != nil {
		return err
	}
	if u.Reporter.Authority != AuthoritySystem && u.Reporter.Authority != AuthorityHarness {
		return ErrInvalidAuthorityPromotion
	}
	for _, p := range u.ChangedPaths {
		c, err := cleanResourcePath(p, false)
		if err != nil || c != p {
			return invalid("resource update: noncanonical path")
		}
	}
	if u.Freshness != ResourceKnown && u.Freshness != ResourceUnknown || u.Freshness == ResourceKnown && !ValidHash(u.WorkspaceFingerprint) || u.Freshness == ResourceUnknown && (u.WorkspaceFingerprint != "" || !u.AllPaths) {
		return invalid("resource update: invalid freshness")
	}
	if !u.Resynchronization && u.ResultingAuthoritativeRevision-u.ExpectedAuthoritativeRevision != 1 && u.Freshness != ResourceUnknown {
		return invalid("resource update: gap must become unknown")
	}
	return nil
}
