package domain

import (
	"fmt"
	"slices"
	"strings"
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
		ActionWaiveObligation, ActionCompleteTask, ActionPromote, ActionDemote,
		ActionArchive, ActionUnarchive, ActionDeclareObligation, ActionSetObligationMaterialization:
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
	TargetIDs []string      // legacy v1; never authorizes a typed obligation target
	Targets   []GrantTarget // v2 decoded targets; mutually exclusive with TargetIDs
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
	g.Targets = slices.Clone(g.Targets)
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
	if len(g.Targets) != 0 {
		if len(g.TargetIDs) != 0 || !g.Action.Delegable() {
			return invalid("grant: mixed schemas or reserved action")
		}
		seen := map[string]bool{}
		for _, target := range g.Targets {
			if target.Validate() != nil || target.SessionID != g.SessionID || !g.Action.ValidForTarget(target.Kind) || g.Matcher != nil && target.Kind != GrantTargetObligation || seen[target.AuthorizationKey] {
				return invalid("grant: invalid or duplicate typed target")
			}
			seen[target.AuthorizationKey] = true
		}
	}
	if len(g.TargetIDs) == 0 && len(g.Targets) == 0 {
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
	// A matcher only evaluates its obligation (FR-AUTH-002, FR-OBL-004); it
	// can never block, unblock, waive, resolve, or complete.
	if g.Matcher != nil && g.Action != ActionAssertObligation {
		return invalid("grant %s: matcher grants are limited to %s", g.ID, ActionAssertObligation)
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
	Ref       GrantTarget // zero only on the frozen legacy path
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
		if t.Ref != (GrantTarget{}) && (t.Ref.Validate() != nil || t.Ref.SessionID != r.Actor.SessionID || r.Seq == 0 || r.Action == ActionCompleteTask) {
			return Authorization{}, ErrInvalidRecord
		}
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
		auth.GrantIDs[t.AuthorizationID()] = grantID
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
		// The issuer must still be able to act on the target directly: a
		// grant never carries authority its issuer lacks, including access
		// to a target outside the issuer's boundary.
		if !grantNamesTarget(g, t) || !g.Issuer.Authority.AtLeast(t.Authority) || !t.Access.Permits(g.Issuer) {
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

// AuthorizeGrantIssuance checks that g's issuer may issue it for targets,
// which must be exactly the records named by g.TargetIDs, each once
// (FR-AUTH-002). Callers load targets from the store in the issuing
// transaction. The issuer must access every target (ErrNotFound otherwise,
// checked before authority so issuance cannot probe for existence) and hold
// authority at least each target's.
func AuthorizeGrantIssuance(g MutationGrant, targets []MutationTarget) error {
	if err := g.Validate(); err != nil {
		return err
	}
	if err := sameTargetSet(g, targets); err != nil {
		return err
	}
	return authorizeOver(g.Issuer, targets)
}

// AuthorizeGrantRevocation checks that actor may revoke g: actor must be
// SYSTEM, HARNESS, or USER in the grant's session, access every target, and
// hold authority at least each target's (FR-AUTH-002). targets must be
// exactly the records g names.
func AuthorizeGrantRevocation(actor Principal, g MutationGrant, targets []MutationTarget) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	if actor.SessionID != g.SessionID {
		return ErrNotFound
	}
	if err := sameTargetSet(g, targets); err != nil {
		return err
	}
	if !actor.Authority.CanHoldLifecycleAuthority() {
		for _, t := range targets {
			if !t.Access.Permits(actor) {
				return ErrNotFound
			}
		}
		return ErrInvalidAuthorityPromotion
	}
	return authorizeOver(actor, targets)
}

func sameTargetSet(g MutationGrant, targets []MutationTarget) error {
	named := map[string]bool{}
	ids := slices.Clone(g.TargetIDs)
	for _, target := range g.Targets {
		ids = append(ids, target.AuthorizationKey)
	}
	for _, id := range ids {
		if named[id] {
			return invalid("grant %s: duplicate target %s", g.ID, id)
		}
		named[id] = true
	}
	if len(targets) != len(named) {
		return invalid("grant %s: targets do not match target IDs", g.ID)
	}
	seen := map[string]bool{}
	for _, t := range targets {
		if t.Ref != (GrantTarget{}) && t.Ref.Validate() != nil {
			return ErrInvalidRecord
		}
		if !named[t.AuthorizationID()] || seen[t.AuthorizationID()] {
			return invalid("grant %s: targets do not match target IDs", g.ID)
		}
		seen[t.AuthorizationID()] = true
	}
	return nil
}

// authorizeOver checks access to every target before any authority check.
func authorizeOver(p Principal, targets []MutationTarget) error {
	for _, t := range targets {
		if !t.Access.Permits(p) {
			return ErrNotFound
		}
	}
	for _, t := range targets {
		if !p.Authority.AtLeast(t.Authority) {
			return ErrInvalidAuthorityPromotion
		}
	}
	return nil
}

// AuthorizeSupersession checks FR-REL-006 for one SUPERSEDES edge created by
// actor: the actor can access both endpoints, the superseding item's
// authority is at least the superseded item's, both belong to the same
// session, and their access boundaries are equal, so a replacement can never
// hide an item from a principal who cannot see the replacement. Widening or
// narrowing an item's boundary needs an explicit authorized replacement
// policy (FR-DIR-002), which V1 does not provide.
//
// TOOL and RETRIEVED_CONTENT actors never create SUPERSEDES edges: tool
// output cannot suppress state (section 9). Deterministic observation rules
// (FR-REL-007) run under a trusted SYSTEM or HARNESS principal. An AGENT
// actor may supersede only a keyed agent write with the same key in the
// same task (FR-TOOL-002): both items AGENT authority with the same
// "agent." directive ID and task. Access is checked before anything else, so an inaccessible endpoint
// always yields ErrNotFound and never reveals its authority.
func AuthorizeSupersession(actor Principal, superseding, superseded ContextItem) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	if !superseding.Access.Permits(actor) || !superseded.Access.Permits(actor) {
		return ErrNotFound
	}
	if superseding.Namespace == NamespaceObservation || superseded.Namespace == NamespaceObservation {
		if actor.Authority != AuthoritySystem && actor.Authority != AuthorityHarness || superseding.Namespace != NamespaceObservation || superseded.Namespace != NamespaceObservation || superseding.Authority != AuthorityTool || superseded.Authority != AuthorityTool || superseding.DirectiveID != superseded.DirectiveID || superseding.TaskID != superseded.TaskID {
			return ErrInvalidAuthorityPromotion
		}
	}
	switch actor.Authority {
	case AuthoritySystem, AuthorityHarness, AuthorityUser:
	case AuthorityAgent:
		if superseding.Authority != AuthorityAgent || superseded.Authority != AuthorityAgent ||
			!strings.HasPrefix(superseding.DirectiveID, AgentKeyID("")) ||
			superseding.DirectiveID != superseded.DirectiveID ||
			superseding.TaskID != superseded.TaskID {
			return ErrInvalidAuthorityPromotion
		}
	default:
		return ErrInvalidAuthorityPromotion
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
