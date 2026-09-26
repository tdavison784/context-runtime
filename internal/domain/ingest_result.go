package domain

import "fmt"

// IngestLink reports a newly created item's duplicate/replacement target.
type IngestLink struct{ ItemID, TargetID string }

func (l IngestLink) Validate() error {
	if l.ItemID == "" || l.TargetID == "" || l.ItemID == l.TargetID {
		return invalid("ingest link: distinct item and target IDs required")
	}
	return nil
}

// IngestResult is the original committed result, including on idempotent
// replay. Lifecycle commands are parsed/resolved only, never executed in Phase 2.
// EventID is the persisted ID (generated when the caller supplied none).
type IngestResult struct {
	EventID      string
	Seq          uint64
	Items        []ContextItem
	Lifecycle    []LifecycleCommand
	Diagnostics  []Diagnostic
	Duplicates   []IngestLink
	Replacements []IngestLink
}

func (r IngestResult) Validate() error {
	if r.EventID == "" || r.Seq == 0 {
		return invalid("ingest result: event ID and sequence required")
	}
	for _, it := range r.Items {
		if err := it.Validate(); err != nil {
			return err
		}
	}
	for _, c := range r.Lifecycle {
		if err := c.Validate(); err != nil {
			return err
		}
	}
	for _, d := range r.Diagnostics {
		if err := d.Validate(); err != nil {
			return err
		}
	}
	for _, links := range [][]IngestLink{r.Duplicates, r.Replacements} {
		for _, link := range links {
			if err := link.Validate(); err != nil {
				return fmt.Errorf("ingest result: %w", err)
			}
		}
	}
	return nil
}
