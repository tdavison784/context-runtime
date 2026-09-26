package domain

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"
	"strings"
	"sync/atomic"
)

// Stable identifiers (ADR 4). IDs derived from caller-supplied stable inputs
// are deterministic so retries and replay reproduce them; other IDs come from
// an injectable generator so tests can be deterministic too.

// IDGenerator returns new unique IDs with the given prefix.
type IDGenerator interface {
	NewID(prefix string) string
}

// RandomIDs generates 128-bit random IDs.
type RandomIDs struct{}

// NewID returns prefix + "_" + 32 random hex characters.
func (RandomIDs) NewID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("context-runtime: crypto/rand failed: " + err.Error())
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

// SequentialIDs generates prefix_1, prefix_2, ... for tests.
type SequentialIDs struct{ n atomic.Uint64 }

// NewID returns the next sequential ID.
func (s *SequentialIDs) NewID(prefix string) string {
	return prefix + "_" + strconv.FormatUint(s.n.Add(1), 10)
}

// DerivedItemID is the deterministic ID of the index-th item created by a
// caller-identified event, so a retried event reproduces its item IDs.
func DerivedItemID(sessionID, eventID string, index int) string {
	return "itm_" + shortHash(NewCanonicalEncoder("context-runtime/item-id/v1").
		String(sessionID).String(eventID).Int(int64(index)))
}

// DerivedCallID is the deterministic ID of a prepared call: repeating an
// identical PrepareCall against the same conversation version returns the
// same logical call (FR-CALL-001).
func DerivedCallID(sessionID, conversationID string, baseVersion uint64, requestHash string) string {
	return "call_" + shortHash(NewCanonicalEncoder("context-runtime/call-id/v1").
		String(sessionID).String(conversationID).Uint(baseVersion).String(requestHash))
}

// DerivedDirectiveID is the ID of a directive item written without an
// explicit ID (FR-DIR-002): the lowercased keyword, a hyphen, and the full
// hex content hash.
func DerivedDirectiveID(keyword, contentHash string) string {
	return strings.ToLower(keyword) + "-" + strings.TrimPrefix(contentHash, hashPrefix)
}

// AgentKeyID is the directive ID of a keyed agent write (FR-TOOL-002).
func AgentKeyID(key string) string { return "agent." + key }

func shortHash(e *CanonicalEncoder) string {
	return strings.TrimPrefix(e.Hash(), hashPrefix)[:32]
}
