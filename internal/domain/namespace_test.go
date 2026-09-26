package domain

import "testing"

func TestCurrentKeyValidate(t *testing.T) {
	access := AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "t1"}
	good := CurrentKey{SessionID: "s1", TaskID: "t1", Access: access, Namespace: NamespaceDirective, ID: "agent.status"}
	if err := good.Validate(); err != nil {
		t.Fatalf("agent.status is a legal directive ID: %v", err)
	}
	for name, mut := range map[string]func(*CurrentKey){
		"namespace":      func(k *CurrentKey) { k.Namespace = "directive" },
		"empty ns":       func(k *CurrentKey) { k.Namespace = "" },
		"id":             func(k *CurrentKey) { k.ID = "bad id" },
		"session":        func(k *CurrentKey) { k.SessionID = "" },
		"access session": func(k *CurrentKey) { k.Access.SessionID = "s2" },
		"access task":    func(k *CurrentKey) { k.TaskID = "t2" },
		"access":         func(k *CurrentKey) { k.Access.TaskID = "" },
	} {
		k := good
		mut(&k)
		if k.Validate() == nil {
			t.Errorf("%s: invalid key accepted", name)
		}
	}
}

func TestItemNamespaceSeparatesDirectivesFromAgentKeys(t *testing.T) {
	access := AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "t1"}
	directive := ContextItem{SessionID: "s1", TaskID: "t1", Access: access, DirectiveID: "agent.status", Section: SectionWorking}
	agent := directive
	agent.Section = SectionNone
	dk, ok1 := directive.CurrentKey()
	ak, ok2 := agent.CurrentKey()
	if !ok1 || !ok2 || dk.Namespace != NamespaceDirective || ak.Namespace != NamespaceAgentKey || dk == ak {
		t.Fatalf("namespaces not separated: %+v %+v", dk, ak)
	}
	if _, ok := (ContextItem{}).DirectiveNamespace(); ok {
		t.Fatal("item without an ID has a namespace")
	}
}

func TestExplicitNamespaceOverridesLegacyShape(t *testing.T) {
	it := ContextItem{SessionID: "s1", TaskID: "t1", DirectiveID: "same", Namespace: NamespaceObservation,
		Access: AccessBoundary{Scope: ScopeTask, SessionID: "s1", TaskID: "t1"}}
	k, ok := it.CurrentKey()
	if !ok || k.Namespace != NamespaceObservation {
		t.Fatal("observation inferred as agent key")
	}
	seen := map[string]bool{}
	for _, ns := range []DirectiveNamespace{NamespaceDirective, NamespaceAgentKey, NamespaceObservation} {
		k.Namespace = ns
		k.ID = "sub_" + HashBytes(nil)[7:]
		h, err := k.CanonicalHash()
		if err != nil || seen[h] {
			t.Fatal("namespace key collision", err)
		}
		seen[h] = true
	}
	it.Namespace = "UNKNOWN"
	if _, ok := it.CurrentKey(); ok {
		t.Fatal("unknown namespace downgraded to legacy")
	}
}
