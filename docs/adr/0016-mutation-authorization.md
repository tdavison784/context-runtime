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
  may supersede **only** a keyed agent write with the identical key **in
  the same task** (round 1, AUTH-1.6): both items must be AGENT authority,
  share the same `"agent.<key>"` `DirectiveID`, **and** share `TaskID` — not
  merely the same key, which (since FR-TOOL-002 keys are per task) would
  let an AGENT actor in task T2 supersede task T1's `agent.status` item. A
  TOOL or RETRIEVED_CONTENT actor can never create a SUPERSEDES edge, full
  stop. Boundaries must still be **equal**, not `Within` (narrowing would
  hide the original from principals who could see it before); superseding
  authority ≥ superseded; actor's authority ≥ superseding item's own
  authority.
- FR-DIR-007's amendment (SDD v0.6) is implemented by the same equal-
  boundary rule `AuthorizeSupersession` already enforces: "same authority in
  the same task" is replaced by "same authority and access boundary,"
  because a task-wide Working item must not silently suppress an
  agent-restricted Working item that happens to share a task.
- **Working-snapshot selection is by recorded `Section`, not `Kind` (round
  1, SPEC-1.1, SDD v0.7).** `SupersedeSnapshot` selects every current item
  whose `Section == domain.SectionWorking`, in the given task, with the
  same authority and boundary as the new snapshot — whatever `Kind` those
  items were given. Selecting by `Kind` instead (the pre-round-1 behavior)
  had two failure modes reproduced by the reviewer: FR-DIR-003 permits a
  Working item with `kind=conversation`, so a kind-based selector could
  leave an old `conversation`-kind Working item current forever (never
  superseded because it doesn't look like the selector's expected kind);
  and a selector that instead swept broadly by task/authority/boundary
  alone could supersede an unrelated `task_state` item that merely shares
  those three fields but was never a Working snapshot. `Section` is
  immutable provenance recorded at creation (ADR 4), so neither failure mode
  is reachable: the selector's candidate set is exactly "items a Working
  section created," independent of what kind they were declared with.
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
- **`ObligationTransition.Action` must equal `domain.TransitionAction(From,
  To)` (round 1, AUTH-1.4).** The transition table (`ValidObligationTransition`)
  said *which* `(From, To)` pairs exist; nothing previously bound a stored
  transition to *which lifecycle action authorized it*. `TransitionAction`
  is the map: any transition into WAIVED is `ActionWaiveObligation`; into
  BLOCKED is `ActionBlockObligation`; out of BLOCKED (back to UNRESOLVED) is
  `ActionUnblockObligation`; everything else (UNRESOLVED↔SATISFIED) is
  `ActionAssertObligation`. `ObligationTransition.Validate` now also
  requires: the actor's `SessionID` equal the transition's `SessionID`; the
  actor `CanHoldLifecycleAuthority()` (SYSTEM/HARNESS/USER — AGENT/TOOL/
  RETRIEVED_CONTENT can never drive a transition, matching §9); and a
  `Matcher`-attributed transition have `Action == ActionAssertObligation` —
  a matcher can never be recorded as having blocked, unblocked, or waived,
  regardless of what a caller passes in `Action`. Before this, a mutation
  correctly authorized as `ActionAssertObligation` (e.g. under a matcher
  grant scoped only to that action, per this ADR's matcher-grant
  restriction) could still be *stored* as a WAIVED transition with a
  different, unchecked `Action` value or none at all — `AuthorizeMutation`
  never sees `To`, so nothing connected "what was authorized" to "what got
  written." Binding `Action` to the table closes that gap at the record
  level, where `AuthorizeMutation` cannot reach.
- **`LifecycleEvent.Validate` requires `Actor.SessionID == SessionID`
  (round 1, AUTH-1.7).** `EventRecord` and `MutationGrant` already enforced
  this; `LifecycleEvent` — the append-only audit record every other
  mutation in this ADR writes — did not, so an audit entry could misattribute
  an action to a principal from another session. Matches the same rule now
  on `ObligationTransition.Actor` above.
