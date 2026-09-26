package domain

// DirectiveNamespace separates parsed directive IDs from keyed agent-state
// IDs in the current-version key (M6, R6). FR-DIR-006 allows a directive ID
// such as "agent.status", so the "agent." display prefix of AgentKeyID
// cannot by itself keep keyed agent writes (FR-TOOL-002) from colliding with
// directives; the namespace does.
type DirectiveNamespace string

const (
	NamespaceDirective   DirectiveNamespace = "DIRECTIVE"
	NamespaceAgentKey    DirectiveNamespace = "AGENT_KEY"
	NamespaceObservation DirectiveNamespace = "OBSERVATION"
)

// Valid reports whether n is a known namespace.
func (n DirectiveNamespace) Valid() bool {
	return n == NamespaceDirective || n == NamespaceAgentKey || n == NamespaceObservation
}

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
	if k.Namespace == NamespaceObservation && !ValidSubjectKey(k.ID) {
		return invalid("observation current key: subject digest required")
	}
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

// DirectiveNamespace uses the explicit immutable namespace when present.
// The zero-value fallback is frozen legacy decoding only (P3-3/41). Parsed
// directive items are exactly the items with a directive Section, which only
// trusted or marked spans can carry (FR-ING-004); an ID without a Section is
// keyed agent state. ok is false when the item has no DirectiveID.
func (it ContextItem) DirectiveNamespace() (DirectiveNamespace, bool) {
	if it.Namespace != "" {
		return it.Namespace, it.DirectiveID != "" && it.Namespace.Valid()
	}
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

// ValidateSemantic is required for new semantic writes. Migration may decode
// legacy items with Validate, but never invent an OBSERVATION namespace.
func (it ContextItem) ValidateSemantic() error {
	if err := it.Validate(); err != nil {
		return err
	}
	if it.DirectiveID != "" && it.Namespace == "" {
		return invalid("semantic item: explicit namespace required")
	}
	return nil
}

func (it ContextItem) validateNamespace() error {
	key, ok := it.CurrentKey()
	if !ok {
		return invalid("semantic item: namespace without key")
	}
	if err := key.Validate(); err != nil {
		return err
	}
	switch it.Namespace {
	case NamespaceDirective:
		if it.Section == SectionNone || !it.Authority.CanHoldLifecycleAuthority() {
			return invalid("directive namespace: section and trusted authority required")
		}
	case NamespaceAgentKey:
		if it.Section != SectionNone || it.Authority != AuthorityAgent || it.AgentID == "" || it.Access.AgentID != it.AgentID || it.Access.TaskID != it.TaskID || it.TaskID == "" || it.Scope != ScopeTask {
			return invalid("agent namespace: exact task/agent ownership required")
		}
	case NamespaceObservation:
		if it.Section != SectionNone || it.Authority != AuthorityTool || it.Kind != KindTaskState || it.TaskID == "" {
			return invalid("observation namespace: TOOL task state required")
		}
	default:
		return invalid("semantic item: unknown namespace")
	}
	return nil
}

func (k CurrentKey) CanonicalHash() (string, error) {
	if err := k.Validate(); err != nil {
		return "", err
	}
	e := NewCanonicalEncoder("context-runtime/current-key/v2").String(k.SessionID).String(k.TaskID)
	encodeBoundary(e, k.Access)
	return e.String(string(k.Namespace)).String(k.ID).Hash(), nil
}
