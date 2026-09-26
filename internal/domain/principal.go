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
// who can read an item, including from the archive (FR-DOM-003). It is a
// conjunction: a principal must be in the session and match every non-empty
// owner field. Scope sets the minimum constraints (TURN and TASK need a task,
// WORKFLOW a workflow, AGENT an agent); derived content may carry extra
// constraints so its boundary is no broader than the intersection of its
// sources (FR-REL-008), for example a checkpoint bound to one task and agent.
type AccessBoundary struct {
	Scope      Scope
	SessionID  string
	WorkflowID string
	TaskID     string
	AgentID    string
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

// Validate checks that the boundary carries the owners its scope requires.
func (b AccessBoundary) Validate() error {
	if !b.Scope.Valid() {
		return invalid("access boundary: invalid scope %q", b.Scope)
	}
	if b.SessionID == "" {
		return invalid("access boundary: session ID is required")
	}
	switch {
	case (b.Scope == ScopeTurn || b.Scope == ScopeTask) && b.TaskID == "":
		return invalid("access boundary: scope %s requires a task owner", b.Scope)
	case b.Scope == ScopeWorkflow && b.WorkflowID == "":
		return invalid("access boundary: scope %s requires a workflow owner", b.Scope)
	case b.Scope == ScopeAgent && b.AgentID == "":
		return invalid("access boundary: scope %s requires an agent owner", b.Scope)
	}
	return nil
}

// Permits reports whether principal p may read content inside b: same
// session and every owner constraint matches (FR-DOM-003). Access says
// nothing about context eligibility, which the planner checks separately.
func (b AccessBoundary) Permits(p Principal) bool {
	if b.Validate() != nil || p.SessionID != b.SessionID {
		return false
	}
	return matches(b.WorkflowID, p.WorkflowID) && matches(b.TaskID, p.TaskID) && matches(b.AgentID, p.AgentID)
}

func matches(constraint, actual string) bool { return constraint == "" || constraint == actual }

// Within reports whether b is no broader than outer: same session and b
// carries every owner constraint outer carries. Derived content must be
// Within each source boundary (FR-REL-008).
func (b AccessBoundary) Within(outer AccessBoundary) bool {
	if b.SessionID != outer.SessionID {
		return false
	}
	return (outer.WorkflowID == "" || outer.WorkflowID == b.WorkflowID) &&
		(outer.TaskID == "" || outer.TaskID == b.TaskID) &&
		(outer.AgentID == "" || outer.AgentID == b.AgentID)
}

// Intersect returns the narrowest boundary that is Within both a and b, with
// the given scope, or ok=false when no principal could satisfy both (for
// example, two different tasks or sessions).
func Intersect(scope Scope, a, b AccessBoundary) (AccessBoundary, bool) {
	if a.SessionID != b.SessionID {
		return AccessBoundary{}, false
	}
	out := AccessBoundary{Scope: scope, SessionID: a.SessionID}
	var ok bool
	if out.WorkflowID, ok = merge(a.WorkflowID, b.WorkflowID); !ok {
		return AccessBoundary{}, false
	}
	if out.TaskID, ok = merge(a.TaskID, b.TaskID); !ok {
		return AccessBoundary{}, false
	}
	if out.AgentID, ok = merge(a.AgentID, b.AgentID); !ok {
		return AccessBoundary{}, false
	}
	if out.Validate() != nil {
		return AccessBoundary{}, false
	}
	return out, true
}

func merge(x, y string) (string, bool) {
	switch {
	case x == "":
		return y, true
	case y == "" || x == y:
		return x, true
	}
	return "", false
}
