# 16. Common mutation authorization, version replacement, residency/goal status, immutable snapshots

Status: Proposed
Date: 2026-09-25

## Context

FR-AUTH-001 requires every mutation to check access to its targets and
authority for the action; failure is atomic `ErrInvalidAuthorityPromotion`.
FR-AUTH-002 lets a source delegate a named action to a SYSTEM/HARNESS/USER
principal, but AGENT/TOOL/RETRIEVED_CONTENT can never issue or receive
lifecycle grants. FR-REL-006 requires a SUPERSEDES edge to have the **same**
access boundary on both endpoints (not merely `Within`), so a replacement
can never hide an item from a principal who could see the original.
FR-DOM-005 requires residency and goal status to be independent axes.
FR-DOM-008 makes content/kind/authority/IDs/scope/access/source/sequence
immutable. FR-TOOL-003 and FR-AUTH-001 jointly raise a conflict below.

## Decision

- `AuthorizeMutation` (`internal/domain/authz.go:172-196`) is the single
  entry point for every lifecycle mutation. All-or-nothing: every target
  must be accessible before any authority check runs, and access failure
  returns `ErrNotFound` uniformly (§9, ADR 6). Each target is authorized
  directly — actor holds `CanHoldLifecycleAuthority()`
  (`internal/domain/enums.go:71-74`) and `actor.Authority.AtLeast(target
  .Authority)` — or by an in-force `MutationGrant` for that action/target,
  issued by a principal whose authority is at least the target's
  (`findGrant`, `internal/domain/authz.go:198-228`).
- `Authority.AtLeast` (`internal/domain/enums.go:32-38`): equal or strictly
  higher rank. `AuthorityTool`/`AuthorityRetrievedContent` share rank 1 and
  do not dominate each other.
- `MutationGrant` validity uses session sequence numbers only —
  `IssuedSeq`/`ExpiresAtSeq`/`RevokedSeq` (`activeAt`,
  `internal/domain/authz.go:117-127`) — never wall-clock time (§10).
  `MutationGrant.Validate` (`internal/domain/authz.go:73-105`) rejects
  AGENT/TOOL/RETRIEVED_CONTENT as issuer or grantee structurally.
- Supersession (FR-REL-006) is checked separately by
  `AuthorizeSupersession` (`internal/domain/authz.go:237-252`): access to
  both endpoints; superseding authority ≥ superseded (no downgrade via
  replacement); actor's authority ≥ superseding item's own authority (no
  laundering through a low-authority actor); and boundaries **equal**, not
  `Within` — `Within` alone would let a narrower replacement supersede a
  broader original, hiding it from principals who could see it before.
  Boundary widening/narrowing requires an explicit authorized replacement
  policy, never ID reuse (FR-DIR-002).
- `GoalStatus` transitions only OPEN→RESOLVED; `ItemChange.Apply`
  (`internal/domain/records.go:224-256`) rejects any other transition with
  `ErrInvalidTransition`. Reopening requires an authorized replacement
  creating a new OPEN item (FR-DIR-005), not a mutation. Residency changes
  never touch `GoalStatus` (FR-DOM-005).
- `ItemChange.Apply` bundles generation/residency/goal/retention/usage into
  one CAS-guarded update (`store.Tx.UpdateItem` checks `expectedVersion`,
  `internal/store/store.go:141-144`; `Apply` increments `Version`).
  Immutable fields (FR-DOM-008) have no `ItemChange` field at all, so the
  type system, not a runtime check, prevents mutating them.
- Obligation transitions follow `domain.ValidObligationTransition`
  (`internal/domain/obligation.go:24-36`): UNRESOLVED↔BLOCKED,
  UNRESOLVED↔SATISFIED, any status→WAIVED (terminal).
  `AuthorizeMutation`/`AuthorizeSupersession` govern *who*; this table
  governs *which* transitions exist; `ObligationTransition.Validate`
  combines both before a transition is stored.

## Required SDD amendment

