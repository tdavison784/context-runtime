package tools

import (
	"strings"
	"testing"

	"github.com/tdavison784/context-runtime/internal/domain"
)

func TestResultTextReportsActualClaimStatusWithoutCopiedContent(t *testing.T) {
	claim := func(status domain.GoalStatus, c domain.ItemCurrentness) string {
		return ResultText(domain.ToolResult{Claim: &domain.CompletionClaimResult{ClaimItemID: "claim", TargetItemID: "goal", ObservedVersion: 3, GoalStatus: status, Currentness: c}})
	}
	open := claim(domain.GoalOpen, domain.ItemCurrent)
	if !strings.Contains(open, "did not change the goal") || !strings.Contains(open, "Authorized Resolve or CompleteTask is still required.") || !strings.Contains(open, "is OPEN (CURRENT)") {
		t.Fatal(open)
	}
	for _, text := range []string{claim(domain.GoalResolved, domain.ItemCurrent), claim(domain.GoalOpen, domain.ItemHistorical)} {
		if strings.Contains(text, "still required") || strings.Contains(text, "OPEN (CURRENT)") {
			t.Fatalf("mislabeled status: %s", text)
		}
	}
	if got := ResultText(domain.ToolResult{Keyed: &domain.KeyedWriteResult{ItemID: "dup", CanonicalItemID: "cur", Duplicate: true}}); got != "Recorded dup as a duplicate of current cur; nothing changed." {
		t.Fatal(got)
	}
	if got := ResultText(domain.ToolResult{}); got != domain.ToolErrorUnavailable.Message() {
		t.Fatal(got)
	}
}
