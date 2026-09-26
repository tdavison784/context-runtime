package sqlite

import "github.com/tdavison784/context-runtime/internal/domain"

// pathStateRow is a path's current content, keyed by its canonical
// resource locator key (P3-19).
type pathStateRow struct {
	SessionID  string
	LocatorKey string
	State      domain.ResourcePathState
}

// subjectStateRow is a subject's current state in one (task, boundary)
// partition, keyed by that partition; Resource and FirstSeq place it in
// the by-resource index in first-filing order (P3-22).
type subjectStateRow struct {
	SessionID string
	Key       string
	Resource  string
	FirstSeq  uint64
	State     domain.SubjectState
}

