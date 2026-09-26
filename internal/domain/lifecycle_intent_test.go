package domain

import "testing"

func TestIntentCannotGrantCompletionShortcut(t *testing.T) {
	p := Principal{SessionID: "s", Authority: AuthorityUser}
	i := GrantIntent{RequestID: "req", GrantID: "g", Action: ActionCompleteTask, Targets: []GrantTarget{ItemGrantTarget("s", "i")}, Grantee: &p}
	if i.Validate() == nil {
		t.Fatal("completion shortcut grant accepted")
	}
	i.Action = ActionResolve
	if err := i.Validate(); err != nil {
		t.Fatal(err)
	}
	copy := i.Clone()
	copy.Targets[0].ItemID = "other"
	if i.Targets[0].ItemID != "i" {
		t.Fatal("clone aliases")
	}
}
