# 4. Stable IDs, event/call idempotency, and canonical hashes

Status: Proposed
Date: 2026-09-25

## Context

FR-ING-006 requires a caller-supplied EventID to be an idempotency key:
identical payload/principal/source returns the original result with no new
sequence number; a differing one fails `ErrEventIDConflict`. FR-ING-007
requires content-addressed immutable blobs. FR-CALL-001 requires
`DerivedCallID`-style idempotent PrepareCall. FR-DIR-002 requires a
derived ID for a directive written without an explicit ID: "the lowercased
keyword, a hyphen, and its full content hash." FR-DIR-006's ABNF caps
`id` at 64 characters. INV-09 requires stable input identities for
deterministic replay.

## Decision

- Hash format: `"sha256:<64 lowercase hex>"`, implemented by
  `domain.HashBytes`/`domain.ValidHash` (`internal/domain/canonical.go:64-79`).
- Canonical encodings are uvarint-length-prefixed fields preceded by a
  version domain tag (e.g. `"context-runtime/content/v1"`,
  `"context-runtime/item-id/v1"`), built by `domain.CanonicalEncoder`
  (`internal/domain/canonical.go:20-56`). Not JSON: JSON has ambiguous key
  order, float formatting, and escaping, none of which a content-addressed
  hash can tolerate. A new encoding requires a new tag so two different
  values never collide across encoding versions.
- `ContentHash` (`internal/domain/canonical.go:87-89`) covers parts' type,
  media type, text, blob hash, and blob size — blob bytes are covered via
  the blob's own hash, so `ContentHash` never has to load blob bytes.
- Item IDs: when a caller-supplied EventID is present, `DerivedItemID`
  (`internal/domain/ids.go:38-41`) computes a deterministic ID from
  `(session, eventID, index)`, so a retried event reproduces its item IDs.
  Otherwise an injected `IDGenerator` is used: `RandomIDs` (128-bit random
  hex) in production, `SequentialIDs` (`prefix_1`, `prefix_2`, ...) in tests
  (`internal/domain/ids.go:14-36`).
- Event idempotency: `(session, eventID)` is unique. `EventRecord.SameRequest`
  (`internal/domain/records.go:97-101`) compares session, event ID,
  principal (all fields), payload hash, and source hash. A matching request
  returns the stored record via `Tx.InsertEvent`'s `existed=true` path
  (`internal/store/store.go:132-135`) with no new sequence number; a
  differing request fails `ErrEventIDConflict`. Content deduplication
  (`DUPLICATE_OF`, FR-ING-005) is a separate operation from event identity.
- Call IDs: `DerivedCallID(session, conversation, baseConversationVersion,
  requestHash)` (`internal/domain/ids.go:44-48`) makes repeating an
  identical `PrepareCall` against the same conversation version return the
  same PREPARED record (FR-CALL-001). A different request while a call is
  reserved fails `ErrCallInFlight`; a stale base version fails
  `ErrVersionConflict`.
- Blobs are session-scoped, keyed by `(session, hash)`
  (`domain.Blob.SessionID`, `internal/domain/records.go:60-65`): no
  cross-session dedup, so blob existence cannot be probed across sessions
  (matches §9's "unauthorized lookups return ErrNotFound" policy applied to
  content existence, not just item existence).
- Directive derived IDs: `DerivedDirectiveID(keyword, contentHash)`
  (`internal/domain/ids.go:57-60`) returns the lowercased keyword, a hyphen,
  and the **full** 64-hex content hash (not truncated), per FR-DIR-002's
  "full content hash." This is deliberately different from `DerivedItemID`
  and `DerivedCallID`, which use `shortHash` — a 32-hex-char truncation
  (`internal/domain/ids.go:62-64`) — because those are opaque storage keys
  with no grammar constraint, while a directive ID is the identifier a user
  types into a Resolve/Unpin heading and must satisfy FR-DIR-006's `id`
  production.

## Required SDD amendment

**Conflict:** FR-DIR-006's ABNF caps `id` at 64 characters
(`id = 1*64(ALPHA / DIGIT / "-" / "_" / ".")`), but FR-DIR-002's derived ID
is `lowercased keyword + "-" + 64 hex chars`. For the `References` keyword
alone that is `references-` (11) + 64 = 75 characters, already over the cap;
every other keyword (`goal-`, `pinned-`, `working-`, `remember-`,
`ephemeral-`) is 5–10 chars plus hyphen plus 64, also over 64.

