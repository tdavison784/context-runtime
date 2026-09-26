# 16. Common mutation authorization, version replacement, residency/goal status, immutable snapshots

Status: Proposed
Date: 2026-09-25

## Context

FR-AUTH-001 requires every mutation to check both access to its targets and
authority for the action; unauthorized operations fail atomically with
`ErrInvalidAuthorityPromotion`. FR-AUTH-002 lets a source delegate a named
action to a SYSTEM/HARNESS/USER principal on named targets within a scope
and expiry, but AGENT/TOOL/RETRIEVED_CONTENT can never issue or receive
lifecycle grants. FR-REL-006 requires a SUPERSEDES edge to require access to
both endpoints, superseding authority at least superseded authority, and the
**same** access boundary (not merely `Within`) so a replacement can never
hide an item from a principal who could see the original. FR-DOM-005
requires residency and goal status to be independent axes. FR-DOM-008 makes
content/kind/authority/IDs/scope/access/source/sequence immutable, with only
generation/residency/goal-status/retention/usage/version changing through
audited lifecycle events. FR-TOOL-003 and FR-AUTH-001 jointly raise a
conflict recorded below.

## Decision

- `AuthorizeMutation` (`internal/domain/authz.go:172-196`) is the single
  entry point every lifecycle mutation (Resolve, Unpin, directive
  replacement, scope changes, obligation transitions, CompleteTask) runs
  through. It is all-or-nothing across the request's targets: **every**
  target must be accessible (`MutationTarget.Access.Permits(actor)`) before
  **any** authority check runs, and access failure returns `ErrNotFound`
  uniformly so a caller cannot distinguish "not found" from "found but
  denied" (§9, ADR 6). Each target is then authorized either directly — the
  actor holds `CanHoldLifecycleAuthority()` (SYSTEM/HARNESS/USER,
  `internal/domain/enums.go:71-74`) and `actor.Authority.AtLeast(target
  .Authority)` — or by an in-force `MutationGrant` matching the action and
  target, issued by a principal whose authority is at least the target's
  (`findGrant`, `internal/domain/authz.go:198-228`).
- `Authority.AtLeast` (`internal/domain/enums.go:32-38`) is: equal, or
  strictly higher rank. `AuthorityTool` and `AuthorityRetrievedContent`
  share rank 1 (`internal/domain/enums.go:20-27`) and explicitly do not
  dominate each other — `a == b || rank[a] > rank[b]` means two distinct
  equal-rank authorities never satisfy `AtLeast` against one another.
