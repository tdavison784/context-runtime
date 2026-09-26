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
- `AuthorizeGrantIssuance(g, targets)` is the check run when a grant is
  *issued*. `sameTargetSet` first requires `g.TargetIDs` to contain no
  duplicates and `targets` to be exactly that set, one record per ID —
  neither a target missing from the supplied list nor a repeated ID (e.g.
  `TargetIDs [A,B]` with `targets [A,A]`) can pass. `authorizeOver` then
  requires the issuer to access every target (`ErrNotFound` first, so
  issuance cannot probe for existence) and hold authority at least each
  target's. `store.Tx.InsertGrant`'s doc comment requires callers to run
  this check first — the store does not re-derive it, since only the caller
  has the target records' authority/boundary in hand at issuance time.
- `AuthorizeGrantRevocation(actor, g, targets)` is the separate check run
  when a grant is *revoked*: it takes an explicit revoking `actor` (not the
  original issuer), requires the same session and the same exact target set
  as issuance, and requires the actor be SYSTEM/HARNESS/USER with access and
  authority over every target — an actor that fails the authority test still
  gets `ErrNotFound` first if it also lacks access, so revocation cannot
  disclose target authority to a principal that cannot see the target.
  `store.Tx.RevokeGrant` now takes the audit `LifecycleEvent` (`TargetGrant`)
  and applies it atomically with the revocation, and requires callers to run
  this check first.
- Matcher grants are limited to `ActionAssertObligation`:
  `MutationGrant.Validate` now rejects a matcher grantee for any other
  action. A registered matcher evaluates its own obligation (FR-OBL-004);
  it must not be usable to receive a `waive_obligation` or `resolve` grant,
  which FR-AUTH-002 never contemplated for a matcher.
