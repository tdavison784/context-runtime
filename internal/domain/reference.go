package domain

// MaxLocatorKeyBytes bounds a lexical locator key.
const MaxLocatorKeyBytes = 4096

// UnresolvedReference is a persisted References entry that named a path or
// URL not yet matched by an ingested item (M5, R2, R18). A later ingestion
// whose source matches LocatorKey under the same RuleVersion may link it with
// a REFERENCES edge, but only under the reference's own ownership context
// (Access and Authority) as well as the later event's authorization, so an
// old broad reference can never disclose newly ingested private evidence.
// LocatorKey is lexical: the versioned identity rule derives it from the
// locator plus its repository/resource namespace and base directory without
// touching the filesystem or network. ItemID is the reference item that
// declared it; SpanIndex is its span in the declaring event. ID derives from
// (session, occurrence, ordinal), so retries reproduce it and anonymous
// events never alias.
type UnresolvedReference struct {
	ID           string
	SessionID    string
	OccurrenceID string
	Ordinal      int
	SpanIndex    int
	ItemID       string
	LocatorKey   string
	RuleVersion  string
	Access       AccessBoundary
	Authority    Authority
	Seq          uint64
}

// UnresolvedReferenceID derives a reference ID from its key.
func UnresolvedReferenceID(sessionID, occurrenceID string, ordinal int) string {
	return DerivedArtifactID(IDDomainReference, sessionID, occurrenceID, uint64(ordinal))
}

// Clone returns a copy; the record holds no shared state.
func (r UnresolvedReference) Clone() UnresolvedReference { return r }

// Validate checks the record's key, locator key, and ownership context.
func (r UnresolvedReference) Validate() error {
	if r.SessionID == "" || !ValidOccurrenceID(r.OccurrenceID) || r.Ordinal < 0 || r.SpanIndex < 0 || r.ItemID == "" || r.Seq == 0 {
		return invalid("unresolved reference: session, occurrence, ordinal, span, item, and sequence are required")
	}
	if r.ID != UnresolvedReferenceID(r.SessionID, r.OccurrenceID, r.Ordinal) {
		return invalid("unresolved reference: ID does not match its key")
	}
	if r.LocatorKey == "" || len(r.LocatorKey) > MaxLocatorKeyBytes || r.RuleVersion == "" {
		return invalid("unresolved reference: locator key and rule version are required")
	}
	// References sections are parsed only from SYSTEM, HARNESS, and marked
	// USER spans (FR-ING-004).
	if !r.Authority.CanHoldLifecycleAuthority() {
		return invalid("unresolved reference: authority %q cannot declare references", r.Authority)
	}
	if err := r.Access.Validate(); err != nil {
		return err
	}
	if r.Access.SessionID != r.SessionID {
		return invalid("unresolved reference: access boundary belongs to another session")
	}
	return nil
}