- `MutationGrant` validity is computed purely from session sequence numbers
  — `IssuedSeq`, `ExpiresAtSeq`, `RevokedSeq`
  (`MutationGrant.activeAt`, `internal/domain/authz.go:117-127`) — never
  wall-clock time, per §10's determinism requirement. `MutationGrant
  .Validate` (`internal/domain/authz.go:73-105`) rejects a grant issued by or
  to AGENT/TOOL/RETRIEVED_CONTENT via `CanHoldLifecycleAuthority()`, encoding
  FR-AUTH-002's restriction structurally rather than as a runtime check
  callers could skip.
- Supersession (FR-REL-006) is checked separately by
  `AuthorizeSupersession` (`internal/domain/authz.go:237-252`): the actor
  must access both endpoints; superseding authority must be at least
  superseded authority (rejects an authority downgrade masquerading as
  supersession); the actor's authority must be at least the superseding
  item's own authority (rejects authority laundering through an
  intermediate low-authority actor); and — critically — the two items'
  access boundaries must be **equal**, not merely `Within` one another. This
  is the correct check because `Within` alone would permit a *narrower*
  replacement to supersede a *broader* original, which would hide the
  original from principals who could see it before but not after — exactly
  what FR-REL-006 forbids ("a replacement must never hide an item from a
  principal who cannot see the replacement"). Scope/boundary widening or
  narrowing must go through an explicit authorized replacement policy
  instead of ID reuse (FR-DIR-002).
- `GoalStatus` transitions only OPEN→RESOLVED; `ItemChange.Apply`
  (`internal/domain/records.go:224-256`) enforces this directly — any other
  `GoalStatus` transition (including RESOLVED→OPEN) fails
  `ErrInvalidTransition`. Reopening a resolved goal requires an authorized
  replacement that creates a new item with a new OPEN `GoalStatus`
  (FR-DIR-005), not a mutation of the existing item. Residency changes are a
  separate field on the same `ItemChange` and never touch `GoalStatus`,
  matching FR-DOM-005's independence requirement.
- `ItemChange.Apply` bundles generation/residency/goal/retention/usage
  changes into one CAS-guarded update: `store.Tx.UpdateItem` requires the
  stored `Version` to equal `expectedVersion` or fails
  `ErrVersionConflict` (`internal/store/store.go:141-144`), and
  `ItemChange.Apply` itself increments `Version` (`internal/domain/records
  .go:255`). Immutable fields (content, kind, authority, IDs, scope, access,
  source, sequence — FR-DOM-008) have no corresponding `ItemChange` field at
  all, so the type system rather than a runtime check prevents mutating them.
- Obligation transitions follow the table in
  `domain.ValidObligationTransition` (`internal/domain/obligation.go:24-36`):
  UNRESOLVED↔BLOCKED, UNRESOLVED↔SATISFIED, and any-status→WAIVED (terminal
  — no transition leaves WAIVED, checked by `from == ObligationWaived`
  returning false unconditionally at line 26). This is the FR-OBL-002 table;
  `AuthorizeMutation`/`AuthorizeSupersession` govern *who* may drive a
  transition, `ValidObligationTransition` governs *which* transitions exist
  at all, and `ObligationTransition.Validate`
  (`internal/domain/obligation.go:141-156`) combines both by rejecting any
  transition whose `(From, To)` fails the table before it becomes a stored
  record.

## Required SDD amendment

**Conflict:** FR-TOOL-003 says `context_resolve` "resolves only items the
agent has authority over (FR-AUTH-001)." But FR-AUTH-001 requires a
SYSTEM, HARNESS, or USER principal for Resolve (`AuthorizeMutation`'s
`CanHoldLifecycleAuthority()` gate, `internal/domain/enums.go:71-74`, which
AGENT never satisfies), and FR-TOOL-002/§9 state the semantic-state tools
write only at AGENT authority and "cannot create goals, pins, or
obligations." Combined, an AGENT-authority tool call can never hold
authority over anything Resolve is meaningful for — there is no item an
AGENT principal has FR-AUTH-001 authority over, since AGENT can never
outrank the SYSTEM/HARNESS/USER-authority items that carry goals/pins. So
FR-TOOL-003's stated behavior ("resolves only items the agent has authority
over") describes an empty set: `context_resolve` as written can never
resolve anything.

**Proposed replacement text**, FR-TOOL-003, first sentence:

> context_resolve always records a completion claim: an evidence item
> DERIVED_FROM the cited evidence, linked to the target goal with
> REFERENCES, with the tool result stating that the goal stays OPEN until an
> authorized Resolve or CompleteTask. It never performs the Resolve mutation
> itself, regardless of the target's authority.

This removes the "resolves only items the agent has authority over" clause
entirely and makes the completion-claim behavior (already described in
FR-TOOL-003's second sentence for the higher-authority case) the *only*
behavior, since the "agent has authority" branch was unreachable.

**Alternative rejected:** leaving FR-TOOL-003 as written and treating the
unreachable branch as harmless dead specification. Rejected because a
future implementer could read "resolves only items the agent has authority
over" as license to weaken FR-AUTH-001's gate for some as-yet-undefined case
where AGENT does hold authority over a goal — no such case exists today, but
an ambiguous requirement inside the same document as the invariant it
appears to soften is a hazard. Making the text match the only reachable
behavior removes that hazard.

## Alternatives considered

- **A separate `AuthorizeSupersession` check reusing `AuthorizeMutation`'s
  target loop instead of its own function.** Rejected: supersession's
  same-boundary requirement is stricter than (and structurally different
  from) `AuthorizeMutation`'s per-target access+authority check — reusing
  the general path would require bolting an exception onto it rather than
  expressing the FR-REL-006-specific rule directly. A dedicated function
  with its own doc comment referencing FR-REL-006 keeps the "why" attached
  to the code that enforces it.
- **`Within` instead of boundary equality for supersession.** Rejected per
  the decision above: `Within` permits narrowing, which FR-REL-006
  forbids for supersession specifically ("cannot suppress an item visible
  to another principal").
- **Wall-clock-based grant expiry.** Rejected: §10 requires determinism for
  replay; a grant that expires based on wall-clock time would make
  `AuthorizeMutation`'s outcome depend on when replay runs rather than what
  happened, breaking FR-OBS-004.
- **Letting a matcher's grant lend authority to the AGENT/TOOL principal
  running it.** Rejected: `findGrant`'s matcher branch
  (`internal/domain/authz.go:214-221`) requires
  `r.Actor.Authority.CanHoldLifecycleAuthority()` even when a matcher
  reference is present — the grant authorizes the *matcher version*, but the
  transition still runs "under a trusted runtime principal" (the doc
  comment at line 216), never under the tool-evidence-supplying AGENT/TOOL
  principal itself. This keeps FR-OBL-004's "tool evidence satisfies an
  obligation only through its deterministic matcher" from becoming a route
  to lifecycle authority for low-authority content.

## Consequences / compatibility impact

- The FR-TOOL-003 amendment is documentation-only against current Phase 1
  code: `internal/domain` already implements only the completion-claim
  behavior (there is no code path where an AGENT principal satisfies
  `AuthorizeMutation`), so no Phase 1 code changes when this amendment
  lands. It matters for Phase 3 (semantic state engine, ADR 8) and Phase 5
  (semantic state tool schemas, ADR 18), which implement `context_resolve`
  itself and must not build the unreachable "AGENT resolves" branch.
- Any future relaxation of `CanHoldLifecycleAuthority()` to include AGENT
  (there is no current proposal to do so) would need to revisit this ADR's
  reasoning and the FR-TOOL-003 amendment together, since the amendment's
  premise is that the branch is currently unreachable.
- `ItemChange`'s missing fields for immutable properties mean adding a new
  mutable lifecycle property later requires a new `ItemChange` field plus a
  transition rule in `Apply`, not a schema-level unlock — this is
  intentional friction matching FR-DOM-008.

## Tests that lock the behavior

- Required: `internal/domain/authz_test.go` — `AuthorizeMutation`:
  all-or-nothing across multiple targets (one inaccessible target fails the
  whole request with `ErrNotFound` even if others are accessible and
  authorized); direct authority success/failure at each authority level;
  grant-based authorization success, expired grant (`ExpiresAtSeq` before
  `Seq`), revoked grant (`RevokedSeq` at or before `Seq`), and grant issued
  by an authority lower than the target's authority (must fail); a grant
  attempting to name AGENT/TOOL/RETRIEVED_CONTENT as issuer or grantee is
  rejected at `MutationGrant.Validate` before it ever reaches
  `AuthorizeMutation`.
- Required: `internal/domain/authz_test.go` —
  `AuthorizeSupersession`: equal-boundary success; `Within`-but-not-equal
  boundaries fail (regression test for the narrowing case this ADR calls
  out); authority downgrade (superseding < superseded) fails; actor lacking
  authority over the superseding item's own authority fails even when
  superseding ≥ superseded.
- Required: `internal/domain/obligation_test.go` (or extend
  `records_test.go`) — `ValidObligationTransition` table exhaustively
  (every `(from, to)` pair among the four statuses), asserting WAIVED
  accepts no outbound transition.
- Required: `internal/domain/records_test.go` — `ItemChange.Apply`: OPEN→
  RESOLVED succeeds; RESOLVED→OPEN fails `ErrInvalidTransition`;
  `LastUsedCall` moving backward fails; negative `AccessDelta` fails;
  residency change alongside goal status leaves goal status independently
  correct (FR-DOM-005 regression).
- Trace T02 (replacing a pin retires only the old version) and T06 (all
  lifecycle paths enforce the same authorization) in
  `docs/sdd-event-traces.md` are the integration-level fixtures Phase 3
  turns into `internal/store/storetest` cases once directive replacement and
  CompleteTask exist end-to-end; this ADR's unit tests are the
  Phase-1-reachable subset of what those traces require.

## Open questions

- Whether `MutationGrant.Grantee` matching on exact `TaskID`/`WorkflowID`/
  `AgentID` (`granteeMatches`, `internal/domain/authz.go:129-133`) is
  expressive enough once Phase 3 needs grants scoped to "any task the
  grantee currently owns" rather than one fixed task ID at issuance time.
- Whether CompleteTask's "never waives obligations or resolves goals owned
  by another scope" rule (FR-AUTH-003) needs its own dedicated function
  distinct from `AuthorizeMutation`, given it combines a multi-target
  all-or-nothing check with a same-task ownership filter not otherwise
  expressed in `MutationTarget`.
