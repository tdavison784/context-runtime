package domain

import "testing"

func TestAgentKeyAuthorityUsesNamespaceAndExactOwner(t *testing.T) {
	actor := agentActor()
	item := keyedAgentItem("status", taskBoundary)
	if err := AuthorizeAgentKeyWrite(actor, item); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*ContextItem){
		func(i *ContextItem) { i.Namespace = NamespaceDirective },
		func(i *ContextItem) { i.Namespace = "" },
		func(i *ContextItem) { i.AgentID = "other" },
		func(i *ContextItem) { i.Access.AgentID = "" },
		func(i *ContextItem) { i.WorkflowID = "other" },
		func(i *ContextItem) { i.Section = SectionWorking },
	} {
		bad := item
		mutate(&bad)
		if AuthorizeAgentKeyWrite(actor, bad) == nil || AuthorizeSupersession(actor, bad, item) == nil {
			t.Fatal("agent ownership or namespace bypass")
		}
	}
}
