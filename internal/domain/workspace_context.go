package domain

type WorkspaceContextKind string

const (
	WorkspaceSource       WorkspaceContextKind = "SOURCE"
	WorkspaceTask         WorkspaceContextKind = "TASK"
	WorkspaceConversation WorkspaceContextKind = "CONVERSATION"
)

type WorkspaceSourceContext struct {
	Kind WorkspaceContextKind
	ID   string
}

func (c WorkspaceSourceContext) Validate() error {
	if !semanticID(c.ID) || c.Kind != WorkspaceSource && c.Kind != WorkspaceTask && c.Kind != WorkspaceConversation {
		return invalid("workspace context: exactly one immutable context required")
	}
	return nil
}
func (c WorkspaceSourceContext) Matches(source, task, conversation string) bool {
	switch c.Kind {
	case WorkspaceSource:
		return c.ID == source && task == "" && conversation == ""
	case WorkspaceTask:
		return c.ID == task && source == "" && conversation == ""
	case WorkspaceConversation:
		return c.ID == conversation && source == "" && task == ""
	}
	return false
}