**Proposed replacement text**, FR-DIR-006's `id` production:

```
id = 1*80(ALPHA / DIGIT / "-" / "_" / ".")
```

Rationale for 80 rather than a larger round number: the longest keyword
(`ephemeral`, 9 chars) plus hyphen (1) plus hash (64) is 74; 80 leaves
headroom without inviting arbitrarily long identifiers into a field users
type by hand.

**Alternative rejected:** truncating the derived ID's hash to fit inside 64
characters. Rejected because FR-DIR-002 explicitly specifies "its full
content hash," and a truncated hash reintroduces a collision surface this
whole ADR's canonical-encoding discipline exists to avoid — two directives
with different content but colliding truncated hashes would silently
supersede one another under FR-DIR-002's ID-reuse rule.

## Alternatives considered

- **JSON as the canonical encoding.** Rejected: key ordering, float
  formatting, and Unicode escaping are all implementation-defined enough
  that two semantically-equal values could serialize differently across Go
  versions or library choices, breaking content addressing. The
  length-prefixed binary encoding has none of these ambiguities.
  `internal/domain/canonical.go`'s doc comment records this explicitly.
- **A single ID scheme for both derived items/calls and derived directive
  IDs.** Rejected: item/call IDs are internal storage keys never typed by a
  human and can be freely shortened; directive IDs are user-facing and
  constrained by the ABNF grammar, so they need the amendment above instead
  of truncation.
- **Cross-session blob dedup keyed only by hash.** Rejected: FR-ING-007
  plus §9's non-disclosure policy mean one session must not be able to
  learn that another session already has a given blob (a timing or
  existence side channel); scoping blobs to `(session, hash)` closes that
  channel at the cost of some storage duplication across sessions.

## Consequences / compatibility impact

- Any change to a canonical encoding's field set or order requires a new
  version tag (e.g. `.../v2`), which changes every hash computed from it —
  this is by design (a deliberate, versioned break) but must be tracked as a
  compatibility event whenever `contentEncodingV1` or the ID tags change.
- The FR-DIR-006 amendment, once accepted, changes the parser's grammar
  before Phase 2 (Directives and ingestion) exits; Phase 1 code is
  unaffected since `DerivedDirectiveID` already returns the full hash.
- `SequentialIDs` (`internal/domain/ids.go:32-36`) is production code reused
  by tests, not a test-only shim; its determinism is required for
  INV-09/FR-OBS-004 replay, and any test using it must not assume ID values
  survive a change in call order.

## Tests that lock the behavior

- Required: `internal/domain/canonical_test.go` — `HashBytes`/`ValidHash`
  round-trip and rejection of malformed hashes (wrong prefix, short hex,
  uppercase hex); `ContentHash` changes iff any part field changes, and is
  stable across process runs.
- Required: `internal/domain/ids_test.go` — `DerivedItemID` reproduces the
  same ID for the same `(session, eventID, index)` and differs for any
  changed input; `DerivedCallID` likewise for
  `(session, conversation, baseVersion, requestHash)`; `DerivedDirectiveID`
  returns the full 64-hex hash (length assertion) and is lowercase.
- Required: `internal/store/storetest` case for `InsertEvent` — repeating an
  EventID with an identical canonical request returns the stored record and
  `existed=true` with no new `Seq` allocated; a differing principal or
  payload hash fails `ErrEventIDConflict` (trace T10 step 1).
- Required: `internal/store/storetest` case for call preparation idempotency
  — repeating an identical derived `CallID` against the same conversation
  version returns the same PREPARED record; a differing request while
  reserved fails `ErrCallInFlight` (trace T10 step 2).
- Required: a blob store test asserting `(sessionA, hash)` and
  `(sessionB, hash)` are independent existence checks — inserting in one
  session must not make `Blob(hash)` succeed in another.

## Open questions

- Whether `shortHash`'s 32-hex-char truncation for item/call IDs has been
  checked against a birthday-bound collision probability at expected V1
  scale, or whether it should also move to the full hash now that storage
  cost is not a stated constraint.
- Whether the FR-DIR-006 amendment should also relax the grammar to allow a
  lowercase-only `id` production, since `DerivedDirectiveID` always
  lowercases the keyword but hex is already lowercase-only — i.e., whether
  user-supplied IDs should be case-normalized to avoid an ID that only a
  derived directive could produce (all-lowercase-plus-hyphen-plus-hex) being
  ambiguous with a user-chosen ID of the same shape.
