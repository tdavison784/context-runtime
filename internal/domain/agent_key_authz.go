package domain

// AuthorizeAgentKeyWrite checks the explicit namespace and exact immutable owner
// boundary. A display prefix or visibility in another agent's task is no grant.
func AuthorizeAgentKeyWrite(actor Principal, item ContextItem) error {
	if err := actor.Validate(); err != nil {
		return err
	}
	if !item.Access.Permits(actor) {
		return ErrNotFound
	}
	want := AccessBoundary{Scope: ScopeTask, SessionID: actor.SessionID, WorkflowID: actor.WorkflowID, TaskID: actor.TaskID, AgentID: actor.AgentID}
	if actor.Authority != AuthorityAgent || actor.TaskID == "" || actor.AgentID == "" ||
		item.Namespace != NamespaceAgentKey || item.Authority != AuthorityAgent ||
		item.SessionID != actor.SessionID || item.TaskID != actor.TaskID || item.AgentID != actor.AgentID || item.WorkflowID != actor.WorkflowID ||
		item.Section != SectionNone || item.Scope != ScopeTask || item.Access != want || !ValidDirectiveID(item.DirectiveID) {
		return ErrInvalidAuthorityPromotion
	}
	return nil
}
