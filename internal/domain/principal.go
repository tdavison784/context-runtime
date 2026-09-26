package domain

// Principal is the session, workflow, task, and agent a caller acts for, plus
// the caller's authority. Every API call carries one. The embedding process
// authenticates it; the runtime never trusts model-authored identifiers.
type Principal struct {
	SessionID  string
	WorkflowID string
	TaskID     string
	AgentID    string
	Authority  Authority
}

// Validate checks that p names a session and a known authority.
func (p Principal) Validate() error {
	if p.SessionID == "" {
		return invalid("principal: session ID is required")
	}
	if !p.Authority.Valid() {
		return invalid("principal: invalid authority %q", p.Authority)
	}
	return nil
}

// AccessBoundary is the immutable set of ownership constraints that govern
// who can read an item, including from the archive (FR-DOM-003). The owner
// fields that the scope does not use must be empty.
type AccessBoundary struct {
	Scope      Scope
	SessionID  string
	WorkflowID string // WORKFLOW scope
	TaskID     string // TURN and TASK scope
	AgentID    string // AGENT scope
}

// BoundaryFor returns the access boundary an item of scope s receives when
// ingested by principal p.
func BoundaryFor(s Scope, p Principal) AccessBoundary {
	b := AccessBoundary{Scope: s, SessionID: p.SessionID}
	switch s {
	case ScopeTurn, ScopeTask:
		b.TaskID = p.TaskID
	case ScopeWorkflow:
		b.WorkflowID = p.WorkflowID
	case ScopeAgent:
		b.AgentID = p.AgentID
	}
	return b
}

// Validate checks that the boundary names exactly the owners its scope needs.
func (b AccessBoundary) Validate() error {
	if !b.Scope.Valid() {
		return invalid("access boundary: invalid scope %q", b.Scope)
	}
	if b.SessionID == "" {
		return invalid("access boundary: session ID is required")
	}
	needTask := b.Scope == ScopeTurn || b.Scope == ScopeTask
	needWorkflow := b.Scope == ScopeWorkflow
	needAgent := b.Scope == ScopeAgent
	if needTask != (b.TaskID != "") {
		return invalid("access boundary: scope %s task owner mismatch", b.Scope)
	}
	if needWorkflow != (b.WorkflowID != "") {
		return invalid("access boundary: scope %s workflow owner mismatch", b.Scope)
	}
	if needAgent != (b.AgentID != "") {
		return invalid("access boundary: scope %s agent owner mismatch", b.Scope)
	}
	return nil
}

// Permits reports whether principal p may read content inside b. Access
// always requires the same session; TURN and TASK also require the same
// task, WORKFLOW the same workflow, and AGENT the same agent (FR-DOM-003).
// Access says nothing about context eligibility, which the planner checks
// separately.
func (b AccessBoundary) Permits(p Principal) bool {
	if b.SessionID == "" || p.SessionID != b.SessionID {
		return false
	}
	switch b.Scope {
	case ScopeTurn, ScopeTask:
		return p.TaskID != "" && p.TaskID == b.TaskID
	case ScopeWorkflow:
		return p.WorkflowID != "" && p.WorkflowID == b.WorkflowID
	case ScopeAgent:
		return p.AgentID != "" && p.AgentID == b.AgentID
	case ScopeSession:
		return true
	}
	return false
}

// Within reports whether every principal b permits is also permitted by
// outer: b is no broader than outer. Derived content must satisfy Within for
// each source boundary (FR-REL-008).
func (b AccessBoundary) Within(outer AccessBoundary) bool {
	if b.SessionID != outer.SessionID {
		return false
	}
	switch outer.Scope {
	case ScopeSession:
		return true
	case ScopeWorkflow:
		return b.Scope == ScopeWorkflow && b.WorkflowID == outer.WorkflowID
	case ScopeAgent:
		return b.Scope == ScopeAgent && b.AgentID == outer.AgentID
	case ScopeTask, ScopeTurn:
		return (b.Scope == ScopeTask || b.Scope == ScopeTurn) && b.TaskID == outer.TaskID
	}
	return false
}
