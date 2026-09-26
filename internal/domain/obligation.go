package domain

import "slices"

// ObligationStatus is the status of one obligation version (FR-OBL-002).
type ObligationStatus string

const (
	ObligationUnresolved ObligationStatus = "UNRESOLVED"
	ObligationSatisfied  ObligationStatus = "SATISFIED"
	ObligationBlocked    ObligationStatus = "BLOCKED"
	ObligationWaived     ObligationStatus = "WAIVED"
)

// Valid reports whether s is a known obligation status.
func (s ObligationStatus) Valid() bool {
	switch s {
	case ObligationUnresolved, ObligationSatisfied, ObligationBlocked, ObligationWaived:
		return true
	}
	return false
}

// ValidObligationTransition reports whether from -> to is allowed by
// FR-OBL-002. It checks shape only; authorization is AuthorizeMutation's job.
func ValidObligationTransition(from, to ObligationStatus) bool {
	if !from.Valid() || !to.Valid() || from == ObligationWaived {
		return false
	}
	switch {
	case to == ObligationWaived:
		return true
	case from == ObligationUnresolved:
		return to == ObligationSatisfied || to == ObligationBlocked
	case from == ObligationSatisfied:
		return to == ObligationUnresolved
	case from == ObligationBlocked:
		return to == ObligationUnresolved
	}
	return false
}

// MatcherRef names a registered deterministic matcher version (FR-OBL-004).
type MatcherRef struct {
	Name    string
	Version string
}

// ObligationVersion is one version of an obligation (FR-OBL-001,
// FR-OBL-006). Replacing the source directive retires the version and creates
// a new one that starts UNRESOLVED. Status history lives in
// ObligationTransition records; Status caches the latest.
type ObligationVersion struct {
	ObligationID    string
	Version         uint64
	SessionID       string
	TaskID          string
	SourceItemID    string
	SourceAuthority Authority
	Access          AccessBoundary
	Description     string
	Matcher         *MatcherRef
	Status          ObligationStatus
	Current         bool
	EvidenceIDs     []string
	CreatedSeq      uint64
	RetiredSeq      uint64
	// MaterializationDisabled records an authorized FR-OBL-003 exception.
	// It never satisfies, waives, or permits completion.
	MaterializationDisabled bool
	// Revision increments with every change and is used for compare-and-swap.
	Revision uint64
}

// Clone returns a deep copy.
func (o ObligationVersion) Clone() ObligationVersion {
	o.EvidenceIDs = slices.Clone(o.EvidenceIDs)
	if o.Matcher != nil {
		m := *o.Matcher
		o.Matcher = &m
	}
	return o
}

// Validate checks structural rules.
func (o ObligationVersion) Validate() error {
	if o.ObligationID == "" || o.SessionID == "" || o.SourceItemID == "" {
		return invalid("obligation: ID, session, and source item are required")
	}
	if o.Version == 0 || o.Revision == 0 || o.CreatedSeq == 0 {
		return invalid("obligation %s: version, revision, and created sequence start at 1", o.ObligationID)
	}
	if !o.Status.Valid() {
		return invalid("obligation %s: invalid status %q", o.ObligationID, o.Status)
	}
	if !o.SourceAuthority.Valid() {
		return invalid("obligation %s: invalid source authority %q", o.ObligationID, o.SourceAuthority)
	}
	if err := o.Access.Validate(); err != nil {
		return err
	}
	if o.Access.SessionID != o.SessionID {
		return invalid("obligation %s: access boundary belongs to another session", o.ObligationID)
	}
	if o.Current == (o.RetiredSeq != 0) {
		return invalid("obligation %s: retired sequence disagrees with currentness", o.ObligationID)
	}
	return nil
}

// ObligationTransition is the append-only record of one status change.
type ObligationTransition struct {
	ID           string
	SessionID    string
	ObligationID string
	Version      uint64
	Seq          uint64
	From         ObligationStatus
	To           ObligationStatus
	// Action is the authorized lifecycle action that produced the
	// transition; it must be the one TransitionAction(From, To) requires,
	// so a mutation authorized as one action cannot be stored as another.
	Action       Action
	Actor        Principal
	GrantID      string
	Matcher      *MatcherRef
	EvidenceIDs  []string
	Fingerprints []string // applicability fingerprints of the evidence
	Reason       string
}

// Clone returns a deep copy.
func (t ObligationTransition) Clone() ObligationTransition {
	t.EvidenceIDs = slices.Clone(t.EvidenceIDs)
	t.Fingerprints = slices.Clone(t.Fingerprints)
	if t.Matcher != nil {
		m := *t.Matcher
		t.Matcher = &m
	}
	return t
}

// TransitionAction returns the lifecycle action that authorizes from -> to
// (FR-OBL-002): satisfaction and revalidation are assertions, blocking and
// unblocking are their own actions, and any status may be waived.
func TransitionAction(from, to ObligationStatus) (Action, bool) {
	if !ValidObligationTransition(from, to) {
		return "", false
	}
	switch {
	case to == ObligationWaived:
		return ActionWaiveObligation, true
	case to == ObligationBlocked:
		return ActionBlockObligation, true
	case from == ObligationBlocked:
		return ActionUnblockObligation, true
	}
	return ActionAssertObligation, true
}

// Validate checks structural rules including the transition table. The
// actor must be a SYSTEM, HARNESS, or USER principal in the transition's
// session: AGENT, TOOL, and RETRIEVED_CONTENT never change obligation
// status, and a matcher runs under a trusted principal (FR-OBL-002).
func (t ObligationTransition) Validate() error {
	if t.ID == "" || t.SessionID == "" || t.ObligationID == "" || t.Version == 0 || t.Seq == 0 {
		return invalid("obligation transition: ID, session, obligation, version, and sequence are required")
	}
	if err := t.Actor.Validate(); err != nil {
		return err
	}
	if t.Actor.SessionID != t.SessionID {
		return invalid("obligation transition %s: actor belongs to another session", t.ID)
	}
	if !t.Actor.Authority.CanHoldLifecycleAuthority() {
		return ErrInvalidAuthorityPromotion
	}
	want, ok := TransitionAction(t.From, t.To)
	if !ok {
		return ErrInvalidTransition
	}
	if t.Action != want {
		return invalid("obligation transition %s: %s -> %s requires action %s, not %q", t.ID, t.From, t.To, want, t.Action)
	}
	if t.Matcher != nil && t.Action != ActionAssertObligation {
		return invalid("obligation transition %s: a matcher may only assert", t.ID)
	}
	if t.To == ObligationSatisfied && len(t.EvidenceIDs) == 0 && t.Matcher != nil {
		return invalid("obligation transition %s: matcher satisfaction requires evidence", t.ID)
	}
	return nil
}
