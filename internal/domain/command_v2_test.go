package domain

import (
	"reflect"
	"testing"
)

func TestCommandV2WithholdsOutcomeAndAllDetails(t *testing.T) {
	viewer := Principal{SessionID: "s", TaskID: "other", Authority: AuthorityUser}
	base := LifecycleCommandRecord{SchemaVersion: LifecycleCommandSchemaV2, Access: AccessBoundary{Scope: ScopeSession, SessionID: "s"}, DetailAccess: AccessBoundary{Scope: ScopeTask, SessionID: "s", TaskID: "private"}}
	a := base
	a.Status = CommandExecuted
	a.Resolution = TargetResolved
	a.ResolvedItemID = "secret"
	a.ResolvedVersion = 4
	a.Execution = &CommandExecutionDetail{Outcome: CommandOutcomeExecuted, GrantID: "grant", Result: &ItemMutationResult{ItemID: "secret"}}
	b := base
	b.Status = CommandNotExecuted
	b.Resolution = TargetNotFound
	b.Execution = &CommandExecutionDetail{Outcome: CommandOutcomeNotFound}
	if !reflect.DeepEqual(a.Redacted(viewer), b.Redacted(viewer)) {
		t.Fatal("hidden successful outcome distinguishable from missing target")
	}
	clone := a.Clone()
	clone.Execution.Result.ItemID = "changed"
	if a.Execution.Result.ItemID != "secret" {
		t.Fatal("command detail aliases")
	}
}
