# 6. Access boundary and context eligibility matrix

Status: Proposed
Date: 2026-09-25

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

- `AccessBoundary` (`internal/domain/principal.go:26-36`) is a **conjunction**
  of owner constraints `{session, workflow?, task?, agent?}`, not a single
  scope tag. `Scope` sets the *minimum* constraints a boundary of that scope
  must carry: TURN/TASK require a task, WORKFLOW a workflow, AGENT an agent,
  SESSION requires none beyond the session
  (`AccessBoundary.Validate`, `internal/domain/principal.go:57-72`).
  `BoundaryFor` (`internal/domain/principal.go:41-51`) constructs the
  boundary a given scope receives from an ingesting principal. Derived
  content may carry *extra* constraints beyond its nominal scope's minimum —
  a checkpoint is TASK-scoped but additionally agent-bound, matching
  FR-TOOL-004's "the conversation's" boundary, because a single scope cannot
  express a task-AND-agent intersection but a conjunction of constraints can.
- `AccessBoundary.Permits(principal)` (`internal/domain/principal.go:75-80`)
  is the access check: same session and every non-empty owner constraint
  matches. This is what FR-DOM-003's "access" means and what
  `AuthorizeMutation` checks first for every target
  (`internal/domain/authz.go:180-183`), returning `ErrNotFound` on failure so
  an unauthorized caller cannot distinguish "target doesn't exist" from
  "target exists but you can't see it" (§9).
- `AccessBoundary.Within(outer)` (`internal/domain/principal.go:88-93`) and
  `Intersect(scope, a, b)` (`internal/domain/principal.go:97-116`) implement
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
  | any scope | an unexpired TTL, or an explicit retrieval lease (FR-RET-006) covering the item |

  This table is recorded now (Phase 1) even though the eligibility engine
  (active task/turn tracking, lease issuance) is Phase 3/4 work
  (`domain.TaskState`, `internal/domain/records.go:150-165`, is the Phase 1
  building block; turn/lease enforcement is not yet implemented).
- Unauthorized reads return `domain.ErrNotFound`
  (`internal/domain/errors.go:12`), never a distinct "forbidden" error, at
  every layer: `AccessBoundary.Permits` false short-circuits to
  `ErrNotFound` in `AuthorizeMutation`
  (`internal/domain/authz.go:180-183`), and the same discipline is expected
  of `store.ReadTx` getters (documented at `internal/store/store.go:76`:
  "Every getter returns domain.ErrNotFound ... when the record does not
  exist in this session").
- Epoch validation: `domain.Conversation.RequireNewEpoch`
  (`internal/domain/call.go:87-89`) is the Phase 1 field that records "the
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
     specific items later lost eligibility.
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

- Required: `internal/domain/principal_test.go` — `Permits` matrix covering
  same-session/cross-session, and each scope's minimum-constraint
  enforcement (`Validate` failing when a required owner field is empty);
  `Within`/`Intersect` covering: equal boundaries, a strict subset, two
  boundaries differing only in task (expect `ok=false`), and the
  checkpoint-style task+agent conjunction case from FR-TOOL-004.
- Required: `internal/domain/authz_test.go` — `AuthorizeMutation` returns
  `ErrNotFound` (not `ErrInvalidAuthorityPromotion`) when the actor lacks
  access to a target, verified by asserting the error via `errors.Is` before
  any authority check runs, so a probe cannot distinguish the two failure
  modes.
- Required: `internal/store/storetest` — every `ReadTx` getter returns
  `ErrNotFound` for an ID that exists in a different session, not a
  different error type.
- Trace T04 (cross-agent access) and trace T05 (resolved-goal archival not
  reopening) in `docs/sdd-event-traces.md` are the fixtures Phase 3/4 must
  turn into `internal/store/storetest` and `internal/domain` cases once
  eligibility/leases exist; they are the acceptance tests for this ADR's
  eligibility table.

## Open questions

- Exact representation of a retrieval lease (FR-RET-006) — not yet a type in
  `internal/domain`; needed before Phase 6 (archive/retention) but the
  eligibility table above already assumes its shape (principal/task/agent/
  turn-bound, with an expiry).
- Whether TTL expiry is measured in turns only (`ContextItem.TTLTurns`,
  `internal/domain/item.go`) or needs a session-sequence-based expiry too for
  scopes without a turn concept (WORKFLOW/AGENT/SESSION-scoped ephemeral
  content, if any is ever introduced).

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