- Supersession (FR-REL-006) is checked by `AuthorizeSupersession`, in this
  order: **access first** — both endpoints' `Access.Permits(actor)` are
  checked before any authority-kind test, so an inaccessible endpoint always
  yields `ErrNotFound` regardless of the actor's or the endpoint's
  authority. This ordering itself is load-bearing: checking authority kind
  first (as v2 did) let a caller distinguish an inaccessible AGENT item
  (`ErrNotFound` from the AGENT-kind branch) from an inaccessible USER item
  (`ErrInvalidAuthorityPromotion` from the authority-kind branch) purely
  from the error returned, disclosing the item's authority class without
  ever granting access to it. After access, the actor is restricted by
  kind: SYSTEM/HARNESS/USER may supersede as before (subject to the
  boundary-equality and authority-ordering checks below); an AGENT actor
  may supersede **only** a keyed agent write with the identical key
  (FR-TOOL-002): both items must be AGENT authority **and** share the same
  `"agent.<key>"` `DirectiveID` — not merely "both AGENT," which would let
  one keyed write supersede an unrelated one. A TOOL or RETRIEVED_CONTENT
  actor can never create a SUPERSEDES edge, full stop. Boundaries must
  still be **equal**, not `Within` (narrowing would hide the original from
  principals who could see it before); superseding authority ≥ superseded;
  actor's authority ≥ superseding item's own authority.
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
  `store.Tx.AppendObligationTransition(t, expectedRevision)` applies the
  transition and returns the updated version atomically, and now also takes
  `expectedRevision` under compare-and-swap: the version's stored `Revision`
  must equal it or the write fails `ErrVersionConflict`. This closes an ABA
  race this ADR's authorization matrix cannot close by itself — a matcher
  can evaluate evidence against one obligation revision, a resource change
  can move the same obligation through SATISFIED and back to UNRESOLVED
  (revision N+2), and without the CAS the matcher's stale transition would
  still satisfy `From == UNRESOLVED` and apply anyway, applying proof
  evaluated against a state the obligation is no longer in (FR-OBL-005,
  INV-16). The applicability-fingerprint and matcher-versioning rules that
  motivate *why* freshness matters are ADR 8's territory (Phase 3); this
  ADR only fixes the authorization-adjacent mechanism — the transition
  cannot commit against a revision the evaluator didn't actually see.

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
- **Letting the AGENT exception match on authority alone ("both items
  AGENT"), without requiring the same keyed `DirectiveID`.** Rejected: that
  would let one agent key's write supersede an unrelated agent key's item as
  long as both happen to be AGENT authority in the same boundary —
  FR-TOOL-002 only ever describes a write to an *existing key* superseding
  its own previous version, not cross-key suppression.
- **Checking actor-authority kind before endpoint access in
  `AuthorizeSupersession`.** Rejected: this is what v2 shipped, and it lets a
  caller learn an inaccessible endpoint's authority class from which error
  comes back (`ErrNotFound` vs. `ErrInvalidAuthorityPromotion`) — exactly
  the disclosure §9 and ADR 6 forbid. Access must be checked first,
  unconditionally.
- **A single grant-authorization function reused for issuance and
  revocation, keyed on the original issuer.** Rejected: revocation is a
  distinct action taken by a distinct (possibly different) actor;
  `AuthorizeGrantRevocation` takes that actor explicitly rather than
  assuming only the original issuer may ever revoke.
- **`Within` instead of equality for supersession/Working-snapshot
  boundaries.** Rejected: permits narrowing, which both FR-REL-006 and the
  amended FR-DIR-007 forbid.
- **Splitting `UpdateItem`/`AppendObligationTransition` into a mutation call
  plus a caller-written audit call.** Rejected: nothing then prevents a
  transaction from committing the mutation without its audit record; atomic
  methods make the two inseparable at the store layer instead of trusting
  every caller to pair them.

## Consequences / compatibility impact

- `AuthorizeGrantIssuance` and `AuthorizeGrantRevocation` are required call
  sites for every grant-issuing/revoking path; any Phase 3+ code that calls
  `store.Tx.InsertGrant`/`RevokeGrant` directly without them violates the
  store's own doc comment.
- The AGENT-same-key restriction on `AuthorizeSupersession` means any future
  feature that wants AGENT to supersede a different key or a non-AGENT item
  (none currently proposed) needs its own ADR, not a loosening of this
  function.
- `UpdateItem`/`AppendObligationTransition`/`RevokeGrant`'s new atomic-audit
  and CAS signatures are breaking changes to the Phase 1 store interface; no
  production data exists yet, so this is a clean signature change, not a
  migration.
- The FR-TOOL-003/FR-DIR-007 amendments are applied; no further SDD change
  is pending for this ADR's scope.

## Tests that lock the behavior

- `internal/domain/authz_test.go`: `TestAuthorizeMutation_AllOrNothing`,
  `TestAuthorizeMutation_DirectAuthoritySucceeds`,
  `TestAuthorizeMutation_GrantExpiry`,
  `TestAuthorizeMutation_GrantNotYetIssuedIgnored`,
  `TestAuthorizeMutation_IssuerWithoutTargetAccessIgnoresGrant` and
  `TestAuthorizeMutation_IssuerWithTargetAccessSucceeds` (an issuer lacking
  access to the target fails even with sufficient authority rank);
  `TestAuthorizeMutation_InaccessibleTargetReturnsNotFoundBeforeAuthority`.
- `internal/domain/authz_test.go`: `TestAuthorizeGrantIssuance` (issuer
  access/authority per target; duplicate IDs in `TargetIDs` and a
  `targets` list with a repeated or missing ID rejected — the exact
  `TargetIDs[A,B]`/`targets[A,A]` case from finding N3) and
  `TestAuthorizeGrantRevocation` (non-SYSTEM/HARNESS/USER actor fails; an
  actor lacking access fails `ErrNotFound` even with sufficient authority; a
  different-session actor fails; the same exact-target-set check as
  issuance applies).
- `internal/domain/authz_test.go`: `TestMutationGrantValidate` includes the
  matcher-action restriction (a matcher grantee rejected for any action
  other than `ActionAssertObligation`).
- `internal/domain/authz_test.go`: the full `TestAuthorizeSupersession_*`
  family —
  `AccessCheckedBeforeAuthority` and `InaccessibleEndpointNotFound` (the
  access-ordering regression from finding N4: `ErrNotFound` regardless of
  actor or endpoint authority kind); `DifferentAccessBoundariesFail`
  (equality, not `Within`); `AgentSupersedingAgentOK`,
  `AgentDifferentKeyFails`, `AgentNonKeyedDirectiveIDFails`,
  `AgentSupersedingUserFails` (the same-key restriction); `ToolActorNever
  Supersedes`, `RetrievedContentActorNeverSupersedes`,
  `ToolActorFailsRegardlessOfItemAuthority`;
  `SupersedingBelowSupersededAuthorityFails`,
  `ActorBelowSupersedingAuthorityFails`; `DifferentSessionsFail`,
  `InvalidActorPropagates`.
- **Integration-level, not just unit-level:** `internal/graph/graph_test.go`
  exercises the same rules through the actual `Supersede`/`SupersedeSnapshot`
  operations, not just `domain.AuthorizeSupersession` in isolation —
  `TestSupersede_AuthorizationRules` (subtests
  `AgentCannotSupersedeUser`, `AgentSupersedesAgentAllowed`,
  `AgentCannotSupersedeDifferentKeyAgentItem`,
  `AgentActorInaccessibleItemIsNotFoundNotPromotionError`);
  `TestSupersede_CycleRejected`; `TestReplaceDirective_T02` (trace T02,
  directive replacement) and `TestReplaceDirective_KeyedAgentWriteChain_T17`;
  `TestSupersedeSnapshot_FRDIR007` is trace T18 exactly — a task-wide
  Working item does *not* supersede an agent-restricted item sharing the
  task, confirming the amended FR-DIR-007 end-to-end — plus
  `TestSupersedeSnapshot_MultipleOldItems`.
- `internal/domain/obligation_test.go`: `TestValidObligationTransitionMatrix`,
  `TestValidObligationTransition_WaivedIsTerminal`,
  `TestValidObligationTransition_InvalidStatusesRejected`.
- `internal/store/storetest` (`storetest.Run`): `TestConformance
  /ObligationTransitions` (`testObligationTransitions`) is the exact ABA
  regression test for finding N2 — a transition proposed against a stale
  `expectedRevision` fails `ErrVersionConflict` even when `From` still
  matches the version's current status; `TestConformance/UpdateItem` checks
  the atomic audit-event requirement; `TestConformance/Grants`
  (`testGrants`) checks `RevokeGrant`'s atomic `TargetGrant` audit event
  and rejects a revocation whose event targets the wrong grant or record
  kind. `internal/store/sqlite/durability_test.go
  :TestObligationTransitionCAS` and `:TestAuditedGrantAndTaskMutations`
  reconfirm both against the SQLite typed-column write path.
- Trace T06 (all lifecycle paths enforce the same authorization) has no
  dedicated fixture yet — it needs the semantic-state/tools layer (goals,
  CompleteTask) that Phase 3 builds; `TestAuthorizeMutation_T06_*` in
  `authz_test.go` (`UserCannotActOnSystemGoal`,
  `HarnessCannotAssertWithoutGrant`,
  `SystemGrantedMatcherLetsHarnessAssert`) already cover its authorization
  core at the domain-function level, but the full multi-actor trace across
  Resolve/CompleteTask/Block/Waive remains a Phase 3 integration gap.

## Open questions

- Whether `granteeMatches`'s exact-ID matching is expressive enough once
  Phase 3 needs grants scoped to "any task the grantee currently owns."
- Whether CompleteTask's same-task-ownership filter (FR-AUTH-003) needs its
  own function distinct from `AuthorizeMutation`.

## Review

First pass (Codex gpt-6-sol xhigh, `codex-decision-review-out.md`, findings
1, 2, 6, 8, 10, 11, 12): `findGrant` checks issuer access to the target;
added `AuthorizeGrantIssuance`; `AuthorizeSupersession` rejects TOOL/
RETRIEVED_CONTENT actors and restricts AGENT to AGENT-on-AGENT; matcher
grants limited to `ActionAssertObligation`; `UpdateItem`/
`AppendObligationTransition` made atomic with their audit records;
FR-DIR-007 amended to same-boundary; FR-TOOL-003's amendment reported as
applied.

Second pass (Codex gpt-6-sol xhigh, `codex-contract-v2-review.md`, findings
N2, N3, N4, verifying PARTIAL on findings 1, 2, 8): the first pass's fixes
were each incomplete. Changed: `AuthorizeGrantIssuance` now rejects
duplicate/mismatched target sets via `sameTargetSet`, not just length
equality (N3); added `AuthorizeGrantRevocation` with an explicit revoking
actor, since the first pass never added a revocation-side check at all
(N3); `AuthorizeSupersession` now checks both endpoints' access **before**
the actor-authority-kind switch, closing the authority-class disclosure the
first pass's ordering still permitted (N4); the AGENT exception now
requires the identical `"agent.<key>"` `DirectiveID` on both items, not
just "both AGENT" (N4, tightening finding 2's original fix);
`AppendObligationTransition` now takes `expectedRevision` under CAS,
closing the ABA race the first pass's atomic-but-unversioned transition
still allowed (N2).

Verified against the integrated `phase-1-foundation` codebase (tip
`ddbb53e`): every fix above has a passing unit test in
`internal/domain/authz_test.go`/`obligation_test.go`, plus real
integration coverage in `internal/graph/graph_test.go`
(`TestSupersede_AuthorizationRules`, `TestSupersedeSnapshot_FRDIR007`) that
exercises the same rules through the actual `Supersede`/
`SupersedeSnapshot` operations, not only the isolated domain functions.
