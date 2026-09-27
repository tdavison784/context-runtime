# Architecture Decision Records

ADRs record decisions that gate a delivery phase (SDD §11) before that
phase's exit gate is checked. SDD §15 lists all 19 required ADRs; this index
tracks which are written and which phase still needs them.

## Format

Each ADR is a single Markdown file, numbered `NNNN-title.md`, with these
sections:

- **Status** — `Proposed` until the phase exits with the decision unchanged
  by review; `Accepted` once the phase gate has passed with tests green.
- **Date**
- **Context** — the FR/INV IDs the decision answers.
- **Decision**
- **Alternatives considered** — with why each was rejected.
- **Consequences / compatibility impact**
- **Tests that lock the behavior** — concrete packages/files where they
  exist; phrased as required tests where they do not exist yet.
- **Open questions**

A decision that conflicts with the current SDD text is recorded under a
**Required SDD amendment** subsection with the exact proposed replacement
text. The ADR does not edit SDD.md itself; once the amendment lands there
(as SDD v0.6's did), the subsection is retitled **SDD amendment (applied in
vX.Y)** and keeps the record of what changed and why.

Each ADR also carries a **Review** section once an independent review (e.g.
Codex) has scrutinized it, noting which findings changed the decision.

## Status of all 19 ADRs (SDD §15)

| # | Title | Gates phase (§11) | Status |
|---|-------|--------------------|--------|
| 1 | Go module path and minimum Go version | Phase 1 | [Accepted](0001-module-and-go-version.md) |
| 2 | Strict submitted-input accounting, reservations, verified counter bounds, safety margins, unsupported profiles | Phase 5 | pending — gates Phase 5 |
| 3 | SQLite driver and migration mechanism | Phase 1 | [Accepted](0003-sqlite-driver-and-migrations.md) |
| 4 | Stable IDs, event/call idempotency keys, conflict detection, canonical hashes | Phase 1 | [Accepted](0004-ids-idempotency-and-hashes.md) |
| 5 | Semantic scoring weights, SemanticBytes encoding, fixed-point scale, relevance threshold, resident-byte limits, soft-pressure fraction, retrieval-call windows, stub budget, checkpoint size limit, decision-trace retention | Phase 4 | pending — gates Phase 4 |
| 6 | Access-boundary and context-eligibility matrix, historical leases, expiry, epoch validation | Phase 1 | [Accepted](0006-access-and-eligibility.md) |
| 7 | Lexical index and normalization rules | Phase 4 | pending — gates Phase 4 |
| 8 | Observation identities, obligation matcher/claim versions, evidence applicability fingerprints, mutation grants, invalidation rules | Phase 3 | [Proposed](0008-observations-obligations-applicability-grants.md) — gates Phase 3; reconciled through PR #6 round 3. Status is Proposed because P3-42's required-test mapping is still incomplete (SPEC-1.23/2.14/3.9), not merely pending a formality |
| 9 | Provider transport libraries, retry policy, OpenAI API surface, verified reasoning replay rules | Phase 5 | pending — gates Phase 5 |
| 10 | Benchmark fixture/oracle, comparative statistics and run counts, baseline profiles, shared resource limits/projections, reproducible hardware/data profile | Phase 5 (initial fixture/profile); finalized Phase 8 | pending — gates Phase 5 |
| 11 | Render templates and delimiters per provider | Phase 5 | pending — gates Phase 5 |
| 12 | Capability profiles/verification, three-valued edit safety, canonical provider blocks and source coverage, REQUIRE/ALLOW_RESET policies, checkpoint compaction protocols | Phase 5 | pending — gates Phase 5 |
| 13 | Deployment model: embedded library, sidecar, or both | Phase 1 | [Accepted](0013-deployment-model.md) |
| 14 | Content redaction and retention after V1 | Phase 11 | pending — gates Phase 11 |
| 15 | Forecast horizon/growth/output/compaction assumptions, recorded cache/timing inputs, uncertainty/savings margins, local resource pricing, simulator validation limits | Phase 9 | pending — gates Phase 9 |
| 16 | Common mutation authorization, directive/obligation version replacement, independent residency/goal status, immutable source snapshots | Phase 1 | [Accepted](0016-mutation-authorization.md) |
| 17 | Provider call/attempt state machine, conversation reservation, outcome reconciliation, streaming completion, crash/retry tests | Phase 1 | [Accepted](0017-call-ledger.md) |
| 18 | Semantic state tool schemas, result formats, reference instruction block | Phase 5 | pending — gates Phase 5 |
| 19 | Directive parsing and ingestion: grammar, IDs, span/parse-unit isolation, deterministic classification, deduplication/replacement, Working snapshots, obligations, receipts, and lifecycle-command deferral | Phase 2 | [Accepted](0019-directive-parsing-and-ingestion.md) |

