package domain

import (
	"fmt"
	"slices"
)

// Action is a lifecycle mutation subject to the common authorization matrix
// (FR-AUTH-001, ADR 16).
type Action string

const (
	ActionResolve           Action = "resolve"
	ActionUnpin             Action = "unpin"
	ActionReplaceDirective  Action = "replace_directive"
	ActionChangeScope       Action = "change_scope"
	ActionAssertObligation  Action = "assert_obligation" // satisfy or request revalidation
	ActionBlockObligation   Action = "block_obligation"
	ActionUnblockObligation Action = "unblock_obligation"
	ActionWaiveObligation   Action = "waive_obligation"
	ActionCompleteTask      Action = "complete_task"
)

// Valid reports whether a is a known action.
func (a Action) Valid() bool {
	switch a {
	case ActionResolve, ActionUnpin, ActionReplaceDirective, ActionChangeScope,
		ActionAssertObligation, ActionBlockObligation, ActionUnblockObligation,
		ActionWaiveObligation, ActionCompleteTask:
		return true
	}
	return false
}

// MutationGrant authorizes a named action on named targets (FR-AUTH-002).
// Exactly one grantee form is set: a registered matcher version, or a
// SYSTEM, HARNESS, or USER principal. Expiry and revocation are expressed in
// session sequence numbers so authorization never reads the wall clock.
type MutationGrant struct {
	ID        string
	SessionID string
	Action    Action
	TargetIDs []string
	Issuer    Principal
	// Grantee is matched on session, authority, and every non-empty task,
	// workflow, and agent field.
	Grantee      *Principal
	Matcher      *MatcherRef
	IssuedSeq    uint64
	ExpiresAtSeq uint64 // 0 means no expiry; the grant is valid through this sequence
	RevokedSeq   uint64 // 0 means not revoked; the grant is invalid from this sequence
}

// Clone returns a deep copy.
func (g MutationGrant) Clone() MutationGrant {
	g.TargetIDs = slices.Clone(g.TargetIDs)
	if g.Grantee != nil {
		p := *g.Grantee
		g.Grantee = &p
	}
	if g.Matcher != nil {
		m := *g.Matcher
		g.Matcher = &m
	}
	return g
}

// Validate checks structural rules. A grant can never be issued by, or to,
// AGENT, TOOL, or RETRIEVED_CONTENT.
func (g MutationGrant) Validate() error {
	if g.ID == "" || g.SessionID == "" || g.IssuedSeq == 0 {
		return invalid("grant: ID, session, and issued sequence are required")
	}
	if !g.Action.Valid() {
		return invalid("grant %s: invalid action %q", g.ID, g.Action)
	}
	if len(g.TargetIDs) == 0 {
		return invalid("grant %s: at least one target is required", g.ID)
	}
	if err := g.Issuer.Validate(); err != nil {
		return err
	}
	if g.Issuer.SessionID != g.SessionID {
		return invalid("grant %s: issuer belongs to another session", g.ID)
	}
	if !g.Issuer.Authority.CanHoldLifecycleAuthority() {
		return fmt.Errorf("grant %s: %w", g.ID, ErrInvalidAuthorityPromotion)
	}
	if (g.Grantee == nil) == (g.Matcher == nil) {
		return invalid("grant %s: exactly one of grantee principal or matcher is required", g.ID)
	}
	if g.Grantee != nil {
		if err := g.Grantee.Validate(); err != nil {
			return err
		}
		if g.Grantee.SessionID != g.SessionID {
			return invalid("grant %s: grantee belongs to another session", g.ID)
		}
		if !g.Grantee.Authority.CanHoldLifecycleAuthority() {
			return fmt.Errorf("grant %s: %w", g.ID, ErrInvalidAuthorityPromotion)
		}
	}
	if g.Matcher != nil && (g.Matcher.Name == "" || g.Matcher.Version == "") {
		return invalid("grant %s: matcher name and version are required", g.ID)
	}
	if g.ExpiresAtSeq != 0 && g.ExpiresAtSeq < g.IssuedSeq {
		return invalid("grant %s: expires before it was issued", g.ID)
	}
	return nil
}

// activeAt reports whether g is in force at sequence seq.
func (g MutationGrant) activeAt(seq uint64) bool {
	if seq < g.IssuedSeq {
		return false
	}
	if g.ExpiresAtSeq != 0 && seq > g.ExpiresAtSeq {
		return false
	}
	if g.RevokedSeq != 0 && seq >= g.RevokedSeq {
		return false
	}
	return true
}

func granteeMatches(g, actor Principal) bool {
	return g.SessionID == actor.SessionID && g.Authority == actor.Authority &&
		(g.TaskID == "" || g.TaskID == actor.TaskID) &&
		(g.WorkflowID == "" || g.WorkflowID == actor.WorkflowID) &&
		(g.AgentID == "" || g.AgentID == actor.AgentID)
}

