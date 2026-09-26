# 16. Common mutation authorization, version replacement, residency/goal status, immutable snapshots

Status: Proposed
Date: 2026-09-25

## Context

FR-AUTH-001 requires every mutation to check access to its targets and
authority for the action; failure is atomic `ErrInvalidAuthorityPromotion`.
FR-AUTH-002 lets a source delegate a named action to a SYSTEM/HARNESS/USER
principal on named targets, but AGENT/TOOL/RETRIEVED_CONTENT can never issue
or receive lifecycle grants, and a grant must not carry authority its issuer
itself lacks over the target. FR-REL-006 requires a SUPERSEDES edge to have
the **same** access boundary on both endpoints, and requires that tool
output never suppress current state. FR-DIR-007 (as amended, SDD v0.6)
scopes Working-snapshot supersession to same-authority-**and**-boundary
items. FR-DOM-005 requires residency and goal status to be independent
axes. FR-DOM-008 makes content/kind/authority/IDs/scope/access/source/
sequence immutable. FR-TOOL-003 (as amended) states `context_resolve` never
performs a Resolve in V1.

## Decision

- `AuthorizeMutation` (`internal/domain/authz.go`) is the single entry point
  for every lifecycle mutation. All-or-nothing: every target must be
  accessible before any authority check runs, and access failure returns
  `ErrNotFound` uniformly (§9, ADR 6). Each target is authorized directly —
  actor holds `CanHoldLifecycleAuthority()` and `actor.Authority.AtLeast
  (target.Authority)` — or by an in-force `MutationGrant` for that
  action/target. `findGrant` now additionally requires `t.Access.Permits
  (g.Issuer)` alongside `g.Issuer.Authority.AtLeast(t.Authority)`: a grant
  can authorize only what its issuer could do directly, including access —
  an issuer scoped to task A can no longer name a target in task B and have
  a grantee in B pass authorization on the issuer's say-so.
- `AuthorizeGrantIssuance(g, targets)` is a new, separate check run when a
  grant is *issued or revoked* (not when it is applied): every named target
  must match `g.TargetIDs` exactly, the issuer must access every target
  (`ErrNotFound` on failure, so issuance cannot probe for existence), and
  the issuer's authority must be at least each target's. `store.Tx
  .InsertGrant`'s doc comment requires callers to run this check first — the
  store does not re-derive it, since only the caller has the target records'
  authority/boundary in hand at issuance time.
- Matcher grants are limited to `ActionAssertObligation`:
  `MutationGrant.Validate` now rejects a matcher grantee for any other
  action. A registered matcher evaluates its own obligation (FR-OBL-004);
  it must not be usable to receive a `waive_obligation` or `resolve` grant,
  which FR-AUTH-002 never contemplated for a matcher.
- Supersession (FR-REL-006) is checked by `AuthorizeSupersession`: boundaries
  **equal**, not `Within` (narrowing would hide the original from principals
  who could see it before); superseding authority ≥ superseded; actor's
  authority ≥ superseding item's own authority. The actor is now also
  restricted by kind: SYSTEM/HARNESS/USER may supersede as before; an AGENT
  actor may supersede **only** an AGENT item with an AGENT item (the keyed
  agent-write case, FR-TOOL-002); a TOOL or RETRIEVED_CONTENT actor can
  never create a SUPERSEDES edge, full stop — closing the gap where a TOOL
  actor could otherwise supersede TOOL content and suppress current state
  under FR-ING-005/§9's "tool output cannot ... supersede."
- FR-DIR-007's amendment (SDD v0.6) is implemented by the same equal-
  boundary rule `AuthorizeSupersession` already enforces: "same authority in
  the same task" is replaced by "same authority and access boundary,"
  because a task-wide Working item must not silently suppress an
  agent-restricted Working item that happens to share a task.
- `GoalStatus` transitions only OPEN→RESOLVED (`ItemChange.Apply`);
  reopening requires an authorized replacement creating a new OPEN item.
  Residency changes never touch `GoalStatus`.
- `ItemChange.Apply` bundles generation/residency/goal/retention/usage into
  one CAS-guarded, audit-atomic update: `store.Tx.UpdateItem` now takes the
  `LifecycleEvent` and writes it in the same transaction as the change,
  closing the gap where a caller could change an item without its required
  audit record.
- Obligation transitions follow `domain.ValidObligationTransition`
  (UNRESOLVED↔BLOCKED, UNRESOLVED↔SATISFIED, any status→WAIVED terminal).
  `store.Tx.AppendObligationTransition` now applies the transition and
  returns the updated version atomically, rather than leaving status and
  transition history as two separate writes a caller could split.

## SDD amendment (applied in v0.6)

FR-TOOL-003 is applied as amended (commit `78d988f`): "context_resolve never
resolves in V1, because Resolve requires a SYSTEM, HARNESS, or USER
principal (FR-AUTH-001) and the tools cannot create goals. For an accessible
goal it records a completion claim ... An unknown or inaccessible ID is a
tool error." This removes the unreachable "resolves only items the agent has
authority over" clause entirely, rather than leaving it beside the invariant
it appeared to soften.

