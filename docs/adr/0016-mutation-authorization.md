# 16. Common mutation authorization, version replacement, residency/goal status, immutable snapshots

Status: Accepted (2026-09-26, Phase 1 exit; decision unchanged by review rounds 1-3 of PR #2)
Date: 2026-09-25

## Amended in Phase 2 (ADR 19, 2026-09-26; corrected 2026-09-26 per SPEC-1.8)

The store methods this ADR's `ResolveLifecycleTarget` and
`rejectVisibleBoundaryConflict` decisions call — `CurrentDirective`/
`CurrentDirectives` — were replaced in Phase 2 by the typed, namespaced
`store.CurrentVersion`/`store.CurrentVersions` (see ADR 4's amendment note;
M6/R6, ADR 19 §17), and both `internal/graph` functions now carry a
`domain.DirectiveNamespace`: `rejectVisibleBoundaryConflict` takes one
explicitly, and `ResolveLifecycleTarget`'s candidate gathering resolves
only the `DIRECTIVE` namespace, per R6, so a keyed agent write can never be
mistaken for a lifecycle target.

**This ADR's authorization semantics are not "unchanged by the rename"
(SPEC-1.8: that claim was wrong); the mechanism this ADR describes below
changed in Phase 2, most visibly here:**

- **`rejectVisibleBoundaryConflict` now returns `graph.ErrBoundaryConflict`
  (R13, ADR 19 §7), never `domain.ErrInvalidAuthorityPromotion`** — a
  deliberate Phase 2 change (ADR 4's amendment note has the full citation),
  so a boundary conflict on a same-ID write is an item-level rejection
  ingestion can diagnose (`boundary_conflict`) without aborting the whole
  event, unlike every other authority-promotion failure this ADR's
  `AuthorizeSupersession`/`AuthorizeMutation` decisions still correctly
  reject with `ErrInvalidAuthorityPromotion`. Only the visible-boundary
  ID-reuse path changed; the rest of this ADR's authority-promotion
  decisions are unaffected.
- **`IsCurrent` (which `AuthorizeSupersession`'s callers and
  `SupersedeSnapshot` both rely on to find "current" candidates) now also
  requires the current-version map to name the item and the item to carry
  no `DUPLICATE_OF` edge** (D10, ADR 19 §7) — not merely the absence of an
  incoming `SUPERSEDES` edge, which is what this ADR's text below
  describes.
- **`Supersede` gained `ErrAlreadySuperseded`** and now retires bound
  obligation versions in the same operation (D13, ADR 19 §9), which this
  ADR's obligation-transition decisions below predate.
- **`SupersedeSnapshot` moved to `internal/graph/snapshot.go`** and returns
  a `SnapshotResult` (`Supersedes`/`Duplicates`), not the return shape this
  ADR's `TestSupersedeSnapshot_*` citations below describe.

Only the store call site's name, its added namespace argument, and the
four points above differ from the authorization/ambiguity/stale-pointer
text below; the rest of this ADR's decisions (grant issuance/revocation,
the AGENT-same-key supersession exception, obligation-transition
authorization, `LinkDerived`'s actor-authority gate) are unaffected by
Phase 2.

## Amended in Phase 3 (ADR 8, 2026-09-26; reconciled against integration head `fc87199`)

Phase 3's binding decision record (`.worktrees/_commander/phase3-decisions.md`,
P3-9, P3-10, P3-11, P3-23, P3-38, P3-39; commander rulings FROZEN 2026-09-26,
reconciled against W3's landed `internal/lifecycle` package) resolves this
ADR's "Resolved at acceptance" open item ("CompleteTask gets its own
authorization function in Phase 3 built on `AuthorizeMutation` for each
affected goal") and extends the grant/promotion/GC machinery this ADR
defines. ADR 8 owns the obligation-matcher/resource-invalidation-specific
consequences of the fourth bullet below; this ADR remains the
authorization-mechanism record.

**New lifecycle actions (P3-42, SPEC-1.14).** `domain.Action` (`internal/domain/authz.go`,
`grant_target.go`) gains six values beyond this ADR's Phase 1 set (`resolve`,
`unpin`, `replace_directive`, `change_scope`, `assert_obligation`,
`block_obligation`, `unblock_obligation`, `waive_obligation`,
`complete_task`): `promote`, `demote`, `archive`, `unarchive`,
`declare_obligation`, and `set_obligation_materialization`. Every one goes
through this ADR's unchanged `AuthorizeMutation`/`AuthorizeGrantIssuance`
entry points; `Action.ValidForTarget(kind)` closes which target kind each
action may name.

**Typed grant targets extend, rather than replace, this ADR's own
`AuthorizeGrantIssuance`/`sameTargetSet` decision (P3-42).** `domain.MutationGrant.Targets
[]GrantTarget` (`internal/domain/authz.go:45`) is the Phase 3 decoded-target
form; the Decision section's `TargetIDs []string` field above is now the
legacy v1 path, kept exactly as this ADR originally described it and
"mutually exclusive with `Targets`" (the field's own doc comment) — a grant
never mixes the two. `sameTargetSet`'s exact-set check runs unchanged over
whichever form is populated. ADR 8 §1 owns the exact-obligation-version
targeting `Targets` adds; this ADR owns the issuance/revocation mechanism
both forms share.

- **`lifecycle.Service.CompleteTask(tx, p, domain.CompleteTaskIntent, seq)`
  (`internal/lifecycle/complete.go`) is `CompleteTask`'s own authorization
  function, built on `AuthorizeMutation` per goal, exactly as this ADR's
  open item promised (P3-9).** It requires SYSTEM/HARNESS/USER and the
  task's immutable workflow ownership (`TestCompleteTaskRejectsForeignWorkflowBeforeOwnerQueries`);
  on first execution the task must be ACTIVE. It enumerates every current
  OPEN goal whose declared owning scope is TURN/TASK and owning task is T —
  including goals the caller cannot access — and authorizes each at its
  actual mutation sequence; any inaccessible or unauthorized member yields
  the fixed `ErrInvalidAuthorityPromotion`. It then separately rejects any
  unresolved/blocked current task-owned obligation (via `obligation.UnfinishedTaskObligations`,
  ADR 8 §8) with the fixed `ErrUnfinishedObligations`
  (`TestCompletionRejectsAllOwnerBlockersBeforeLedger`). After authorization,
  an in-flight operation or unacknowledged exchange blocks completion
  (`TestCompletionX8RejectsEveryReservationAndOpenExchange`,
  `TestCompletionRejectsInFlightWorkOnRealStores`). On success, goals
  resolve, the task CASes to COMPLETED, and a durable GC request is written
  atomically with the completion receipt
  (`internal/lifecycle/completion_effect.go`'s `writeCompletion`); an
  identical completion replay returns the original success
  (`TestCompleteTaskResolvesOwnedGoalsAndReplaysFrozenReceipt`,
  `TestConcurrentCompletionExecutesOnce`), and failure leaves no partial
  effect (`TestCompleteTaskFailsClosedWithoutPartialEffects`,
  `TestCompletionGCFailureRollsBackGoalsAndTask`). As accepted consequences,
  not defects: the task-wide result necessarily reveals aggregate completion
  feasibility even through a generic error, and a USER may complete a
  no-goal task once its current task-owned obligations permit
  (`TestCompleteTaskWithoutGoalsStillRecordsReceiptAndGC`), ending TASK pin
  protection including a SYSTEM pin.
- **Promotion/demotion are typed intents over a closed transition policy, not
  arbitrary `ItemChange` (P3-10).** `lifecycle.Service.Promote`/`Demote`
  (`internal/lifecycle/generation.go`) take `domain.PromoteIntent`/
  `DemoteIntent` (a type alias, `internal/domain/lifecycle_intent.go`). The
  exact allowed-pair table below is a **new V1 policy proposal this ADR
  records, not one the SDD prescribed independently** (P3-10 says exactly
  this: "The exact V1 pair table is a new policy proposal"): Promote allows
  EPHEMERAL→WORKING, WORKING→DURABLE, and current constraint/instruction
  DURABLE→PINNED; Demote allows DURABLE→WORKING and WORKING→EPHEMERAL for
  nonrequirement semantic items. PINNED→DURABLE
  remains exclusively Unpin (this ADR's existing rule, unchanged); goals,
  transcript/checkpoint/projection records, and current obligation sources
  are excluded, governed instead by this ADR's Resolve/Unpin and ADR 8's
  obligation-transition rules (`TestGenerationExcludesObligationSourceAndNeedsAuthority`).
  No promotion/demotion operation changes authority, kind, access, scope,
  refreshes TTL, restores currentness, or waives an obligation
  (`TestGenerationPairsFollowClosedPolicy`). Raw `ItemChange` remains an
  internal storage mechanism, never a public mutation surface.
- **Unpin now reaches any current PINNED semantic item, not only a keyed
  DIRECTIVE (PR #6 round 1, SPEC-1.8).** Promote's DURABLE→PINNED path
  (above) can pin an *unkeyed* semantic item — exactly the SYSTEM/HARNESS
  residual instructions/constraints production actually creates — but
  Unpin originally resolved only a keyed item in the DIRECTIVE namespace,
  so a Promote-pinned instruction could never be unpinned (Demote also
  forbids PINNED→DURABLE, since that transition is exclusively Unpin's).
  `lifecycle.directive`'s target-resolution switch
  (`internal/lifecycle/directive.go:46-55`) now accepts an unkeyed,
  `RoleSemantic` item specifically for `ActionUnpin` (never for Resolve,
  which still targets DIRECTIVE goals only) — the keyed/DIRECTIVE
  restriction stays exactly as strict for every other case, including
  AGENT_KEY and OBSERVATION items and every stale/superseded/lower-authority
  target. `TestPromotedUnkeyedPinCanBeUnpinned`.
- **Grant issuance/revocation are authenticated intents with runtime-derived
  attribution, not caller-certified fields (P3-11).** `lifecycle.Service.IssueGrant`/
  `RevokeGrant` (`internal/lifecycle/grants.go`) take `domain.GrantIntent`/
  `RevokeGrantIntent`; the runtime, not the caller, derives `Issuer`/`Actor`,
  session, issued/revoked sequence, and audit identity from authenticated
  source context, running through this ADR's existing
  `AuthorizeGrantIssuance`/`AuthorizeGrantRevocation` checks unchanged.
  `lifecycle.grantTargets` resolves a target named in a grant intent only if
  it already exists or was created earlier in the same ordered transaction;
  a forward alias to a not-yet-existing target fails. Tests:
  `TestIssueGrantDerivesIssuerAndAuthorizesGrantee`,
  `TestIssueGrantFailsClosed`,
  `TestRevokeGrantNeedsDirectAuthorityAndEndsAuthorization`.
- **A restricted, cause-based invalidation path exists alongside this ADR's
  authorization matrix, and is deliberately not routed through it (P3-23,
  ADR 8 §13).** Runtime invalidation of an already-accepted resource-bound
  obligation proof, triggered by an authenticated resource report, is a
  narrowly scoped consequence of that accepted proof — never a fresh
  exercise of a grant that has since expired or been revoked, and never a
  privilege this ADR's `findGrant`/`AuthorizeMutation` path would otherwise
  grant a session-level resource reporter. It can only move the one
  matching current SATISFIED obligation version to UNRESOLVED; it is
  recorded with a historical `domain.OriginAuthorizationRef`
  (`internal/domain/assertion.go`) distinct from a live `GrantID`. This is
  the one place in the runtime's mutation surface where a write to
  obligation status legitimately occurs outside this ADR's
  `AuthorizeMutation` entry point; ADR 8 §13 owns its schema and tests in
  full.
- **GC uses a pure decision function and an explicitly enabled trigger set,
  built on `CompleteTask`'s durable GC request (P3-38, P3-39, C-17).**
  `policy.CollectDecision(item, policy.GCSnapshot)` (`internal/policy/gc.go`)
  is the pure `gc/v1` rule: it protects an open exchange, a live lease, an
  active-turn item, and — **C-17's exact ruling** — only a `CHECKPOINT`
  item that is its conversation's *newest* checkpoint in an active or
  unknown conversation (`GCReasonActiveCheckpoint`; older and
  completed-conversation checkpoints are not protected forever merely by
  kind or role). `graph.CheckpointOfItem`/`CheckpointsCoveringItem`
  (`internal/graph/membership_checkpoint_read.go`, W5) supply that "newest"
  fact to `lifecycle.Service.Collect` (`internal/lifecycle/collect.go`).
  `domain.Phase3Policy.GCTriggers`/`GCTriggerEnabled`
  (`internal/domain/semantic.go`) is the explicit, sorted, unique enabled
  trigger set `lifecycle.enqueueGC` checks before creating a `GCRequest` for
  a supersession/TTL/policy trigger — an unlisted one persists nothing and
  returns an empty ID, never firing GC merely because the enum value
  exists. `domain.GCTaskCompletion` is the one exempt trigger: task
  completion's request always persists regardless of the enabled set
  (P3-39's "task completion always persists its request"), then waits
  pending until an operator or a later policy enables its actual
  collection.
  **Default enabled set and its producers (PR #6 round 1/2, SPEC-2.13).**
  `policy.DefaultPhase3Policy()` (`internal/policy/phase3.go`) enables
  `{MANUAL, SUPERSESSION, TASK_COMPLETION, TTL}` — **not**
  `domain.DefaultGCTriggers()`'s all-five set, which also includes `POLICY`.
  The stated rule is that only a trigger with a producer on every path that
  can raise it is enabled by default (`FR-GC-004` permits a disabled
  trigger); `POLICY` has no producer yet, so it stays off. **SPEC-2.3
  (PR #6 round 2): this rule was violated for SUPERSESSION on two paths;
  one is now fixed in this reconciliation, one is fixed but not yet merged
  here.** Observation-state supersession
  (`graph.FileObservationState`, via `internal/obligation/subject_state.go`'s
  `deriveState`) now enqueues through `gcqueue.Enqueue`, keyed by the new
  state occurrence, under the service's own recorded policy — a first state
  (nothing to supersede) enqueues nothing, and a task-less trigger persists
  nothing per H4 (`internal/gcqueue`, W4b). Tests:
  `TestObservationStateSupersessionEnqueuesGC_SPEC23`. **Both SPEC-2.3
  paths are now landed.** Agent keyed writes (`internal/tools/keyed.go`,
  via `graph.ReplaceDirective`) and task-less directive replacement
  (`internal/lifecycle/replace.go`) also enqueue through `gcqueue.Enqueue`
  now, closing the round-1/round-2 gap where a keyed replacement or a
  session-scoped directive replacement produced no `GCRequest`, or failed
  outright, despite SUPERSESSION being enabled by default. An enabled
  trigger whose executor
  cannot run it (a stale `PolicyVersion` on the request, or a missing
  authorized collector) fails that attempt closed rather than silently
  succeeding; it stays pending for a later, correctly-configured attempt.
  **GC collection resumes in bounded batches (round 3 rulings J1–J7;
  XREV-3.1–3.3, SEC-3.1/3.2/3.9, SPEC-3.2/3.3/3.6).** The first
  batch pins the eligibility ceiling `SnapshotSeq`; all batches traverse
  that ceiling in `(item.Seq, item.ID)` order. `GCProgress` CAS persists
  the last fully decided candidate, completed batch count, adaptive
  item-count limit, and attempts for the next candidate. Later insertions
  cannot extend the request. Continuations require the first batch's
  collector principal, preserving its access boundary.
  Shared transaction-budget exhaustion commits only the completed prefix
  and halves the item-count limit (floor one). Receipt sizing includes
  the complete enclosing mutation receipt, archive results, request link,
  arguments and metadata before applying effects; a smaller prefix is
  chosen until it fits. A single-item overflow is an explicit
  `SKIP_RESOURCE_LIMIT` decision. Permanent item errors record
  `SKIP_INVALID_ITEM` or `SKIP_INTEGRITY`; transient item reads retry at
  most three times and then record `SKIP_ATTEMPTS_EXHAUSTED`. These skips
  preserve the item and let later candidates proceed. A result becomes
  `COLLECTED` only when the bounded candidate traversal is exhausted.
  Only request-level `INVALID_REQUEST` and `INTEGRITY` failures quarantine.
  Collector policy/trigger mismatch or missing capability returns an error
  and leaves the request pending without charging attempts. Infrastructure
  failures also leave it pending; historical terminal reason codes remain
  readable. Terminal results remove requests from the pending index in the
  same transaction. Each service keeps a concurrency-safe, per-session
  scan continuation across calls and wraps at the end, so a disabled or
  declined prefix cannot permanently hide runnable requests within that
  service's lifetime; a new service begins at the queue head.
  Direct `Collect`, including SESSION scope, uses the same durable path.
  Its receipt exposes `GCRequestID`; callers use `ExecuteGCRequest` or
  `CollectPending` to continue. Retrying the original manual intent replays
  its first receipt without rescanning. Later batch identities derive from
  the authenticated origin in the reserved runtime namespace. H4 still
  disables automatic task-less producers; manual session collection works.
  SQLite migrations 0041–0044 add `SnapshotSeq`, `BatchSize`,
  `ItemAttempts`, and `ItemAttemptID` to GC progress. Existing progress without a snapshot
  recovers its ceiling from its first committed collect receipt. No
  candidate-history or pending-index schema change is needed.
  Regression tests: `TestJ1BudgetBoundaryCollectsEveryCandidate`,
  `TestJ2SnapshotAndCursorStayFrozen`,
  `TestJ3CompleteReceiptFitsAndBatchAdapts`,
  `TestJ4DeadLeaseHistoryDoesNotFailRequest`,
  `TestJ4LongCancelledMembershipHistorySkipsOnlyOneItem`,
  `TestJ5ConfigurationErrorsLeaveRequestsPending`,
  `TestJ6QueuePrefixCannotHideRunnableTail`, and
  `TestJ7ManualSessionCollectionResumesAndReplays` run on both stores.
  **Grant issuance shares its live-count cap fairly and reserves room for
  SYSTEM (SEC-2.7).** `liveGrantRoom` (`internal/lifecycle/grants.go`)
  limits any one issuer to at most a quarter of the policy's live-grant
  cap per `(action, target)`, with the last quarter reserved for SYSTEM —
  so no lower-authority issuer, alone or in combination, can exhaust the
  cap and deny issuance to everyone else. Test:
  `TestLiveGrantCapIsSharedFairly`.
  `lifecycle.CollectPending`/`ExecuteGCRequest` execute a durable request
  idempotently after producer commit, never inline with it. Tests:
  `TestCollectDecisionMatrix`, `TestCollectDecisionRejectsIncompleteSnapshot`,
  `TestCollectDecisionSafetyProperties` (`internal/policy`);
  `TestGCProtectsOnlyTheNewestRelevantCheckpoint`,
  `TestGCCheckpointWithoutCompanionAbortsOnRealGraph`,
  `TestGCTriggerSetIsEnforced`,
  `TestEnqueueGCDeduplicatesTriggerIdentity`,
  `TestCompletionGCRequestExecutesOnceAfterProducerCommit`,
  `TestCollectPendingExecutesDurableRequestsOnRealStore`,
  `TestCollectArchivesOnlyAuthorizedUnprotectedCandidates`,
  `TestCollectFailsClosedAtomically`,
  `TestConcurrentIdenticalCollectFreezesOneReceipt` (`internal/lifecycle`).

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
- **A `Matcher`-attributed transition must also record its `GrantID`
  (round 2, AUTH-2.3).** FR-AUTH-002 requires a matcher to run "under that
  recorded grant"; round 1's fix bound the transition's `Action` to
  `ActionAssertObligation` for a matcher, but nothing yet required the
  transition to name *which* grant authorized it. `ObligationTransition
  .Validate` now additionally requires `GrantID != ""` whenever `Matcher !=
  nil` — a matcher transition with no recorded grant is now structurally
  impossible to store, closing the gap between "the action is right" and
  "there was actually a grant behind it."
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
- **`internal/graph.rejectVisibleBoundaryConflict`, called from
  `ReplaceDirective`'s first-version path, rejects a write that would
  smuggle a boundary change through visible ID reuse (round 2, AUTH-2.1;
  round 3, AUTH-3.2).** Boundary-keyed identity (ADR 4) means an actor can
  legally create a new current version of a directive ID in a boundary
  where none currently exists — but if that same actor can *also* access a
  current version of the identical ID in a *different* boundary, writing
  the new one is not "a fresh directive," it is the boundary change
  FR-DIR-002 requires go through "an explicit authorized replacement
  policy," attempted through ID reuse instead. `rejectVisibleBoundaryConflict`
  calls `store.ReadTx.CurrentDirectives(taskID, directiveID)` (ADR 4), and
  for each returned item ID the actor can access, fails
  `domain.ErrInvalidAuthorityPromotion` — while versions the actor cannot
  see are skipped entirely, never entering the decision or the error,
  preserving round 1's non-disclosure property. **AUTH-3.2:** a version
  `CurrentDirectives` still names but that has since been superseded
  (through some path other than the current-directive map, e.g. a Working
  snapshot) is a stale pointer, not a live second version, and must never
  block a legitimate write — the check now also calls `IsCurrent` on each
  accessible candidate and only fails on one that is genuinely still
  current.
- **`ResolveLifecycleTarget(tx, actor, taskID, id) (itemID string, err
  error)` resolves a Resolve/Unpin target unambiguously (round 2, SPEC-2.2;
  round 3, SPEC-3.1/AUTH-3.2).** `id` is tried both ways — as a literal item
  ID and as a directive ID — and **both namespaces are gathered into one
  candidate set before any decision is made** (SPEC-3.1): every candidate
  from either namespace that is accessible to `actor` *and* currently
  current (via `IsCurrent`, closing the same stale-pointer gap AUTH-3.2
  fixes in `rejectVisibleBoundaryConflict`) is collected; an inaccessible or
  noncurrent candidate in either namespace is dropped silently, exactly
  like a missing one. Checking the literal item first and returning
  immediately on any failure there — the round-2 shape — would let a hidden
  item that merely happens to share an ID with an accessible directive
  change the result: that ordering is an existence oracle (whether the
  literal-ID branch failed "not found" vs. "inaccessible" leaks through to
  whether the directive branch even runs) and could block an otherwise-
  authorized Resolve/Unpin. Zero candidates is `ErrNotFound`; exactly one is
  the answer; more than one — whether two directive versions, or an item ID
  and a directive ID that happen to collide — is `ErrAmbiguousDirective`
  (SDD v0.8, §8), mutating nothing in any case. This is the FR-DIR-005
  amendment's exact target-resolution rule, given its own function so
  Resolve/Unpin (and any future lifecycle command over the same identity
  space) share one implementation rather than each reimplementing the
  accessible-current-candidate filter.
- **`SupersedeSnapshot` rejects any new item whose `Section != SectionWorking`
  (`ErrSnapshotNotWorking`) before writing anything (round 2, SPEC-2.1).**
  Round 1 fixed *which old items* the selector could retire (by `Section`,
  not `Kind` — see the FR-DIR-007 bullet above), but left the *new* item's
  own `Section` unchecked: a new item with `SectionNone` could still be
  passed to `SupersedeSnapshot` and retire every current Working item it
  matched, even though it was never itself ingested as part of a Working
  section. FR-DIR-007 authorizes this operation specifically for "ingesting
  a Working section" — the precondition the `Section` field now lets the
  helper actually enforce. The check runs during the same pass that loads
  and validates each new item (before any candidate scan or edge write), so
  a non-Working new item leaves every existing Working snapshot untouched
  and the call fails outright rather than partially retiring state.
- **`LinkDerived` requires `tx.Allocated(derived.Seq)` (`ErrDerivedLinkNotAtCreation`
  otherwise, round 3, AUTH-3.1 — replaces round 2's AUTH-2.4).** AUTH-1.1
  (round 1) restricted *who* may call `LinkDerived`; AUTH-2.4/3.1 restrict
  *when*, closing a residual version of the same laundering risk: without
  it, an authorized actor could attach `DERIVED_FROM` provenance to an item
  at any later point, from any transaction, not only the one that created
  it — letting provenance be backfilled or reassigned well after the fact.
  Round 2's mechanism for "when" — comparing the derived item's own
  `EventID` field to the caller-supplied `eventID` argument — turned out to
  prove nothing: `EventID` is a plain string stored on the item, readable by
  *anyone* who can access it, so a later, unrelated transaction could simply
  read it off the record and replay it verbatim as its own `eventID`
  argument, passing the check while being exactly the retroactive-provenance
  case AUTH-2.4 was meant to close. **AUTH-3.1's fix:** a new
  `store.Tx.Allocated(seq uint64) bool` method reports whether `seq` was
  allocated by `NextSeq` *in this transaction* — a fact no caller can forge,
  since sequence numbers are the store's own per-transaction bookkeeping,
  not data on the record. `LinkDerived` now requires
  `tx.Allocated(derived.Seq)`: provenance can only be attached in the very
  transaction that inserted the derived item, never from a later one, even
  one that supplies the item's own `EventID` correctly.

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

## SDD amendment (applied in v0.8)

FR-DIR-002 (visible-boundary ID reuse) and FR-DIR-005 (`ErrAmbiguousDirective`
for a Resolve/Unpin target with several accessible current versions) are
amended per round-2 findings AUTH-2.1/SPEC-2.2 (commit `eaf3b98`); full text
and rationale are recorded in ADR 4, which owns directive identity. This
ADR implements the authorization/resolution consequences —
`ReplaceDirective`'s reuse rejection and `ResolveLifecycleTarget` — above.

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
- **Requiring an explicit boundary qualifier in Resolve/Unpin syntax
  instead of `ResolveLifecycleTarget`'s ambiguity diagnostic (round 2).**
  Rejected: see ADR 4's SDD v0.8 amendment section — changing the directive
  grammar for every caller to guard a rare multi-boundary collision is more
  disruptive than detecting and refusing the rare ambiguous case.
- **Silently picking the SYSTEM/HARNESS-authored version when
  `ResolveLifecycleTarget` finds several accessible current versions,
  instead of failing ambiguous.** Rejected: authority ordering answers "who
  outranks whom for a mutation," not "which of several *equally legitimate,
  independently-authored* current versions the caller meant." A USER goal
  in TASK scope and the same USER's goal in TURN scope are both fully
  valid, current, USER-authority versions with no ranking between them;
  silently preferring one is a policy decision this ADR has no basis to
  make on the caller's behalf, and would resolve or unpin state the caller
  never named.
- **Comparing `derived.EventID` to a caller-supplied `eventID` string
  (round 2's AUTH-2.4 mechanism).** Rejected in round 3 (AUTH-3.1): `EventID`
  is ordinary data on the item, not a capability — anyone who can read the
  item can read its `EventID` and hand it back to `LinkDerived` later,
  which defeats the entire point of restricting *when* provenance can be
  attached. Only the store's own transaction-scoped sequence bookkeeping
  (`tx.Allocated`) cannot be forged by a caller, because it isn't derived
  from anything the caller can read off a record.
- **`ResolveLifecycleTarget` trying the literal-item branch first and
  returning immediately on failure, falling through to the directive
  branch only on `ErrNotFound` (round 2's shape).** Rejected in round 3
  (SPEC-3.1): whether the literal-ID branch fails `ErrNotFound` or succeeds
  becomes observable through whether the directive branch's result can
  still change the outcome, which is an existence oracle for the literal
  ID. Gathering both namespaces into one unordered candidate set before
  applying the zero/one/many decision removes the ordering dependency
  entirely.

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
- `ResolveLifecycleTarget` is now the single required entry point for
  translating a Resolve/Unpin `id` into an item ID; any future Phase 2
  directive-command handler that resolves a target by querying
  `CurrentDirective`/`CurrentDirectives` directly instead of calling it
  would silently reintroduce SPEC-2.2's ambiguity gap.
- The `Section != SectionWorking` precondition on `SupersedeSnapshot` and
  the `tx.Allocated(derived.Seq)` check on `LinkDerived` are both breaking
  changes to their existing call sites and test fixtures: any caller/test
  constructing a new item for `SupersedeSnapshot` without `Section =
  SectionWorking`, or calling `LinkDerived` in a transaction other than the
  one that inserted the derived item, must be updated.
- `store.Tx.Allocated` (ADR 17 territory for its store-contract wording;
  cited here because `LinkDerived` is its first consumer) is a new
  required method on every `store.Tx` implementation — any future store
  backend must track which sequence numbers `NextSeq` allocated within the
  current transaction (and forget them on rollback/commit) to implement it
  correctly, not just return a constant or approximate answer.
- AUTH-2.2 (`Section` requires `CanHoldLifecycleAuthority()`, ADR 4) is a
  precondition every `internal/graph` test fixture that constructs a
  Working/directive item under an AGENT/TOOL/RETRIEVED_CONTENT authority
  must also satisfy merely to call `InsertItem`. This briefly broke
  `TestReplaceDirective_FirstVersionAuthorization`'s `ToolActorRejected`/
  `AgentRejectedForNonKeyedItem` subtests (their fixtures set `Section` on
  a TOOL/AGENT item to reach the authorization check under test) between
  AUTH-2.2 landing in `internal/domain` and `graph-worker`'s round-2 fixes
  landing in the same merge; both are now merged together and all four
  subtests pass.

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
- `TestLinkDerived_ActorAuthorityRequired` (subtests `ToolActor`,
  `RetrievedContentActor`, `UnderAuthorityAgentActor`) is AUTH-1.1's
  direct regression test, closing the gap TEST-2.2 identified (round 1's
  fix had no test asserting the gate itself rejects an under-authority or
  TOOL/RETRIEVED_CONTENT actor). Landed with `graph-worker`'s round-2 work.

### Round 2 additions (findings AUTH-2.1, 2.3; SPEC-2.1, 2.2; AUTH-2.4; TEST-2.2) — landed

`internal/domain`, `internal/store`, and `internal/graph`'s round-2 fixes
and tests are all merged and passing (`go test -race ./... ` green):

- `internal/domain/obligation_test.go`: `TestObligationTransitionValidate`'s
  cases "matcher satisfaction with evidence and a grant ID ok," "matcher
  transition without a grant ID rejected, even with evidence," "direct
  assertion (no matcher) needs no grant ID," and "direct assertion may
  still carry a grant ID" lock AUTH-2.3 exactly.
- `internal/domain/item_test.go`:
  `TestContextItemValidate_SectionRequiresLifecycleAuthority` and
  `TestContextItemValidate_NonDirectiveItemAllowsAnyAuthority` lock
  AUTH-2.2 (ADR 4 owns the decision; cited here too since ADR 16's
  `SupersedeSnapshot` selector depends on `Section` being trustworthy).
- `internal/store/memory`/`internal/store/sqlite` implement
  `CurrentVersions` (SPEC-1.8: registered as
  `TestConformance/CurrentVersions(DIRECTIVE)Order`, renamed from
  `CurrentDirectivesOrder`; memory and SQLite) and
  `internal/store/sqlite/current_directives_test.go
  :TestCurrentDirectivesAcrossBoundaries` lock it (ADR 4 owns the
  decision). `TestConformance/MatcherTransition` reconfirms AUTH-2.3 at the
  store level: `AppendObligationTransition` rejects a matcher transition
  with no `GrantID` (`ErrInvalidRecord`) and accepts it once one is set.
  `TestConformance/LedgerSeqIsolation` is ADR 17's DUR-2.1 regression.
- `internal/graph/graph_test.go`: `TestReplaceDirective_VisibleBoundaryConflict`
  (subtests `VisibleOtherBoundaryRejected`, `HiddenOtherBoundaryStaysIndependent`)
  locks AUTH-2.1 exactly — a boundary the actor can see blocks reuse, a
  boundary it cannot see stays independent and undisclosed.
  `TestResolveLifecycleTarget_LiteralItemID`,
  `TestResolveLifecycleTarget_LiteralItemNotCurrentOrInaccessible`
  (subtests `Superseded`, `Inaccessible`), and
  `TestResolveLifecycleTarget_DirectiveID` (subtests
  `SingleAccessibleVersion`, `NoAccessibleVersion`,
  `AmbiguousAcrossBoundaries`) lock SPEC-2.2's `ResolveLifecycleTarget`
  exactly, including the ambiguous-target `ErrAmbiguousDirective` case.
  `TestSupersedeSnapshot_NewItemNotWorking` locks SPEC-2.1: a non-Working
  new item is rejected before any existing Working snapshot is touched.

### Round 3 additions (findings SPEC-3.1, AUTH-3.1, AUTH-3.2) — landed

- `internal/store/storetest/transactions.go:testAllocated`
  (`TestConformance/Allocated`, run on both stores) locks `Tx.Allocated`
  exactly: false before any `NextSeq` call, true for every number
  allocated by `NextSeq` in the current transaction, false again once that
  transaction ends (a rolled-back transaction's numbers are reused by the
  next one and only then report `true`), and independent per session.
- `internal/graph/graph_test.go:TestLinkDerived_MustBeCreationEvent`
  (subtests `SameTransactionAllowed`, `LaterTransactionRejectedEvenWithMatchingEventID`)
  locks AUTH-3.1 exactly — the second subtest is the regression itself: a
  later transaction reads the derived item's own `EventID` back off the
  stored record and replays it verbatim to `LinkDerived`, which the old
  string comparison could not tell apart from "the transaction that
  created it," and which `tx.Allocated` correctly rejects
  (`ErrDerivedLinkNotAtCreation`).
- `TestResolveLifecycleTarget_HiddenItemNeverBlocksDirective` (subtests
  `AccessibleItemAndAccessibleDirectiveCollideAmbiguous`,
  `NoncurrentLiteralItemTreatedAsAbsent`) locks SPEC-3.1's unified-
  candidate-gathering fix directly: an inaccessible/noncurrent literal-ID
  candidate never short-circuits or blocks the directive-ID branch, and a
  literal item plus a directive version that both resolve is
  `ErrAmbiguousDirective`, not a silent pick of one.
  `TestResolveLifecycleTarget_StaleDirectivePointerNotReturned` and
  `TestReplaceDirective_StalePointerDoesNotBlockNewBoundary` lock AUTH-3.2
  in `ResolveLifecycleTarget` and `rejectVisibleBoundaryConflict`
  respectively: a `CurrentDirectives` entry that has since been superseded
  through another path is treated as absent, never as a live conflict or a
  valid resolution target.

## Open questions

### Resolved at acceptance (2026-09-26)

- Whether `granteeMatches`'s exact-ID matching is expressive enough once
  Phase 3 needs grants scoped to "any task the grantee currently owns."
  **Decision:** exact-ID grantee matching stays for V1; ADR 8 (Phase 3)
  revisits if task-scoped grants are required, as an additive grant form.
- Whether CompleteTask's same-task-ownership filter (FR-AUTH-003) needs its
  own function distinct from `AuthorizeMutation`.
  **Decision:** CompleteTask gets its own authorization function in Phase 3
  built on `AuthorizeMutation` for each affected goal; recorded in this ADR
  by amendment then.

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
failure is fixed (`a8e895f`; see ADR 17). AUTH-1.1's missing direct test
is closed by `graph-worker`'s round-2 work (`TestLinkDerived_ActorAuthorityRequired`,
below).

**Round 2 review** (PR #2; AUTH — Claude Opus, `auth-review-round2.md`;
SPEC — Codex GPT-6, `spec-pr-comment-round2.md`). AUTH-2.1: boundary-keyed
directive identity (ADR 4, v0.7) opened a new gap — an actor visible in
two boundaries could smuggle a boundary change through ID reuse; fixed by
`rejectVisibleBoundaryConflict`. AUTH-2.3: a matcher transition could omit
which grant authorized it; fixed by requiring `GrantID` whenever `Matcher
!= nil`. AUTH-2.4: `LinkDerived`'s round-1 actor-authority gate didn't
constrain *when* provenance could be attached; fixed by requiring the
derived item's own `EventID` (`ErrDerivedLinkNotAtCreation`). SPEC-2.1:
`SupersedeSnapshot`'s round-1 fix checked only old items' `Section`, not
the new item's; fixed by rejecting a non-Working new item outright
(`ErrSnapshotNotWorking`). SPEC-2.2: boundary-keyed identity let one
principal see two simultaneously-current same-ID directives with no
defined Resolve/Unpin target; fixed by `ResolveLifecycleTarget` plus the
SDD v0.8 amendment (`ErrAmbiguousDirective`). All findings above, including
`internal/graph`'s (AUTH-2.1, 2.4; SPEC-2.1, 2.2) and TEST-2.2, are now
merged and tested; verified against `graph-worker`'s round-2 commits
(`85a5317`/`043626b`) and the domain/store round-2 commits
(`485472b` and others) with a full `go test -race ./...` pass.

**Round 3 review** (PR #2; all four reviewers — DUR, SPEC, AUTH, TEST —
returned NO FURTHER WORK NEEDED after this round). AUTH-3.1: round 2's
AUTH-2.4 fix (comparing `derived.EventID` to a caller-supplied `eventID`)
proved not to be a real constraint — `EventID` is readable data on the
item, so a later transaction could just read it back and replay it;
replaced with `store.Tx.Allocated(derived.Seq)`, a fact about the current
transaction no caller can forge, since it isn't derived from anything on
the record. SPEC-3.1: `ResolveLifecycleTarget`'s round-2 shape (try the
literal item, fall through to the directive lookup only on `ErrNotFound`)
was itself an existence oracle — fixed by gathering both namespaces into
one candidate set before deciding. AUTH-3.2: both `ResolveLifecycleTarget`
and `rejectVisibleBoundaryConflict` could be blocked or misled by a
`CurrentDirectives` entry that had gone stale through a path other than
the directive map (e.g. Working-snapshot supersession); both now confirm
`IsCurrent` before treating a candidate as live. Verified against the
merged round-3 commits (store `894d81a`/memory `05c3de9`/sqlite `7b5eda7`
for `Tx.Allocated`; graph `f48c919`/`6cd2371`/`621700a`/`755ca45`) with a
full `go test -race ./...` pass and every cited test run individually.