- **`internal/graph.LinkDerived` requires actor authority ≥ the derived
  item's authority (round 1, AUTH-1.1, graph-worker's fix).** Before this,
  `LinkDerived` checked only access and `CheckDerivedBoundary`, never the
  actor's authority against the item it was attaching provenance to — any
  AGENT, TOOL, or RETRIEVED_CONTENT actor that could merely *see* a SYSTEM
  or HARNESS item could attach `DERIVED_FROM` edges and `Coverage` to it at
  will, rewriting that item's provenance after the fact. This is FR-REL-006's
  "source authority cannot be laundered through derivation" applied to
  provenance attachment specifically, not just supersession: ADR 6 makes a
  covered item's continued eligibility a dispatch-time recheck, so a
  low-authority actor attaching a short-TTL or TURN-scoped source to a
  higher-authority item is a way to *later* force that higher-authority
  item's representation out of context — suppressing higher-authority
  content, which §9 forbids. The fix costs nothing for the only specified
  callers (FR-TOOL-002/003 only ever link an agent's own new item): require
  `actor.Authority.AtLeast(derived.Authority)`, and never TOOL or
  RETRIEVED_CONTENT.
- **`internal/graph.ReplaceDirective`'s first-version path requires the same
  actor rule as supersession (round 1, AUTH-1.5, graph-worker's fix).** When
  no current version exists yet for a directive ID, the prior code checked
  only access before setting the pointer — a RETRIEVED_CONTENT or TOOL
  actor could file the *first* version of a directive, including one
  claiming SYSTEM authority. The fix applies `AuthorizeSupersession`'s
  actor rules (SYSTEM/HARNESS/USER, or AGENT restricted to its own
  `agent.<key>` item) plus `actor.Authority.AtLeast(newItem.Authority)` on
  this path too, so "there was nothing to supersede yet" is never a way
  around the authority check that would otherwise apply.
- **Missing and inaccessible now yield the identical bare `ErrNotFound`
  everywhere in `internal/graph` (round 1, AUTH-1.3, graph-worker's fix).**
  `errors.Is` already treated them alike, but `err.Error()` did not: a
  missing item's error text passed the store's wrapped message through
  (e.g. `"item nothere: not found"`), while an inaccessible item's text was
  bare `"not found"` — and since Phase 1's semantic-write tools surface
  errors as text to the model (FR-TOOL-002/003: "an unknown or inaccessible
  ID is a tool error"), the message itself was the leak, not just the
  sentinel. Worse, an operation loading two IDs (e.g. `Supersede(new, old)`)
  named whichever ID it happened to check first in its error, so which ID
  came back in the message could reveal that the *other* one exists. The
  fix routes every graph-layer existence/access check through one helper
  that (a) checks access before loading the next ID, so ordering never
  leaks which of several IDs exists, and (b) normalizes any not-found from
  the store to bare `domain.ErrNotFound`, with no item ID or store-specific
  text attached.

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

## SDD amendment (applied in v0.7)

Round-1 finding SPEC-1.1 (Working-snapshot selection erasing unrelated
state or leaving a wrong-kind Working item current forever) is resolved by
amending FR-DIR-007: "supersedes every current Working-**section** item
(identified by its recorded directive section, whatever its kind)" replaces
kind-based selection. FR-DIR-004 is amended to name the recorded directive
section as parser output (commit `4329e29`).

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
- **Selecting Working-snapshot candidates by `Kind` plus a fixed kind list
  (e.g. treating `task_state` and `conversation` as "the Working kinds").**
  Rejected (SPEC-1.1): FR-DIR-003 explicitly allows a Working item to
  declare any `kind=` value, so a fixed list is either incomplete (misses a
  legitimately-declared kind, leaving it perpetually current) or overbroad
  (matches an unrelated item that merely shares a common kind like
  `task_state`). Recording `Section` at creation and selecting by it is the
  only approach that doesn't require guessing the kind space in advance.
- **Requiring only `actor.Authority.AtLeast(derived.Authority)` for
  `LinkDerived` without also barring TOOL/RETRIEVED_CONTENT explicitly.**
  Rejected: `AtLeast` alone would let a TOOL actor attach provenance to
  another TOOL item (equal rank), which is still "tool output" attaching
  itself as another tool item's basis — the same category of laundering
  FR-ING-005/§9 forbid for supersession. Barring TOOL/RETRIEVED_CONTENT
  outright, as `AuthorizeSupersession` already does, keeps the two
  provenance-mutating operations under one consistent actor rule.
- **Leaving graph-layer error text as-is, relying on `errors.Is` alone.**
  Rejected (AUTH-1.3): FR-TOOL-002/003 make an unknown-or-inaccessible-ID
  error a *tool result*, i.e. text the model reads directly — `errors.Is`
  equivalence doesn't stop a distinguishable message string from leaking
  which of two IDs exists.

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
- The FR-TOOL-003/FR-DIR-007/FR-DIR-004 amendments are applied; no further
  SDD change is pending for this ADR's scope.
- `Section` is now load-bearing for correctness (Working-snapshot
  selection), not just descriptive metadata; any future ingestion code that
  constructs a Working-section item without setting `Section` silently
  breaks FR-DIR-007, with no structural check catching the omission beyond
  `Validate`'s "non-empty Section requires a DirectiveID" rule (which does
  not itself require Working items to set `Section`).
- `ObligationTransition.Action` binding, `LifecycleEvent`'s session check,
  and the AGENT same-task rule are all breaking changes to record shapes
  already used by `internal/store/storetest`'s conformance suite and
  `internal/graph`; every existing test fixture that constructs these
  records must be updated to supply a valid `Action`/session/`TaskID`, not
  just new tests added.
- Normalizing graph-layer errors to bare `ErrNotFound` means any caller
  that was inspecting graph-layer error *text* (not just `errors.Is`) for
  diagnostics loses that detail; this is intentional (the detail was the
  leak) but is a user-visible behavior change for any Phase 1 tooling that
  logged the richer message.

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

### Round 1 additions (findings AUTH-1.1, 1.3, 1.4, 1.5, 1.6; SPEC-1.1) — landed

`internal/domain` and `internal/graph`'s round-1 fixes and tests are now
merged and passing (`go test -race ./internal/domain/... ./internal/graph/...`
green).

- `internal/domain/obligation_test.go`: `TestTransitionActionMatrix` and
  `TestTransitionAction_InvalidStatusesRejected` lock `TransitionAction`
  exhaustively (AUTH-1.4); the existing transition-validation tests were
  updated in place to supply a valid `Action` per case rather than gaining
  new standalone cases.
- `internal/domain/authz_test.go`: `TestAuthorizeSupersession_AgentSameKeySameTaskOK`
  and `TestAuthorizeSupersession_AgentSameKeyDifferentTaskFails` lock the
  AUTH-1.6 regression exactly (equal key, equal boundary, differing
  `TaskID` → `ErrInvalidAuthorityPromotion`).
- `internal/domain/records_test.go`: `TestLifecycleEventValidate`'s "actor
  belongs to another session" case locks AUTH-1.7.
- `internal/domain/item_test.go`: `TestDirectiveSectionValid`,
  `TestContextItemValidate_SectionNoneWithoutDirectiveIDPasses`,
  `TestContextItemValidate_EachSectionWithDirectiveIDPasses` lock
  `DirectiveSection`/`Section` validation (SPEC-1.1's supporting field).
- `internal/graph/graph_test.go`: `TestReplaceDirective_MismatchedNewItem`
  (subtests `WrongDirectiveID`, `WrongTask`) and
  `TestReplaceDirective_InaccessibleNewItem` are TEST-1.1's required
  `ErrDirectiveMismatch` coverage; `TestSupersedeSnapshot_TaskMismatch` is
  its `ErrSnapshotTaskMismatch` counterpart.
  `TestSupersede_MissingAndInaccessibleErrorsAreIndistinguishable` locks
  AUTH-1.3's byte-identical error text (both stores).
  `TestReplaceDirective_FirstVersionAuthorization` (subtests
  `ToolActorRejected`, `AgentRejectedForNonKeyedItem`,
  `AgentAllowedForItsOwnKeyedItem`, `UserActorInsufficientAuthorityRejected`)
  locks AUTH-1.5 exactly.
  `TestSupersedeSnapshot_ConversationKindAndIndependentTaskState` is
  SPEC-1.1's exact reproduction: a `kind=conversation` Working item is
  still superseded, and an unrelated `task_state` item sharing task/
  authority/boundary is not.
- `internal/store/storetest`: `TestConformance/DirectiveBoundaries` covers
  the boundary-keyed directive pointer end-to-end (ADR 4's decision) — two
  versions of the same `(task, directiveID)` in different boundaries
  resolve independently, and every boundary field (`AgentID`, `WorkflowID`,
  `Scope`, `SessionID`, `TaskID`) is part of the key. Passes on both
  stores (a brief SQLite-only failure was fixed in `a8e895f`; see ADR 4/17).
- **Genuine remaining gap, assigned to `graph-worker` for round 2:** no
  test directly exercises `LinkDerived` rejecting an AGENT/TOOL/
  RETRIEVED_CONTENT actor whose authority is below the derived item's —
  the code fix for AUTH-1.1 is landed (`internal/graph/graph.go`:
  `actor.Authority.CanHoldLifecycleAuthority() || actor.Authority ==
  AuthorityAgent`, plus `actor.Authority.AtLeast(derived.Authority)`), and
  one existing test comment (`graph_test.go:987`) notes in passing that a
  case deliberately sets `derived.Authority = AuthorityAgent` "to pass the
  actor-authority gate (AUTH-1.1)" while testing something else, but no
  test asserts the gate itself rejects an under-authority or TOOL/
  RETRIEVED_CONTENT actor. Required: a
  `TestLinkDerived_ActorAuthorityBelowDerived`-shaped case reproducing the
  original finding directly (a SYSTEM item, a low-authority actor,
  asserting `ErrInvalidAuthorityPromotion`).

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

**Round 1 review** (PR #2; SPEC — Codex GPT-6, `spec-pr-comment-round1.md`;
AUTH — Claude Opus, `auth-review-round1.md`; TEST — Claude Sonnet,
`test-review-round1.md`). SPEC-1.1 (HIGH): `SupersedeSnapshot` selected
Working candidates by `Kind`, which FR-DIR-003's `kind=conversation` escape
hatch and unrelated `task_state` items both broke; fixed by recording
`Section` and selecting by it (SDD v0.7). AUTH-1.1 (HIGH): `LinkDerived`
never checked actor authority against the derived item, letting a
low-authority actor rewrite a SYSTEM item's provenance; fixed by requiring
`actor.Authority.AtLeast(derived.Authority)` and barring TOOL/
RETRIEVED_CONTENT. AUTH-1.3 (MEDIUM): missing-vs-inaccessible error text
differed across `internal/graph`, leaking existence through tool-visible
error strings, not just `errors.Is`; fixed by normalizing to bare
`ErrNotFound` and checking access before loading the next ID. AUTH-1.4
(MEDIUM): the obligation-transition table encoded no link between a stored
transition and the lifecycle action that was supposed to have authorized
it; fixed by `ObligationTransition.Action`/`TransitionAction` plus actor/
session/matcher-scope checks in `Validate`. AUTH-1.5 (LOW):
`ReplaceDirective`'s first-version path skipped the actor rule entirely;
fixed by applying `AuthorizeSupersession`'s actor rules there too. AUTH-1.6
(LOW): the AGENT keyed-supersession exception ignored task, letting a
cross-task agent key collision through; fixed by requiring equal `TaskID`.
AUTH-1.7 (LOW): `LifecycleEvent.Validate` didn't require the actor's
session match the event's; fixed to match `EventRecord`/`MutationGrant`.
TEST-1.1 (MEDIUM, tracked for `internal/graph`, not this ADR directly):
flagged `ErrDirectiveMismatch`/`ErrSnapshotTaskMismatch` as untested; now
locked by `TestReplaceDirective_MismatchedNewItem`/
`TestSupersedeSnapshot_TaskMismatch`.
Findings 1, 2, 4, N2-N4 from earlier passes were re-verified FIXED and are
unchanged by this round.

Verified against the merged `graph-worker`/`domain-tests-worker` branches
(`go test -race ./internal/domain/... ./internal/graph/...` green): every
finding above has a passing test, cited in the "Round 1 additions"
subsection. `TestConformance/DirectiveBoundaries`'s brief SQLite-only
failure is fixed (`a8e895f`; see ADR 17). One exception remains
intentionally open: AUTH-1.1 (`LinkDerived` actor-authority gate) has the
code fix but no direct test — assigned to `graph-worker` for round 2.
