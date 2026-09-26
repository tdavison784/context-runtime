# 6. Access boundary and context eligibility matrix

Status: Accepted (2026-09-26, Phase 1 exit; decision unchanged by review rounds 1-3 of PR #2)
Date: 2026-09-25

## Amended in Phase 3 (ADR 8, 2026-09-26; proposed pending Phase 3 gate)

Phase 3's binding decision record (`.worktrees/_commander/phase3-decisions.md`,
P3-6, P3-7, P3-29, P3-31, P3-32, P3-33; commander rulings FROZEN 2026-09-26)
implements this ADR's Phase 1 "decided now, built in Phases 3/5" temporal-
eligibility rule and its two open items (the lease type, and what "the
workflow's/agent's active-owner state" means). This section records the
resulting decisions; ADR 8 (this repository's Phase 3 ADR) is the record of
authorization/matcher-specific consequences and owns none of what follows.

- **The retrieval lease is implemented in Phase 3, not deferred to Phase 6
  (P3-29, P3-33; corrects this ADR's "Resolved at acceptance" note below).**
  `RetrievalLease` binds an exact principal (including authority),
  session/workflow/task/agent, conversation, the current requesting turn,
  source occurrence/content identity, issue sequence, the issued completed-
  inference index at issue time, and a bounded positive call allowance. Live
  means exact holder and current active turn, and
  `completedInferenceIndex - issuedIndex < allowance` under checked
  nonnegative arithmetic; only completed inferences in that conversation
  consume allowance — compaction, failure, retry, and other conversations do
  not. Active-request coalescing may reuse a live lease but never extends
  it; an idempotent retry returns the original result even after its lease
  has expired; a genuinely new explicit request may issue a new lease over
  the same immutable content. A lease never renews an old projection that
  names an already-expired lease. Observed source lifecycle
  revision/status is frozen on the retrieval result for audit, never used as
  a lease-invalidation trigger by itself. This meets the shape this ADR's
  eligibility table already assumed (principal/task/agent/turn-bound, with
  an expiry); Phase 3 policy configures a finite positive default/max
  allowance — zero or unlimited is invalid — and the effective values are
  recorded.
- **One pure `Eligibility` function replaces this ADR's table as the
  authoritative decision surface (P3-31).**
  `Eligibility(item, snapshot, principal, dispatchTurn)` consumes explicit
  validated currentness, owning task/turn state, owner registration, resource
  applicability where required, active leases, the conversation's completed-
  inference index, and pending-input/representation context at one sequence
  snapshot, and returns `Access`, `OrdinaryTemporal`, `LeaseAdmission`,
  `NewSelection`, and closed reason codes as independent outputs — `Access`
  is computed first, exactly as `AccessBoundary.Permits` already is in this
  ADR. A lease may admit historical evidence without restoring independent
  current-requirement eligibility or protection (ADR 16/FR-GC-003 territory).
  An archived item can never enter ordinary new selection through a lease; a
  historical or superseded goal/directive, or a retired obligation source,
  never regains requirement status through one either. Missing required
  snapshot state fails closed. No store access, wall clock, tokenizer, or
  provider counter runs inside this function — it is exactly this ADR's
  existing access/eligibility split (rules 1-3, unchanged), given one
  callable, testable shape instead of the table format, with independent
  outputs where the table's single row previously conflated them.
- **WORKFLOW/AGENT "active-owner state" is session-lifetime registration,
  not child-task liveness (P3-32).** A trusted registered workflow/agent
  owner is ACTIVE for the session's lifetime once registered/first
  associated; ending its last child task does not end it, and Phase 3 exposes
  no general termination API. This resolves this ADR's eligibility-table row
  ("WORKFLOW: the workflow's active-owner state"; "AGENT: the agent's
  active-owner state") concretely: that state is an explicit, reconstructible
  registration/association record (owner kind/id/session/source/sequence),
  never inferred from whether any child conversation/task happens to be
  active right now. Unknown legacy association fails closed for automatic
  selection while retaining archive access. A future explicit termination
  needs its own authorized, audited scope-end semantics and migration; V1
  does not add one.
- **`Coverage` gains purpose tagging and linear storage; conversation
  membership becomes an explicit fact (P3-6, P3-7).** This ADR's rule 1
  already requires `Coverage.ItemIDs` to be complete for the temporal-
  eligibility recheck; Phase 3 adds distinct coverage *purposes*
  (provenance, qualifying evidence support, raw/opaque representation
  dependency, lease dependency, replaceable exchange coverage) stored once
  per immutable `CoverageRecord` with indexed `CoverageMember` rows, so N
  sources produce O(N) members rather than a union copied into every
  dependent edge — the mechanism changes, the "complete `ItemIDs`, never a
  range alone" requirement does not. Separately, conversation membership
  (which items an agent actually received, closing an exchange) is a durable
  authenticated fact recorded through `LogicalExchange`/`ExchangeMember`/
  `AdmissionManifest` records, never inferred from task visibility or item
  kind — visibility permits reading; it does not by itself prove an agent
  received an item. This is a precondition for FR-TOOL-004's checkpoint
  coverage (ADR 8 territory does not include checkpoints; that lands with
  the semantic-tools worker), not a change to this ADR's access/eligibility
  checks themselves.

### SDD amendment (applied in v0.10)

- **FR-DOM-003 / ADR 6.** Add: "V1 registered WORKFLOW and AGENT owners
  remain active for the session lifetime; the absence of active child tasks
  does not end their scope. Unknown required owner state forbids automatic
  admission."
- **FR-TOOL-004 / ADR 6.** Add: "Checkpoint source provenance and the
  complete closed exchange prefix it may replace are recorded separately.
  Membership is explicit; current requirements and pending/open exchanges
  cannot be removed by coverage. Authored semantic knowledge may outlive
  source expiry; raw/opaque/retrieval representations retain their source
  and exact lease dependencies."
- **FR-RET-006; T05 wording.** Add: "Retrieval leaves persisted Residency
  unchanged; a holder-bound lease supplies temporary historical-evidence
  admission. The lease binds immutable source occurrence/content, while the
  retrieval result records observed lifecycle revision/status." T05's
  optional residency flip becomes an explicit unchanged-residency
  expectation (`docs/sdd-event-traces.md`'s own amendment, below).

Applied to SDD.md as v0.10 (this ADR does not itself edit SDD.md).

## Context

FR-DOM-003 requires access to always require the same session, with TURN/TASK
also requiring the same task, WORKFLOW the same workflow, and AGENT the same
agent, plus a separate, stricter context-eligibility check for automatic
planning. FR-REL-008 requires derived content's access boundary to be no
broader than the intersection of its sources' boundaries. FR-TOOL-004 gives a
checkpoint "the conversation's" access boundary, which for a (task, agent)
conversation is the intersection of one task and one agent — a single
`Scope` value cannot express that. §9 requires unauthorized reads to return
`ErrNotFound` with no existence disclosure. FR-ASM-010/FR-DOM-003 require an
epoch rebase when a principal/authorization change or loss of eligibility
affects inherited content.

## Decision

- `AccessBoundary` (`internal/domain/principal.go`) is a **conjunction**
  of owner constraints `{session, workflow?, task?, agent?}`, not a single
  scope tag. `Scope` sets the *minimum* constraints a boundary of that scope
  must carry: TURN/TASK require a task, WORKFLOW a workflow, AGENT an agent,
  SESSION requires none beyond the session
  (`AccessBoundary.Validate`, `internal/domain/principal.go`).
  `BoundaryFor` (`internal/domain/principal.go`) constructs the
  boundary a given scope receives from an ingesting principal. Derived
  content may carry *extra* constraints beyond its nominal scope's minimum —
  a checkpoint is TASK-scoped but additionally agent-bound, matching
  FR-TOOL-004's "the conversation's" boundary, because a single scope cannot
  express a task-AND-agent intersection but a conjunction of constraints can.
- `AccessBoundary.Permits(principal)` (`internal/domain/principal.go`)
  is the access check: same session and every non-empty owner constraint
  matches. This is what FR-DOM-003's "access" means and what
  `AuthorizeMutation` checks first for every target
  (`internal/domain/authz.go`), returning `ErrNotFound` on failure so
  an unauthorized caller cannot distinguish "target doesn't exist" from
  "target exists but you can't see it" (§9).
- `AccessBoundary.Within(outer)` (`internal/domain/principal.go`) and
  `Intersect(scope, a, b)` (`internal/domain/principal.go`) implement
  FR-REL-008: `Within` checks that a derived boundary carries every
  constraint its source carries (no broader); `Intersect` computes the
  narrowest boundary satisfying two sources simultaneously, failing
  (`ok=false`) when no principal could satisfy both — e.g. two different
  tasks or two different sessions, which is exactly the empty-intersection
  case FR-REL-008 uses to reject an over-broad derived write.
- Access vs. eligibility are two different checks, per FR-DOM-003's own
  text distinguishing them. **Access** (used by API reads and archive
  retrieval, FR-RET-001) is `boundary.Permits(principal)` alone. **Eligibility**
  (used by automatic planning/dispatch, FR-DOM-003, FR-MAT-004) additionally
  requires:

  | Scope | Eligibility requires, beyond access |
  |---|---|
  | TURN | the current turn of an active task |
  | TASK | that task is active |
  | WORKFLOW | the workflow's active-owner state |
  | AGENT | the agent's active-owner state |
  | SESSION | no additional owner check (session-level content has no narrower active-owner state) |
  | any scope | an unexpired TTL, or an explicit retrieval lease (FR-RET-006) covering the item. A TTL is counted in turns of the item's originating task and is treated as expired when the originating task is terminal or is not the dispatching task's turn source (see the TTL decision below) |

  This table is recorded now (Phase 1) even though the eligibility engine
  (active task/turn tracking, lease issuance) is Phase 3/4 work
  (`domain.TaskState`, `internal/domain/records.go`, is the Phase 1
  building block; turn/lease enforcement is not yet implemented).
- Unauthorized reads return `domain.ErrNotFound`
  (`internal/domain/errors.go`), never a distinct "forbidden" error, at
  every layer: `AccessBoundary.Permits` false short-circuits to
  `ErrNotFound` in `AuthorizeMutation`
  (`internal/domain/authz.go`), and the same discipline is expected
  of `store.ReadTx` getters (documented at `internal/store/store.go`:
  "Every getter returns domain.ErrNotFound ... when the record does not
  exist in this session").
- Epoch validation: `domain.Conversation.RequireNewEpoch`
  (`internal/domain/call.go`) is the Phase 1 field that records "the
  next operation must rebase" (set today by abandonment, per ADR 17); the
  broader FR-ASM-010 rule — that a principal/authorization change or a loss
  of eligibility of *inherited* content also forces a rebase — is enforced
  starting Phase 5 (materialization), reusing this same field and the access/
  eligibility checks defined here.
- **Temporal eligibility of inherited/opaque content — decided now, built in
  Phases 3/5.** Conjunctive ownership alone (`Within`/`Permits`) cannot
  police this: TURN and TASK boundaries share the same owner fields, so
  ownership comparison cannot tell that a TURN-scoped item's owning turn has
  since ended, or that a retrieval lease covering an opaque provider block
  has expired. The rule, fixed now so Phases 3/5 implement one design
  instead of improvising two:

  1. Every inherited representation — retained history, an opaque provider
     block, a summary/checkpoint, or a retrieval projection — carries
     `Coverage` with explicit `ItemIDs` (`domain.Coverage`,
     `internal/domain/records.go`: "Dispatch rechecks every covered item's
     access, turn/task/TTL eligibility, and lease before inherited content
     is transmitted ... so the IDs must be complete; a range alone cannot
     show which items lost eligibility"). A `FromSeq`/`ToSeq` range is
     insufficient on its own because a range cannot be diffed against which
     specific items later lost eligibility. An opaque reasoning block/item covers every item rendered before it in the request, including system/instructions and tool definitions.
  2. Before every dispatch, the materialization strategy rechecks each
     covered item's access (`AccessBoundary.Permits`) and context
     eligibility (turn/task/TTL, per this ADR's eligibility table) — or an
     unexpired retrieval lease — for the dispatching principal and turn.
  3. Any failure forces removal of that representation and a rebase
     (FR-MAT-004, FR-ASM-010). This check is never deferred for cache
     savings: a cache hit is not evidence of eligibility.
  4. This does **not** mean a durable fact expires with the evidence it was
     derived from: a fact `DERIVED_FROM` now-expired evidence remains valid
     durable knowledge (FR-DOM-006's evidence/knowledge separation) — only
     *representations that themselves contain the expired evidence's bytes*
     (the opaque block, the raw history, the projection) are subject to
     removal, never the derived fact.
  5. Persisted inputs for the recheck are exactly: `Coverage.ItemIDs`, lease
     records (FR-RET-006, type not yet defined — see open item), and
     task/turn state (`domain.TaskState`). No additional hidden state is
     needed or permitted.

  Phase 1 ships `Coverage.ItemIDs` as a structural field; Phase 3 (ingestion)
  populates it when derived/opaque content is created; Phase 5
  (materialization) implements the pre-dispatch recheck and rebase.

## Alternatives considered

- **A single `Scope` enum as the whole access model.** Rejected: FR-TOOL-004's
  checkpoint boundary (task AND agent) cannot be expressed by any one of
  TURN/TASK/WORKFLOW/SESSION/AGENT; a conjunction of optional owner fields is
  the minimal structure that expresses both a plain scope and an
  intersection.
- **Merging access and eligibility into one check.** Rejected: FR-DOM-003
  and FR-RET-001 explicitly split them — Search/Get/Rehydrate may return
  authorized *expired* or *superseded* evidence with historical status
  (access without eligibility), while automatic planning must not select
  ineligible content even if it is accessible (eligibility on top of
  access). Collapsing them would either block legitimate archive reads or
  let automatic planning select expired content.
- **Distinguishing "not found" from "forbidden" in error responses.**
  Rejected by §9 directly: distinguishing them lets a caller probe for the
  existence of content outside their boundary by observing which error comes
  back. `ErrNotFound` uniformly for both cases is the chosen, and only
  compliant, behavior.
- **Letting `Intersect` return the wider of two boundaries when they
  conflict, rather than failing.** Rejected: FR-REL-008 requires the
  boundary be "no broader than the intersection of sources"; silently
  widening on conflict would let derived content leak beyond either source's
  audience. Failing (`ok=false`) forces the caller to reject the derived
  write, which is the only FR-REL-008-compliant outcome for an unsatisfiable
  intersection.
- **A `Coverage` range (`FromSeq`/`ToSeq`) alone, without explicit
  `ItemIDs`, for temporal-eligibility rechecks.** Rejected (Codex finding 7):
  a range says which sequence numbers were summarized, not which specific
  items later lost eligibility; a recheck against a range would have to
  re-scan and re-derive membership, which is exactly the kind of
  non-reconstructible state FR-ASM-007 forbids for materialization
  decisions. Explicit `ItemIDs` make the recheck a direct lookup.
- **Expiring a durable fact when the evidence it cites expires.** Rejected:
  FR-DOM-006 treats evidence and derived knowledge as separate categories
  precisely so a fact can outlive the (possibly bulky, possibly archived)
  evidence that supported it; only the *representation* that carries the
  expired evidence's bytes needs to be dropped and rebased around.

## Consequences / compatibility impact

- Every future package that reads items (planner, retrieval, tools) must
  route both access and eligibility through these two named checks rather
  than reimplementing scope comparisons, or the two-check discipline this
  ADR establishes erodes silently.
- The eligibility table above, and the temporal-eligibility rule for
  inherited/opaque content, are not yet enforced by any code; Phase 3/4/5
  work implements them against `domain.TaskState`/turn tracking, `domain
  .Coverage.ItemIDs`, and a lease record type that does not exist yet
  (`FR-RET-006`'s lease). Treat both as the accepted, decided contract those
  phases implement against, not as already-implemented behavior — deciding
  the rule now (rather than leaving it an open item) is what lets Phase 3
  ingestion and Phase 5 materialization be built against one design instead
  of two incompatible guesses.
- `RequireNewEpoch` is currently set only by call abandonment (ADR 17); when
  Phase 5 wires FR-ASM-010's eligibility-loss trigger, the same field is
  reused, so no new `Conversation` field is anticipated to be needed for
  that trigger.

## Tests that lock the behavior

- `internal/domain/principal_test.go`: `TestAccessBoundaryPermits` (same-
  session/cross-session, every scope's minimum-constraint enforcement),
  `TestBoundaryFor`, `TestAccessBoundaryValidate`, `TestAccessBoundaryWithin`
  (equal boundaries, a strict subset, two boundaries differing only in
  task), `TestIntersect`/`TestIntersectCommutative`/
  `TestIntersectResultWithinBoth` (including the checkpoint-style task+agent
  conjunction case from FR-TOOL-004), and `TestAccessBoundaryProperty`
  (property-based).
- `internal/domain/authz_test.go`:
  `TestAuthorizeMutation_InaccessibleTargetReturnsNotFoundBeforeAuthority`
  is the exact regression this ADR calls for — `ErrNotFound`, not
  `ErrInvalidAuthorityPromotion`, when access fails, checked before any
  authority branch runs.
- `internal/store/storetest` (`storetest.Run`): `TestConformance
  /ForeignSessionRecords` asserts every `ReadTx` getter returns
  `ErrNotFound` for an ID that exists only in a different session, and every
  `Tx` write against a foreign-session record fails `ErrInvalidRecord`;
  `TestConformance/SessionIsolation` covers the same boundary from the
  writing side.
- Trace T05 (resolved-goal archival not reopening) is directly locked by
  `TestConformance/GoalLifecycle`: resolve, archive, retrieve (residency
  back to RESIDENT without touching `GoalStatus`), then two distinct
  rejected reopen attempts (`ErrInvalidTransition`), then a same-status
  resolve as a no-op. Trace T04 (cross-agent/cross-session access) has a
  provenance-side counterpart already in `internal/graph/graph_test.go`:
  `TestProvenance_Truncation_T04` and `TestProvenance_RootMustBeAccessible`;
  the planner-side "B's assembly never includes A's private history" half
  of T04 remains a Phase 4/5 fixture once the planner exists.

## Open questions

### Resolved at acceptance (2026-09-26)

- Exact representation of a retrieval lease (FR-RET-006) — not yet a type in
  `internal/domain`; needed before Phase 6 (archive/retention) but the
  eligibility table above already assumes its shape (principal/task/agent/
  turn-bound, with an expiry).
  **Decision:** the retrieval lease type is deferred to Phase 6 and will be
  added to this ADR by amendment before Phase 6 exits; the eligibility
  table's assumed shape (principal/task/agent/turn-bound with expiry) is the
  constraint it must meet.
- Whether TTL expiry is measured in turns only (`ContextItem.TTLTurns`,
  `internal/domain/item.go`) or needs a session-sequence-based expiry too for
  scopes without a turn concept (WORKFLOW/AGENT/SESSION-scoped ephemeral
  content, if any is ever introduced).
  **Decision:** V1 TTL is measured in turns only: `TTLTurns` counts turns of
  the item's originating task (`TaskState.Turn`); no sequence-based expiry
  in V1.
  **Amendment (PR #4 review, SEC-1.3):** a TTL is only defined when the item
  names its originating task. `TTLTurns` therefore requires a non-empty
  `TaskID`; `ContextItem.Validate` rejects a TTL without one with
  `ErrInvalidRecord` (`internal/domain/item.go`). When expiry cannot be
  decided from the originating task, ambiguity resolves to **expired**
  (ineligible; fail closed): this covers an originating task that is
  terminal (its `Turn` no longer advances) and a dispatching task that is
  not the item's turn source. Access is unaffected. An expired item stays
  readable through the API and archive retrieval, and only an explicit
  retrieval lease (FR-RET-006) can re-admit it to a plan.

## Review

First pass (Codex gpt-6-sol xhigh, `codex-decision-review-out.md`, finding
7): conjunctive ownership, `Within`, and `Intersect` are correct for valid
ownership boundaries (no change to those). Deferred the temporal-eligibility
gap to an open item.

Second pass (Codex gpt-6-sol xhigh, `codex-contract-v2-review.md`, finding
7: **DEFERRED-WRONG**): deferring the *rule* itself (not just its
implementation) to Phases 3/5 left FR-DOM-003/FR-REL-008 and traces T03/T04
without a decided design for reviewers to accept at the Phase 1 gate.
Changed: the temporal-eligibility rule is now decided in this ADR's Decision
section — `Coverage.ItemIDs`, the pre-dispatch recheck, forced rebase on
failure, and the evidence-vs-fact expiry distinction — with only its
*implementation* (Phases 3/5) still pending, matching how `domain.Coverage`
already carries `ItemIDs` after the phase1/contract merge.

Test citations verified against the integrated `phase-1-foundation`
codebase (tip `ddbb53e`): every citation above is a real, passing test;
none of this ADR's originally "Required" test placeholders remained
genuinely missing except the Phase 4/5 planner-side fixtures noted above.

Phase 1 acceptance amendment (PR #4 review round 2, SEC-2.2): rule 1 now
states what an opaque reasoning block/item covers: every item rendered
before it in the request, including system/instructions and tool
definitions. The descriptor probes found that reasoning is replayed as
stale when earlier content changes (`Reasoning.PostEditReplay: STALE` on
profiles without a provider-side check). So the pre-dispatch recheck and
the adapter's post-edit strip rely on this coverage definition. A coverage
that records only the reasoning's own round would pass rule 1 as it was
worded before and still replay stale reasoning.