**Conflict:** FR-TOOL-003 says `context_resolve` "resolves only items the
agent has authority over (FR-AUTH-001)." But FR-AUTH-001 requires
SYSTEM/HARNESS/USER for Resolve, which AGENT never satisfies
(`CanHoldLifecycleAuthority()`), and FR-TOOL-002/§9 confine the
semantic-state tools to AGENT authority with no goal/pin/obligation
creation. So the "agent has authority over" branch describes an empty set:
`context_resolve` as specified can never perform a Resolve.

**Proposed replacement text**, FR-TOOL-003, first sentence:

> context_resolve always records a completion claim: an evidence item
> DERIVED_FROM the cited evidence, linked to the target goal with
> REFERENCES, with the tool result stating the goal stays OPEN until an
> authorized Resolve or CompleteTask. It never performs the Resolve mutation
> itself, regardless of the target's authority.

This drops the unreachable "agent has authority" clause and makes the
completion-claim behavior (already FR-TOOL-003's second sentence for the
higher-authority case) the only behavior.

**Alternative rejected:** leaving the text as dead specification. An
ambiguous requirement beside the invariant it appears to soften is a hazard
even if currently unreachable — a future implementer could misread it as
license to weaken FR-AUTH-001's gate.

## Alternatives considered

- **Reusing `AuthorizeMutation`'s loop for supersession instead of a
  dedicated function.** Rejected: same-boundary is stricter than, and
  structurally different from, per-target access+authority; a dedicated
  function keeps the FR-REL-006 rationale attached to the code enforcing it.
- **`Within` instead of equality for supersession boundaries.** Rejected:
  permits narrowing, which FR-REL-006 forbids.
- **Wall-clock grant expiry.** Rejected: breaks §10/FR-OBS-004 replay
  determinism.
- **Letting a matcher's grant lend authority to the AGENT/TOOL principal
  running it.** Rejected: `findGrant`'s matcher branch still requires
  `CanHoldLifecycleAuthority()` on the actor — the grant authorizes the
  matcher version, but the transition runs under a trusted runtime
  principal, never the tool-evidence-supplying principal itself.

## Consequences / compatibility impact

- The FR-TOOL-003 amendment is documentation-only against Phase 1 code (no
  path lets AGENT satisfy `AuthorizeMutation` today); it matters once Phase
  3/5 implement `context_resolve` itself.
- Any future relaxation of `CanHoldLifecycleAuthority()` to include AGENT
  must revisit this ADR and the amendment together.
- Adding a new mutable lifecycle property later requires a new `ItemChange`
  field plus a transition rule, not a schema-level unlock — intentional,
  per FR-DOM-008.

## Tests that lock the behavior

- `internal/domain/authz_test.go`: `AuthorizeMutation` all-or-nothing across
  targets; direct-authority success/failure per level; grant success,
  expired grant, revoked grant, under-authority issuer; grants naming
  AGENT/TOOL/RETRIEVED_CONTENT rejected at `Validate`.
- `internal/domain/authz_test.go`: `AuthorizeSupersession` equal-boundary
  success; `Within`-but-not-equal failure (the narrowing regression);
  authority downgrade failure; actor-under-superseding-authority failure.
- `internal/domain/obligation_test.go`: `ValidObligationTransition`
  exhaustive over all `(from, to)` pairs; WAIVED has no outbound transition.
- `internal/domain/records_test.go`: `ItemChange.Apply` OPEN→RESOLVED
  succeeds, RESOLVED→OPEN fails, `LastUsedCall` regression fails, negative
  `AccessDelta` fails, residency change leaves goal status independent.
- Traces T02 and T06 (`docs/sdd-event-traces.md`) are the Phase 3
  integration fixtures once directive replacement and CompleteTask exist
  end-to-end; the unit tests above are the Phase-1-reachable subset.

## Open questions

- Whether `granteeMatches`'s exact-ID matching is expressive enough once
  Phase 3 needs grants scoped to "any task the grantee currently owns."
- Whether CompleteTask's same-task-ownership filter (FR-AUTH-003) needs its
  own function distinct from `AuthorizeMutation`.
