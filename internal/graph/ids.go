package graph

import "github.com/tdavison784/context-runtime/internal/domain"

// Relationship and lifecycle event IDs are derived deterministically from the
// data that identifies the edge, mirroring domain.DerivedItemID: retrying the
// same logical operation (same endpoints and event) reproduces the same
// record IDs rather than minting new ones, and different record kinds use
// distinct domain tags so their hashes never collide even over identical
// inputs.
const (
	relationshipIDTag = "context-runtime/graph/relationship-id/v1"
	// v2 adds the counterpart (for a retirement, the successor) to the
	// audit identity, so two audit records about one target in one event
	// can never alias (D11). v1 IDs already stored are left as they are.
	lifecycleEventIDTag = "context-runtime/graph/lifecycle-event-id/v2"
	idHashDisplayLength = 32
)

func deriveID(prefix, tag string, parts ...string) string {
	e := domain.NewCanonicalEncoder(tag)
	for _, p := range parts {
		e.String(p)
	}
	h := e.Hash() // "sha256:<hex>"
	if len(h) > len("sha256:")+idHashDisplayLength {
		h = h[len("sha256:") : len("sha256:")+idHashDisplayLength]
	}
	return prefix + "_" + h
}

// relationshipID derives the ID of the edge (sessionID, relType, fromID,
// toID) created by eventID.
func relationshipID(sessionID string, relType domain.RelationshipType, fromID, toID, eventID string) string {
	return deriveID("rel", relationshipIDTag, sessionID, string(relType), fromID, toID, eventID)
}

// lifecycleEventID derives the ID of the audit record for one action on
// targetID raised by eventID, whose other party is counterpartID (for a
// retirement, the successor).
func lifecycleEventID(sessionID, targetID, action, eventID, counterpartID string) string {
	return deriveID("evt", lifecycleEventIDTag, sessionID, targetID, action, eventID, counterpartID)
}
