package domain

import (
	"slices"
	"time"
)

// Coverage is the source coverage of derived content or opaque provider
// state (FR-REL-008): the contiguous range of committed sequence numbers in a
// conversation that the content summarizes or was derived from, and the
// explicit IDs of the covered items. Dispatch rechecks every covered item's
// access, turn/task/TTL eligibility, and lease before inherited content is
// transmitted (ADR 6), so the IDs must be complete; a range alone cannot
// show which items lost eligibility.
type Coverage struct {
	ConversationID string
	FromSeq        uint64
	ToSeq          uint64
	ItemIDs        []string // sorted, unique
}

// Relationship is a directed edge. FromID relates to ToID by Type: a new
// version SUPERSEDES an old one, a derived item is DERIVED_FROM its source, a
// tool result DEPENDS_ON its tool call, and a duplicate is DUPLICATE_OF its
// canonical item. Relationship records are the only store of edges.
type Relationship struct {
	ID          string
	SessionID   string
	Type        RelationshipType
	FromID      string
	ToID        string
	Seq         uint64
	Authority   Authority // authority of the mutation that created the edge
	EventID     string
	RuleVersion string // deterministic rule that produced the edge, if any
	CoverageID  string // normalized Phase 3 coverage; mutually exclusive with legacy Coverage
	Coverage    *Coverage
}

// Clone returns a deep copy.
func (r Relationship) Clone() Relationship {
	if r.Coverage != nil {
		c := *r.Coverage
		c.ItemIDs = slices.Clone(c.ItemIDs)
		r.Coverage = &c
	}
	return r
}

// Validate checks structural rules; stores additionally check endpoints.
func (r Relationship) Validate() error {
	if r.ID == "" || r.SessionID == "" {
		return invalid("relationship: ID and session ID are required")
	}
	if !r.Type.Valid() {
		return invalid("relationship %s: invalid type %q", r.ID, r.Type)
	}
	if r.FromID == "" || r.ToID == "" {
		return invalid("relationship %s: both endpoints are required", r.ID)
	}
	if r.FromID == r.ToID {
		if r.Type == RelSupersedes {
			return ErrSupersessionCycle
		}
		return invalid("relationship %s: self edge", r.ID)
	}
	if r.Seq == 0 {
		return invalid("relationship %s: sequence number is required", r.ID)
	}
	if !r.Authority.Valid() {
		return invalid("relationship %s: invalid authority %q", r.ID, r.Authority)
	}
	if r.CoverageID != "" && r.Coverage != nil {
		return invalid("relationship: mixed coverage schemas")
	}
	if r.Coverage != nil {
		if r.Coverage.FromSeq > r.Coverage.ToSeq {
			return invalid("relationship %s: inverted coverage range", r.ID)
		}
		if !slices.IsSorted(r.Coverage.ItemIDs) || len(slices.Compact(slices.Clone(r.Coverage.ItemIDs))) != len(r.Coverage.ItemIDs) {
			return invalid("relationship %s: coverage item IDs must be sorted and unique", r.ID)
		}
	}
	return nil
}

// Blob is an immutable content-addressed snapshot (FR-ING-007). Blobs are
// scoped to a session so their existence cannot be probed across sessions.
type Blob struct {
	SessionID string
	Hash      string
	MediaType string
	Data      []byte
}

// Validate verifies the blob's bytes against its hash.
func (b Blob) Validate() error {
	if b.SessionID == "" {
		return invalid("blob: session ID is required")
	}
	if !ValidHash(b.Hash) {
		return invalid("blob: malformed hash")
	}
	if HashBytes(b.Data) != b.Hash {
		return ErrIntegrity
	}
	return nil
}

// EventRecord is the idempotency record for a caller-supplied event ID
// (FR-ING-006). A repeated event ID with the same fingerprint returns the
// original result; any difference is ErrEventIDConflict.
type EventRecord struct {
	RequestHashVersion string // empty is legacy v2; unknown without a replayable envelope
	SessionID          string
	EventID            string
	Principal          Principal
	PayloadHash        string // canonical payload hash
	SourceHash         string // canonical source hash; empty when the event has no source
	Seq                uint64
	ItemIDs            []string // items the event created, in creation order
	CommittedAt        time.Time
}

// SameRequest reports whether e and other describe the same logical event.
func (e EventRecord) SameRequest(other EventRecord) bool {
	return e.SessionID == other.SessionID && e.EventID == other.EventID &&
		e.Principal == other.Principal && e.PayloadHash == other.PayloadHash &&
		e.SourceHash == other.SourceHash
}

// Clone returns a deep copy.
func (e EventRecord) Clone() EventRecord {
	e.ItemIDs = slices.Clone(e.ItemIDs)
	return e
}