FR-DIR-007 is applied as amended: Working-section supersession now reads
"supersedes every current Working item of the same authority and access
boundary in the same task," matching `AuthorizeSupersession`'s equal-
boundary rule rather than conflicting with it.

## Alternatives considered

- **Checking issuer access only at grant-application time (`findGrant`),
  not at issuance.** Rejected: the target's access boundary is exactly what
  the grant should have been checked against when it was created; deferring
  the check to application time still lets an out-of-boundary grant sit in
  storage looking valid, and `AuthorizeGrantIssuance` catches the mistake at
  the point a caller can still refuse to store it.
- **Allowing matcher grants for any action, trusting the matcher registry to
  self-limit.** Rejected: FR-AUTH-002 only ever describes a matcher
  evaluating its obligation; allowing the grant type more broadly is an
  unforced widening of what a non-principal grantee can do.
- **Permitting a TOOL actor to supersede same-boundary TOOL content (treated
  as symmetric with the AGENT same-key exception).** Rejected: FR-TOOL-002's
  keyed-write exception is scoped to AGENT specifically; TOOL/RETRIEVED_
  CONTENT superseding anything, including their own kind, is exactly the
  "tool output suppresses state" case §9 forbids.
- **`Within` instead of equality for supersession/Working-snapshot
  boundaries.** Rejected: permits narrowing, which both FR-REL-006 and the
  amended FR-DIR-007 forbid.
- **Splitting `UpdateItem`/`AppendObligationTransition` into a mutation call
  plus a caller-written audit call.** Rejected: nothing then prevents a
  transaction from committing the mutation without its audit record; atomic
  methods make the two inseparable at the store layer instead of trusting
  every caller to pair them.

## Consequences / compatibility impact

- `AuthorizeGrantIssuance` is a new required call site for every grant-
  issuing/revoking path; any Phase 3+ code that calls `store.Tx.InsertGrant`
  directly without it violates the store's own doc comment.
- The AGENT-same-kind restriction on `AuthorizeSupersession` means any
  future feature that wants AGENT to supersede a non-AGENT item (none
  currently proposed) needs its own ADR, not a loosening of this function.
- `UpdateItem`/`AppendObligationTransition`'s new atomic-audit signatures are
  breaking changes to the Phase 1 store interface; no production data exists
  yet, so this is a clean signature change, not a migration.
- The FR-TOOL-003/FR-DIR-007 amendments are applied; no further SDD change
  is pending for this ADR's scope.

## Tests that lock the behavior

- `internal/domain/authz_test.go`: `AuthorizeMutation` all-or-nothing;
  direct-authority success/failure per level; grant success, expired/
  revoked grant, under-authority issuer, and now an issuer lacking access to
  the target (must fail even with sufficient authority rank).
- `internal/domain/authz_test.go`: `AuthorizeGrantIssuance` — issuer access
  and authority required per target; a target list not matching
  `TargetIDs` rejected; revocation runs the same check.
- `internal/domain/authz_test.go`: `MutationGrant.Validate` rejects a
  matcher grantee for any action other than `ActionAssertObligation`.
- `internal/domain/authz_test.go`: `AuthorizeSupersession` equal-boundary
  success for SYSTEM/HARNESS/USER; AGENT-AGENT success; AGENT superseding a
  non-AGENT item fails; TOOL/RETRIEVED_CONTENT actor always fails regardless
  of boundary or authority match.
- `internal/domain/obligation_test.go`: `ValidObligationTransition`
  exhaustive; WAIVED terminal.
- `internal/store/storetest`: `UpdateItem` fails without a matching
  `TargetItem`/`TargetID` event allocated in the same transaction;
  `AppendObligationTransition` returns the updated version with `Status`/
  `EvidenceIDs` set from the transition, not a separately written value.
- Traces T02, T06, and T18 (Working snapshots) are the Phase 3 integration
  fixtures once directive replacement and CompleteTask exist end-to-end.

## Open questions

- Whether `granteeMatches`'s exact-ID matching is expressive enough once
  Phase 3 needs grants scoped to "any task the grantee currently owns."
- Whether CompleteTask's same-task-ownership filter (FR-AUTH-003) needs its
  own function distinct from `AuthorizeMutation`.

## Review

Scrutinized by Codex gpt-6-sol xhigh (`codex-decision-review-out.md`,
findings 1, 2, 6, 8, 10, 11, 12). Changed: `findGrant` now checks issuer
access to the target (finding 1); added `AuthorizeGrantIssuance` for the
issue/revoke path (finding 1); `AuthorizeSupersession` rejects TOOL/
RETRIEVED_CONTENT actors outright and restricts AGENT to AGENT-on-AGENT
(finding 2); matcher grants limited to `ActionAssertObligation` (finding
10); `UpdateItem`/`AppendObligationTransition` made atomic with their audit
records (finding 8); FR-DIR-007 amended to same-boundary, matching the
supersession rule instead of conflicting with it (finding 11); FR-TOOL-003's
amendment reported as applied rather than proposed (finding 12).