// MutationTarget is one record a mutation affects, with the source authority
// and access boundary that govern it.
type MutationTarget struct {
	ID        string
	Authority Authority
	Access    AccessBoundary
}

// MutationRequest is the input to AuthorizeMutation.
type MutationRequest struct {
	Actor   Principal
	Action  Action
	Targets []MutationTarget
	// Matcher is set when a registered matcher evaluates the mutation under a
	// grant; the actor is then the trusted principal running the matcher.
	Matcher *MatcherRef
	// Grants are the grants in force for the session; AuthorizeMutation
	// selects applicable ones.
	Grants []MutationGrant
	// Seq is the session sequence at which the mutation would commit.
	Seq uint64
}

// Authorization reports which grant, if any, authorized each target.
type Authorization struct {
	GrantIDs map[string]string // target ID -> grant ID; absent means direct authority
}

// AuthorizeMutation applies the common authorization matrix (FR-AUTH-001,
// FR-AUTH-002). Every target must be accessible to the actor; an
// inaccessible target fails with ErrNotFound so callers cannot probe for
// existence. Each target is then authorized directly (the actor is SYSTEM,
// HARNESS, or USER with authority at least the target's) or by an in-force
// grant for this action and target, issued by a principal whose authority is
// at least the target's. The check is all-or-nothing.
func AuthorizeMutation(r MutationRequest) (Authorization, error) {
	auth := Authorization{GrantIDs: map[string]string{}}
	if err := r.Actor.Validate(); err != nil {
		return auth, err
	}
	if !r.Action.Valid() {
		return auth, invalid("mutation: invalid action %q", r.Action)
	}
	if len(r.Targets) == 0 {
		return auth, invalid("mutation: no targets")
	}
	for _, t := range r.Targets {
		if !t.Access.Permits(r.Actor) {
			return Authorization{}, ErrNotFound
		}
	}
	for _, t := range r.Targets {
		if r.Matcher == nil && r.Actor.Authority.CanHoldLifecycleAuthority() && r.Actor.Authority.AtLeast(t.Authority) {
			continue
		}
		grantID, ok := findGrant(r, t)
		if !ok {
			return Authorization{}, ErrInvalidAuthorityPromotion
		}
		auth.GrantIDs[t.ID] = grantID
	}
	return auth, nil
}

func findGrant(r MutationRequest, t MutationTarget) (string, bool) {
	// Deterministic: grants are considered in ID order.
	grants := slices.Clone(r.Grants)
	slices.SortFunc(grants, func(a, b MutationGrant) int {
		switch {
		case a.ID < b.ID:
			return -1
		case a.ID > b.ID:
			return 1
		}
		return 0
	})
	for _, g := range grants {
		if g.Validate() != nil || g.SessionID != r.Actor.SessionID || g.Action != r.Action || !g.activeAt(r.Seq) {
			continue
		}
		if !slices.Contains(g.TargetIDs, t.ID) || !g.Issuer.Authority.AtLeast(t.Authority) {
			continue
		}
		switch {
		case r.Matcher != nil:
			if g.Matcher == nil || *g.Matcher != *r.Matcher {
				continue
			}
			// The matcher runs under a trusted runtime principal; it never
			// lends lifecycle authority to AGENT, TOOL, or RETRIEVED_CONTENT.
			if !r.Actor.Authority.CanHoldLifecycleAuthority() {
				continue
			}
		case g.Grantee == nil || !granteeMatches(*g.Grantee, r.Actor):
			continue
		}
		return g.ID, true
	}
	return "", false
}

// AuthorizeSupersession checks FR-REL-006 for one SUPERSEDES edge created by
// actor: the actor can access both endpoints, the superseding item's
// authority is at least the superseded item's, both belong to the same
// session, and their access boundaries are equal, so a replacement can never
// hide an item from a principal who cannot see the replacement. Widening or
// narrowing an item's boundary needs an explicit authorized replacement
// policy (FR-DIR-002), which V1 does not provide.
func AuthorizeSupersession(actor Principal, superseding, superseded ContextItem) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	if !superseding.Access.Permits(actor) || !superseded.Access.Permits(actor) {
		return ErrNotFound
	}
	if superseding.SessionID != superseded.SessionID || superseding.Access != superseded.Access {
		return ErrInvalidAuthorityPromotion
	}
	if !actor.Authority.AtLeast(superseding.Authority) {
		return ErrInvalidAuthorityPromotion
	}
	if !superseding.Authority.AtLeast(superseded.Authority) {
		return ErrInvalidAuthorityPromotion
	}
	return nil
}
