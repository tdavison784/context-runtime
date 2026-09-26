# 4. Stable IDs, event/call idempotency, and canonical hashes

Status: Proposed
Date: 2026-09-25

## Context

FR-ING-006 requires a caller-supplied EventID to be an idempotency key:
identical payload/principal/source returns the original result with no new
sequence number; a differing one fails `ErrEventIDConflict`. FR-ING-007
requires content-addressed immutable blobs. FR-CALL-001 requires an
idempotent PrepareCall that revalidates the whole frozen proposal, not just
an ID match. FR-CALL-002/003 require an outcome's identity to be bound to
the response bytes it asserts, and require identity per transport attempt so
a retried attempt cannot be confused with the one it replaced. FR-DIR-002
(as amended, SDD v0.6) derives a directive ID from the lowercased keyword, a
hyphen, and the 64 lowercase hex digits of the content hash; FR-DIR-006 (as
amended) raises the `id` grammar cap to 80 characters to fit it. INV-09
requires stable input identities for deterministic replay.

## Decision

- Hash format: `"sha256:<64 lowercase hex>"`, implemented by
  `domain.HashBytes`/`domain.ValidHash` (`internal/domain/canonical.go`).
- Canonical encodings are uvarint-length-prefixed fields preceded by a
  version domain tag (e.g. `"context-runtime/content/v1"`,
  `"context-runtime/call-proposal/v1"`), built by `domain.CanonicalEncoder`.
  Not JSON: JSON has ambiguous key order, float formatting, and escaping.
  Fields carry no type tag, so injectivity holds **per schema** (a fixed tag
  fixes that tag's field types, order, and count) rather than globally —
  `String("")`, `Bytes(nil)`, and `Uint(0)` all encode as the same byte, so
  two schemas that differ only in which of these they use at a given
  position could collide; the discipline is that any change to a schema's
  field types, order, or count requires a new tag, and no two schemas
  reachable from different tags are compared against each other.
- `ContentHash` covers parts' type, media type, text, blob hash, and blob
  size — blob bytes are covered via the blob's own hash.
- Item IDs: when a caller-supplied EventID is present, `DerivedItemID`
  computes a deterministic ID from `(session, eventID, index)`. Otherwise an
  injected `IDGenerator` is used: `RandomIDs` (128-bit random hex) in
  production, `SequentialIDs` (`prefix_1`, `prefix_2`, ...) in tests.
- Event idempotency: `(session, eventID)` is unique. `EventRecord
  .SameRequest` compares session, event ID, principal (all fields), payload
  hash, and source hash. A matching request returns the stored record via
  `Tx.InsertEvent`'s `existed=true` path with no new sequence number; a
  differing request fails `ErrEventIDConflict`.
- Call IDs (v2): `DerivedCallID(session, conversation, conversationRevision,
  proposalHash)` (`internal/domain/ids.go`) hashes the conversation's
  `Revision` **as read immediately before the reservation is taken** (not
  after — the whole point is to distinguish a later operation from an
  earlier one whose reservation already released) and `CallProposalHash(c)`
  — every field PrepareCall froze: session, conversation, operation, both
  principals, base version, semantic seq, epoch, policy/descriptor
  versions, request hash, manifest hash. `CallRecord.ProposalHash` **must be
  persisted on the record itself** and equal `CallProposalHash(c)`
  (`internal/domain/call.go`, checked by `CallRecord.Validate`) — the hash
  is not just an ID input, it is stored data a later read can re-verify
  without recomputing PrepareCall's original inputs. Repeating `PrepareCall`
  is recognized by comparing the new proposal against the conversation's
  currently held in-flight record's `ProposalHash`, not by the derived ID
  alone — the ID is a storage key; the held record's full proposal is the
  idempotency check. ADR 17 fixes the required order of checks in `Prepare`
  (base version, then semantic staleness, then this comparison) and records
  that `internal/invocation/prepare.go` does not yet implement either the ID
  scheme or that order.
- Outcome identity is per attempt. `CallOutcome.Attempt` (≥1) names the
  attempt it closes; `OutcomeHash()` covers `Attempt` plus state, response
  hash, failure reason, retryable flag, and usage (nil-vs-present
  distinguished, so "unknown" never collides with "zero," FR-COST-001).
  `CallOutcome.Validate` requires `HashBytes(Response) == ResponseHash` for
  any outcome carrying response bytes, and forbids a FAILED/COMPLETED
  mismatch (e.g. a completed outcome carrying a failure reason) — reporting
  a response by bytes or by a bare hash is provably the same outcome, and
  two different response bodies can never share an identity.
  `CallAttempt.OutcomeHash` is immutable once set (`store.Tx.PutCallAttempt`),
  so a closed attempt's identity cannot drift under a later write.
- Blobs are session-scoped, keyed by `(session, hash)`: no cross-session
  dedup, so existence cannot be probed across sessions.
- Directive derived IDs: `DerivedDirectiveID(keyword, contentHash)` returns
  the lowercased keyword, a hyphen, and the full 64-hex content hash. This
  differs from `DerivedItemID`/`DerivedCallID`, which use a 32-hex-char
  `shortHash` truncation — those are opaque storage keys with no grammar
  constraint, while a directive ID is user-facing and must satisfy
  FR-DIR-006's `id` production.

## SDD amendment (applied in v0.6)

The 64-vs-75-character conflict between FR-DIR-002's full-hash derived ID
and FR-DIR-006's 64-character `id` grammar is resolved in SDD v0.6
(commit `78d988f`): FR-DIR-002 now reads "...a hyphen, and the 64 lowercase
hex digits of its content hash," and FR-DIR-006's grammar is
`id = 1*80(ALPHA / DIGIT / "-" / "_" / ".")`. 80 was chosen over exactly 75
(the longest case, `ephemeral-` + 64 hex) to leave small headroom without
inviting arbitrarily long hand-typed identifiers.

**Alternative rejected:** truncating the derived ID's hash to fit inside 64
characters. Rejected because it reintroduces a collision surface — two
directives with different content but colliding truncated hashes would
silently supersede one another under FR-DIR-002's ID-reuse rule.

## Alternatives considered

- **JSON as the canonical encoding.** Rejected: key ordering, float
  formatting, and Unicode escaping are implementation-defined enough that
  two semantically-equal values could serialize differently, breaking
  content addressing.
- **A single ID scheme for both derived items/calls and derived directive
  IDs.** Rejected: item/call IDs are internal, never typed by a human, and
  can be freely shortened; directive IDs are user-facing and grammar
  constrained.
- **Deriving the call ID from the base conversation version and request
  hash alone (v1).** Rejected per Codex finding 4: that scheme lets an
  unrelated later operation at the same conversation version alias an
  earlier PREPARED or terminal call's ID once its reservation is released.
  Binding to the conversation's pre-reservation revision, plus persisting
  and checking the full frozen `ProposalHash` against the currently held
  record, closes the aliasing gap — but not the separate ordering gap ADR
  17 records (Codex finding N1): even with the v2 ID scheme, checking the
  held record before revalidating base version/semantic sequence would
  still let a same-ID match skip revalidation. Both fixes are required.
- **A single `OutcomeHash` per call instead of per attempt.** Rejected per
  Codex finding 3: a retryable failure on attempt 1 followed by a different
  outcome on attempt 2 would either look like a hash conflict against
  attempt 1 (if the hash isn't cleared) or lose duplicate-detection after a
  crash (if it is). Per-attempt, immutable-once-set hashes avoid both.
- **Cross-session blob dedup keyed only by hash.** Rejected: would let one
  session learn whether another session already has a given blob.

## Consequences / compatibility impact

- The v1→v2 `DerivedCallID` and `CallOutcome`/`OutcomeHash` changes are
  breaking changes to any already-persisted v1 call records; Phase 1 has no
  production data to migrate, so this is a clean cutover, not a migration.
- Any change to a canonical schema's field set, order, or type requires a
  new version tag; this is now the documented per-schema injectivity
  boundary, not a claim of global collision-freedom.
- The FR-DIR-002/006 amendment is applied; no further SDD change is pending
  for this ADR's scope.
- `SequentialIDs` remains production code reused by tests for INV-09/
  FR-OBS-004 replay determinism.

## Tests that lock the behavior

- `internal/domain/canonical_test.go`: `HashBytes`/`ValidHash` round-trip
  and malformed-hash rejection; `ContentHash` changes iff any part field
  changes.
- `internal/domain/ids_test.go`: `DerivedItemID` determinism;
  `DerivedCallID` v2 determinism over `(session, conversation, revision,
  proposalHash)`, and a differing revision or proposal hash produces a
  different ID; `DerivedDirectiveID` returns the full 64-hex hash.
- `internal/domain/call_test.go`: `CallOutcome.Validate` rejects a
  `Response` whose bytes don't hash to `ResponseHash`, and rejects a
  COMPLETED outcome carrying `Retryable`/`FailureReason`; `OutcomeHash`
  differs when only `Attempt` differs.
- `internal/invocation` (`t10_test.go`, `property_test.go`, already
  committed): re-`Prepare`ing an identical proposal against an unreleased
  reservation returns the same record; a retryable failure on one attempt
  followed by a distinct outcome on the next attempt is not a conflict. The
  case that a changed semantic seq or base version is never treated as
  identical even when the `ProposalHash` matches — i.e. the check-order
  regression Codex's second pass found (ADR 17, finding N1) — is not yet
  covered; see ADR 17's Tests section for the required addition.
- `internal/store/storetest`: `InsertEvent` idempotency (trace T10 step 1);
  a blob store test asserting `(sessionA, hash)` and `(sessionB, hash)` are
  independent existence checks.

## Open questions

- Whether `shortHash`'s 32-hex-char truncation for item/call IDs should also
  move to the full hash now that storage cost is not a stated constraint.
- Whether FR-DIR-006's grammar should also case-normalize user-supplied IDs,
  so a hand-typed ID can never collide in shape with a derived one.

## Review

First pass (Codex gpt-6-sol xhigh, `codex-decision-review-out.md`, findings
3, 4, 5, 12, 14): `DerivedCallID` bound to the conversation's revision and
the full `CallProposalHash` instead of base version + request hash;
`CallOutcome`/`OutcomeHash` moved to per-attempt identity with
response-bytes-to-hash validation; the injectivity claim scoped per schema;
the amendment section updated to report FR-DIR-002/006 as applied.

Second pass (Codex gpt-6-sol xhigh, `codex-contract-v2-review.md`, finding
N1, verifying PARTIAL on finding 4): the v1→v2 ID redesign was correct as a
domain-layer decision, but two things were still missing — `CallRecord
.ProposalHash` wasn't specified as a field the record itself persists (not
just an ID input), and the committed `internal/invocation` package hadn't
adopted either the new ID signature or a safe check order. Changed: the
Decision now states `ProposalHash` must be persisted and re-verifiable on
the record; the required `Prepare` check order is now ADR 17's decision,
cross-referenced here; the Tests section no longer overclaims coverage
`internal/invocation`'s committed tests don't yet have.