// Validate checks structural rules.
func (e EventRecord) Validate() error {
	if e.RequestHashVersion != "" && e.RequestHashVersion != RequestHashV2 && e.RequestHashVersion != RequestHashV3 && e.RequestHashVersion != "unknown" {
		return ErrUnsupportedSchema
	}
	if e.SessionID == "" || e.EventID == "" {
		return invalid("event: session and event IDs are required")
	}
	if err := e.Principal.Validate(); err != nil {
		return err
	}
	if e.Principal.SessionID != e.SessionID {
		return invalid("event %s: principal belongs to another session", e.EventID)
	}
	if !ValidHash(e.PayloadHash) {
		return invalid("event %s: malformed payload hash", e.EventID)
	}
	if e.SourceHash != "" && !ValidHash(e.SourceHash) {
		return invalid("event %s: malformed source hash", e.EventID)
	}
	if e.Seq == 0 {
		return invalid("event %s: sequence number is required", e.EventID)
	}
	return nil
}

// TaskState is the lifecycle state of a task. Turn counts turns from 1; it
// advances when a USER event or HARNESS turn-boundary event is ingested.
type TaskState struct {
	SessionID    string
	TaskID       string
	WorkflowID   string
	Status       TaskStatus
	Turn         uint64
	TurnID       string
	CompletedSeq uint64
	Version      uint64
}

// Validate checks structural rules.
func (t TaskState) Validate() error {
	if t.SessionID == "" || t.TaskID == "" {
		return invalid("task: session and task IDs are required")
	}
	if !t.Status.Valid() {
		return invalid("task %s: invalid status %q", t.TaskID, t.Status)
	}
	if (t.Status == TaskCompleted) != (t.CompletedSeq != 0) {
		return invalid("task %s: completion sequence disagrees with status", t.TaskID)
	}
	if t.Version == 0 {
		return invalid("task %s: version must start at 1", t.TaskID)
	}
	return nil
}

// TargetKind names the record a lifecycle event changed.
type TargetKind string

const (
	TargetItem       TargetKind = "item"
	TargetObligation TargetKind = "obligation"
	TargetTask       TargetKind = "task"
	TargetGrant      TargetKind = "grant"
	TargetCall       TargetKind = "call"
	TargetDirective  TargetKind = "directive"
)

// LifecycleEvent is the append-only audit record of one lifecycle change.
type LifecycleEvent struct {
	ID          string
	SessionID   string
	Seq         uint64
	TargetKind  TargetKind
	TargetID    string
	Action      string
	From        string
	To          string
	Actor       Principal
	GrantID     string
	EventID     string
	Reason      string
	PayloadHash string // hash of an audited payload stored as a blob, if any
}

// Validate checks structural rules.
func (e LifecycleEvent) Validate() error {
	if e.ID == "" || e.SessionID == "" || e.TargetID == "" || e.Action == "" {
		return invalid("lifecycle event: ID, session, target, and action are required")
	}
	if e.Seq == 0 {
		return invalid("lifecycle event %s: sequence number is required", e.ID)
	}
	if err := e.Actor.Validate(); err != nil {
		return err
	}
	if e.Actor.SessionID != e.SessionID {
		return invalid("lifecycle event %s: actor belongs to another session", e.ID)
	}
	if e.PayloadHash != "" && !ValidHash(e.PayloadHash) {
		return invalid("lifecycle event %s: malformed payload hash", e.ID)
	}
	return nil
}

// ItemChange is an audited change to an item's mutable lifecycle fields
// (FR-DOM-008). Nil fields are unchanged.
type ItemChange struct {
	Generation   *Generation
	Residency    *Residency
	GoalStatus   *GoalStatus
	Retention    *RetentionClass
	LastUsedCall *uint64
	AccessDelta  int
}

// Apply returns it with the change applied and Version incremented. Goal
// status moves only from OPEN to RESOLVED: reopening requires an authorized
// replacement with a new OPEN version (FR-DIR-005). Residency changes never
// alter goal status (FR-DOM-005).
func (c ItemChange) Apply(it ContextItem) (ContextItem, error) {
	out := it.Clone()
	if c.Generation != nil {
		if !c.Generation.Valid() {
			return it, invalid("item %s: invalid generation %q", it.ID, *c.Generation)
		}
		out.Generation = *c.Generation
	}
	if c.Residency != nil {
		if !c.Residency.Valid() {
			return it, invalid("item %s: invalid residency %q", it.ID, *c.Residency)
		}
		out.Residency = *c.Residency
	}
	if c.GoalStatus != nil {
		if it.GoalStatus == nil {
			return it, ErrInvalidTransition
		}
		from, to := *it.GoalStatus, *c.GoalStatus
		if !to.Valid() || (from != to && !(from == GoalOpen && to == GoalResolved)) {
			return it, ErrInvalidTransition
		}
		out.GoalStatus = &to
	}
	if c.Retention != nil {
		if !c.Retention.Valid() {
			return it, invalid("item %s: invalid retention %q", it.ID, *c.Retention)
		}
		out.Retention = *c.Retention
	}
	if c.LastUsedCall != nil {
		if *c.LastUsedCall < it.LastUsedCall {
			return it, ErrInvalidTransition
		}
		out.LastUsedCall = *c.LastUsedCall
	}
	if c.AccessDelta < 0 {
		return it, ErrInvalidTransition
	}
	out.AccessCount += c.AccessDelta
	out.Version++
	return out, nil
}
