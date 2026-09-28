package tools

import (
	"strconv"

	"github.com/tdavison784/context-runtime/internal/domain"
)

// ResultText is the closed model-facing template for a committed result. It
// names only authorized IDs and observed status, never copied source content,
// so a replay renders byte-identical text (P3-24/26).
func ResultText(r domain.ToolResult) string {
	if r.Validate() != nil {
		return domain.ToolErrorUnavailable.Message()
	}
	switch {
	case r.Keyed != nil:
		k := r.Keyed
		switch {
		case k.Duplicate:
			return "Recorded " + k.ItemID + " as a duplicate of current " + k.CanonicalItemID + "; nothing changed."
		case k.SupersededItemID != "":
			return "Stored " + k.ItemID + "; it replaces " + k.SupersededItemID + "."
		}
		return "Stored " + k.ItemID + "."
	case r.Claim != nil:
		c := r.Claim
		goal := " Goal " + c.TargetItemID + " version " + strconv.FormatUint(c.ObservedVersion, 10)
		if c.Currentness != domain.ItemCurrent {
			// P3-26: a superseded or duplicate goal is never labelled OPEN or
			// given the replacement's status; the receipt keeps what was observed.
			return c.Message() + goal + " is not the current goal (" + string(c.Currentness) + "); claim " + c.ClaimItemID + "."
		}
		return c.Message() + goal + " is " + string(c.GoalStatus) + " (CURRENT); claim " + c.ClaimItemID + "."
	case r.CheckpointID != "":
		return "Checkpoint " + r.CheckpointID + " recorded."
	}
	return "Retrieval result " + r.RetrievalResultID + " recorded."
}
