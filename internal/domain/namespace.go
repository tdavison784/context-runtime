package domain

// DirectiveNamespace separates parsed directive IDs from keyed agent-state
// IDs in the current-version key (M6, R6). FR-DIR-006 allows a directive ID
// such as "agent.status", so the "agent." display prefix of AgentKeyID
// cannot by itself keep keyed agent writes (FR-TOOL-002) from colliding with
// directives; the namespace does.
type DirectiveNamespace string

const (
	NamespaceDirective DirectiveNamespace = "DIRECTIVE"
	NamespaceAgentKey  DirectiveNamespace = "AGENT_KEY"
)

// Valid reports whether n is a known namespace.
func (n DirectiveNamespace) Valid() bool { return n == NamespaceDirective || n == NamespaceAgentKey }

// CurrentKey is the complete identity of one current version: (session,
// task, access boundary, namespace, ID) (FR-DIR-002, FR-TOOL-002). Two keys
// that differ in any field name independent current versions. Lifecycle
// commands (Resolve, Unpin) resolve only NamespaceDirective keys.
type CurrentKey struct {
	SessionID string
	TaskID    string
	Access    AccessBoundary
	Namespace DirectiveNamespace
	ID        string
}

// Validate checks the key's structure.
func (k CurrentKey) Validate() error {
	if k.SessionID == "" || !k.Namespace.Valid() || !ValidDirectiveID(k.ID) {
		return invalid("current key: session, namespace, and a valid ID are required")
	}
	if err := k.Access.Validate(); err != nil {
		return err
	}
	if k.Access.SessionID != k.SessionID || (k.Access.TaskID != "" && k.Access.TaskID != k.TaskID) {
		return invalid("current key: access boundary disagrees with session or task")
	}
	return nil
}

// DirectiveNamespace derives the namespace of an item's DirectiveID. Parsed
// directive items are exactly the items with a directive Section, which only
// trusted or marked spans can carry (FR-ING-004); an ID without a Section is
// keyed agent state. ok is false when the item has no DirectiveID.
func (it ContextItem) DirectiveNamespace() (DirectiveNamespace, bool) {
	switch {
	case it.DirectiveID == "":
		return "", false
	case it.Section != SectionNone:
		return NamespaceDirective, true
	}
	return NamespaceAgentKey, true
}

// CurrentKey returns the item's current-version key, or ok=false when the
// item has no DirectiveID.
func (it ContextItem) CurrentKey() (CurrentKey, bool) {
	ns, ok := it.DirectiveNamespace()
	if !ok {
		return CurrentKey{}, false
	}
	return CurrentKey{SessionID: it.SessionID, TaskID: it.TaskID, Access: it.Access, Namespace: ns, ID: it.DirectiveID}, true
}
