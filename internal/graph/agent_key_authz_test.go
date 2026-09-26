package graph

import (
	"github.com/tdavison784/context-runtime/internal/domain"
	"testing"
)

func TestAgentFirstFilingRequiresExplicitExactOwner(t *testing.T) {
	actor := principal("s", domain.AuthorityAgent)
	item := agentDirective("s", "i", domain.AgentKeyID("status"), 1)
	if err := authorizeFirstVersionDirective(actor, item); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*domain.ContextItem){
		func(i *domain.ContextItem) { i.Namespace = domain.NamespaceDirective },
		func(i *domain.ContextItem) { i.Namespace = "" },
		func(i *domain.ContextItem) { i.Access.AgentID = "" },
		func(i *domain.ContextItem) { i.AgentID = "other" },
	} {
		bad := item.Clone()
		change(&bad)
		if authorizeFirstVersionDirective(actor, bad) == nil {
			t.Fatal("first filing bypassed exact ownership")
		}
	}
}