ADR 19 was not one of SDD §15's original 18; Phase 2's adversarial decision
review found the brief's decisions needed a dedicated ADR beyond that list,
and the commander added it by ruling (`phase2-amendments.md` R4). SDD §11
item 2 and §15 now list it (commander ruling, SPEC-1.13's open item
resolved): ADR 19 gates Phase 2 exit the same way ADRs 1-18 gate their
phases.

Phase 1 (Contracts, domain, and stores) requires ADRs 1, 3, 4, 6, 13, 16, 17,
all written here. Phase 1's exit gate (state-transition, restart, graph, and
concurrency tests for the foundational event traces) is tracked in
`internal/domain/*_test.go` and `internal/store/storetest`, not in this
directory.

Phase 2 (Directives and ingestion) requires ADR 19, written here. Phase 2's
exit gate (canonical directive examples, parser fuzzing, retry identity,
and injection resistance) is tracked in `internal/directive`,
`internal/ingest`, and `testdata/directives`, not in this directory.

Phase 3 (Semantic state engine) requires ADR 8, written here as Status:
Proposed pending the Phase 3 gate, and amends ADR 3, 4, 6, 16, 17, and 19
(each keeps its own Accepted Status from its own phase; the amendment
records what Phase 3 adds on top). Phase 3's exit gate (replacement,
resolution/rehydration, evidence invalidation, and mutation-authority traces
against event traces T02/T06/T07) is tracked primarily by `internal/ingest`'s
`TestGate*` suite, which exercises the real W3/W4/W5/W6 services end to end;
the new `internal/obligation` and `internal/lifecycle` packages carry the
matching service-level tests, not in this directory.

## SDD conflicts found and applied (v0.6)

- [0004](0004-ids-idempotency-and-hashes.md#sdd-amendment-applied-in-v06):
  the directive-ID derivation (FR-DIR-002) overflowed the `id` grammar's
  64-char cap (FR-DIR-006) once combined with a keyword prefix. Applied:
  FR-DIR-002 now specifies 64 hex digits explicitly; FR-DIR-006's cap is 80.
- [0016](0016-mutation-authorization.md#sdd-amendment-applied-in-v06):
  FR-TOOL-003 and FR-AUTH-001 jointly made `context_resolve` a dead code path
  for an AGENT principal; FR-DIR-007's same-task Working-snapshot
  supersession conflicted with the equal-boundary supersession rule. Both
  applied: FR-TOOL-003 states `context_resolve` never resolves in V1;
  FR-DIR-007 requires same authority *and* access boundary.

## SDD conflicts found and applied (v0.9)

- [0019](0019-directive-parsing-and-ingestion.md#sdd-amendment-applied-in-v09):
  Phase 2's decision review found five gaps the brief did not resolve:
  FR-ING-004 did not say a span/part is an isolated parse unit (M1);
  FR-ING-005 did not define a Working snapshot's duplicate identity (D11);
  FR-DIR-002 did not reject an explicit ID shaped like a derived one (D20);
  FR-DIR-005 named no closed vocabulary for "other lifecycle words" (M4);
  FR-DIR-006's `ttl` had no representation bound (D12, narrowed by R1 to
  1..2147483647). All five are applied as the exact sentences ADR 19
  records. Two later, separate v0.9 edits followed, each recorded in its
  own dated entry in ADR 19's amendment section (SPEC-3.4: previously
  conflated under one description here, which read as covering both and
  so as wrongly claiming neither changed FR text):
  - (SPEC-1.13, then recorded as an SDD amendment by SPEC-2.4) §11 item 2
    and §15 gained ADR 19's listing entry once Phase 2 code landed and the
    commander added ADR 19 by ruling — no FR/INV text changed, only the
    two ADR index lists.
  - (SPEC-2.7, then recorded as an SDD amendment by SPEC-3.4) FR-DIR-002's
    derived-ID-shape wording narrowed from "a lowercased keyword" to "a
    lowercased content-section keyword" (plus a new sentence naming the
    six content sections and excluding Resolve/Unpin) — this one **did**
    change FR text, scoping D20's rejection rule to the keywords that
    actually derive IDs.

## SDD conflicts found and applied (v0.10)

Phase 3's binding decision record
(`.worktrees/_commander/phase3-decisions.md`, P3-1 through P3-42, commander
rulings C-1 through C-21 FROZEN 2026-09-26) approved its "Required normative
amendments at freeze" table for SDD v0.10 in full, with C-1's text for the
first row. Applied:

- [0008](0008-observations-obligations-applicability-grants.md#sdd-amendment-applied-in-v010):
  FR-REL-001/§7 (SATISFIES becomes a typed derived relation, not a
  Relationship row), FR-AUTH-002/FR-OBL-002 (exact-version grants and the
  restricted cause-based invalidation path), and FR-OBL-005 (authenticated
  resource reporting and explicit assertion modes).
- [0006](0006-access-and-eligibility.md#sdd-amendment-applied-in-v010):
  FR-DOM-003 (session-lifetime WORKFLOW/AGENT owners), FR-TOOL-004 (split
  checkpoint source/coverage provenance), and FR-RET-006 (retrieval leaves
  Residency unchanged; a lease supplies admission instead — replacing this
  spec's earlier optional Residency=RESIDENT flip on retrieval, and
  `docs/sdd-event-traces.md`'s T05 accordingly).
- [0019](0019-directive-parsing-and-ingestion.md#sdd-amendment-applied-in-v010):
  FR-DIR-005/FR-ING-005 (C-1: ordinary identical restatement compares the
  immutable creation declaration and never reopens/re-pins/unarchives/
  rebinds by itself; an explicit authenticated `ActionReplaceDirective`
  intent may reuse identical content as an authorized exception).
- FR-TOOL-002 and FR-TOOL-003 (SDD.md directly; no dedicated ADR owns the
  keyed-write/completion-claim result wording specifically) gained the
  duplicate/unsupported-status clarification and the actual-observed-status
  result wording the table's C-19/C-18 rows record.
- §8 and §11 (SDD.md directly) record that Phase 3's internal mutation
  services gain typed request identities/intents ahead of any public
  `Runtime` signature change, and that basic retrieval leases and logical
  conversation membership land in Phase 3 rather than Phase 6.

ADR 8 itself remains Status: Proposed — these SDD.md edits are applied now,
per the commander's freeze ruling, independently of ADR 8 reaching Accepted
at the Phase 3 gate, the same precedent ADR 19's v0.9 edits set for this
repository (below).

**Final reconciliation against the integrated code (2026-09-26, head
`fc87199`).** ADR 8's decisions, and ADR 6/16/17/19's amendments, were
rewritten to cite real, `grep`-verified packages/functions/tests from the
landed `internal/obligation` (W4), `internal/tools`/`internal/graph`
membership (W5), and `internal/ingest` (W7) code, per W4/W5/W7's final
reports (`final-p3-w4.md`, `final-p3-w5.md`, `final-p3-w7.md`) and the
later commander rulings recorded there. New at this pass:

- [0003](0003-sqlite-driver-and-migrations.md#amended-in-phase-3-adr-8-2026-09-26-reconciled-against-integration-head-fc87199):
  Phase 3's twenty-seven forward migrations, 0018 through 0044 (PR #6
  round 3, SPEC-3.8: corrected from a stale "eleven ... 0018 through
  0028").
- [0017](0017-call-ledger.md#amended-in-phase-3-adr-8-2026-09-26-reconciled-against-integration-head-fc87199):
  `domain.OutcomeBinding`, exchange membership joined to a completed
  outcome through its trusted dispatcher, and confirmation that
  `Conversation.LogicalCalls` is the one completed-inference counter both
  this ADR and Phase 3's retrieval leases read.
- Three further SDD.md amendments this reconciliation surfaced, applied
  directly (no dedicated ADR owns the FR text specifically): FR-DOM-006 (a
  semantic tool's own acknowledgment is never `tool_result`/evidence — W5's
  ruling); FR-TOOL-001 (`context_get` and `context_rehydrate` are the only
  model-facing paths to the same lease-issuing admission; the underlying
  operation is HARNESS-only — W6's ruling 2, ADR 6); FR-GC-003/004 (only a
  conversation's newest checkpoint is
  protected by kind — C-17, ADR 16; GC triggers run only from an explicit
  enabled set — P3-38/39, ADR 16).
- ADR 4's "Canonical domain registry (Phase 3)" section (committed directly
  by workers W4/W7 during implementation, `549190c`/`6933b09`) was checked
  against the golden list in `internal/domain/canonical_domains_test.go`
  and found complete and accurate; no further edit was needed.

Independent review: Codex gpt-6-sol xhigh reviewed all seven Phase 1 ADRs
against the committed code and the SDD in two passes — the initial review
(`scratchpad/codex-decision-review-out.md`) and a verification pass after
contract v2 landed (`scratchpad/codex-contract-v2-review.md`), which found
several of the first pass's fixes PARTIAL and one (ADR 6's temporal-
eligibility deferral) DEFERRED-WRONG. Findings that changed a decision are
noted in that ADR's Review section, labeled by pass.
