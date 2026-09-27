package domain

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
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

// DerivedCallID is the deterministic ID of a prepared call. It covers the
// conversation's revision when the reservation was taken and the complete
// frozen proposal (CallProposalHash), so a later operation at the same
// conversation version (after a cancellation or failure released the
// reservation) gets a new ID, while a retried PrepareCall of the identical
// proposal is recognized through the conversation's in-flight record
// (FR-CALL-001).
func DerivedCallID(sessionID, conversationID string, conversationRevision uint64, proposalHash string) string {
	return "call_" + shortHash(NewCanonicalEncoder("context-runtime/call-id/v2").
		String(sessionID).String(conversationID).Uint(conversationRevision).String(proposalHash))
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

// Event occurrences (M3). Every accepted ingestion has one internal
// occurrence ID that keys all of its artifacts. A caller-keyed occurrence
// derives from (session, EventID), so a retry reproduces it; an anonymous
// occurrence is generated once per Ingest attempt, outside any retried
// transaction callback. The two use different prefixes, so a caller cannot
// choose an EventID whose occurrence aliases an anonymous one.
const (
	callerOccurrencePrefix    = "evc_"
	anonymousOccurrencePrefix = "eva"
)

// CallerOccurrenceID is the occurrence ID of a caller-keyed event.
func CallerOccurrenceID(sessionID, eventID string) string {
	return callerOccurrencePrefix + shortHash(NewCanonicalEncoder("context-runtime/event-occurrence/v1").
		String(sessionID).String(eventID))
}

// NewAnonymousOccurrenceID returns a fresh occurrence ID for an event without
// a caller EventID. It carries no retry guarantee.
func NewAnonymousOccurrenceID(g IDGenerator) string { return g.NewID(anonymousOccurrencePrefix) }

// ValidOccurrenceID reports whether id has either occurrence ID form.
func ValidOccurrenceID(id string) bool {
	if hexPart, ok := strings.CutPrefix(id, callerOccurrencePrefix); ok {
		return len(hexPart) == 32 && isLowerHex(hexPart)
	}
	rest, ok := strings.CutPrefix(id, anonymousOccurrencePrefix+"_")
	return ok && rest != ""
}

// OccurrenceMatchesEvent reports whether occurrenceID is the occurrence of
// eventID in the session: the derived caller occurrence when eventID is set,
// and an anonymous occurrence when it is empty.
func OccurrenceMatchesEvent(sessionID, occurrenceID, eventID string) bool {
	if eventID != "" {
		return occurrenceID == CallerOccurrenceID(sessionID, eventID)
	}
	return ValidOccurrenceID(occurrenceID) && !strings.HasPrefix(occurrenceID, callerOccurrencePrefix)
}

// IDDomain separates the deterministic ID spaces of one occurrence's
// artifacts (M3). Each domain has its own prefix and versioned hash tag, so
// ordinals in different domains never collide. Item IDs keep DerivedItemID;
// relationship and lifecycle-audit IDs belong to internal/graph.
type IDDomain string

const (
	IDDomainDiagnostic IDDomain = "dgn"
	IDDomainCommand    IDDomain = "cmd"
	IDDomainSection    IDDomain = "sec"
	IDDomainReference  IDDomain = "ref"
)

var idDomains = []IDDomain{IDDomainDiagnostic, IDDomainCommand, IDDomainSection, IDDomainReference}

// Valid reports whether d is a known ID domain.
func (d IDDomain) Valid() bool { return slices.Contains(idDomains, d) }

// reservedIDPrefixes are the prefixes of every internally generated ID: the
// occurrence forms, each artifact ID domain, and the item, call, turn, and
// obligation IDs derived here. internal/graph derives relationship ("rel")
// and lifecycle-audit ("evt") IDs, and internal/invocation derives call
// lifecycle-event ("lce") IDs; all three are reserved here too because the
// domain cannot import either package (SPEC-1.13: "lce" was missing).
// "req_" is the runtime-derived operation request namespace (G3, SEC-1.2);
// "gc_" and "gcq_" are the runtime GC collection-request and GC-request
// namespaces (H5, SEC-2.6).
var reservedIDPrefixes = func() []string {
	out := []string{callerOccurrencePrefix, anonymousOccurrencePrefix + "_", "itm_", "call_", "turn_", "obl_", "rel_", "evt_", "lce_", operationRequestPrefix, gcTriggerRequestPrefix, gcRequestRecordPrefix}
	for _, d := range idDomains {
		out = append(out, string(d)+"_")
	}
	return out
}()

// ReservedIDPrefix reports whether id begins with the prefix of an internally
// generated ID. Caller-chosen identifiers (EventIDs) must not (R20.1), so no
// caller value can pose as, or alias, a runtime-generated ID.
func ReservedIDPrefix(id string) bool {
	for _, p := range reservedIDPrefixes {
		if strings.HasPrefix(id, p) {
			return true
		}
	}
	return false
}

// ValidateCallerRequestID checks a request ID supplied by a caller of a
// standalone intent: a bounded printable ID outside every reserved runtime
// namespace, so no caller can name or squat a runtime-derived request.
func ValidateCallerRequestID(id string) error {
	if !semanticID(id) || ReservedIDPrefix(id) {
		return invalid("request ID: caller IDs must be printable and outside reserved runtime namespaces")
	}
	return nil
}

// DerivedArtifactID is the deterministic ID of an occurrence artifact at the
// given ordinals (for example span index and diagnostic index). It panics on
// an unknown domain: domains are compile-time constants, never input.
func DerivedArtifactID(d IDDomain, sessionID, occurrenceID string, ordinals ...uint64) string {
	if !d.Valid() {
		panic("context-runtime: unknown ID domain " + string(d))
	}
	e := NewCanonicalEncoder("context-runtime/" + string(d) + "-id/v1").String(sessionID).String(occurrenceID)
	e.Uint(uint64(len(ordinals)))
	for _, o := range ordinals {
		e.Uint(o)
	}
	return string(d) + "_" + shortHash(e)
}

// DerivedTurnID is the stable ID of a task's n-th turn (D18). A task never
// reuses a turn number, so the ID is unique within the session.
func DerivedTurnID(sessionID, taskID string, turn uint64) string {
	return "turn_" + shortHash(NewCanonicalEncoder("context-runtime/turn-id/v1").
		String(sessionID).String(taskID).Uint(turn))
}

func isLowerHex(s string) bool {
	for i := range len(s) {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
