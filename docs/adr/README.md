# Architecture Decision Records

ADRs record decisions that gate a delivery phase (SDD §11) before that
phase's exit gate is checked. SDD §15 lists all 18 required ADRs; this index
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

## Status of all 18 ADRs (SDD §15)

| # | Title | Gates phase (§11) | Status |
|---|-------|--------------------|--------|
| 1 | Go module path and minimum Go version | Phase 1 | [Written](0001-module-and-go-version.md) |
| 2 | Strict submitted-input accounting, reservations, verified counter bounds, safety margins, unsupported profiles | Phase 5 | pending — gates Phase 5 |
| 3 | SQLite driver and migration mechanism | Phase 1 | [Written](0003-sqlite-driver-and-migrations.md) |
| 4 | Stable IDs, event/call idempotency keys, conflict detection, canonical hashes | Phase 1 | [Written](0004-ids-idempotency-and-hashes.md) |
| 5 | Semantic scoring weights, SemanticBytes encoding, fixed-point scale, relevance threshold, resident-byte limits, soft-pressure fraction, retrieval-call windows, stub budget, checkpoint size limit, decision-trace retention | Phase 4 | pending — gates Phase 4 |
| 6 | Access-boundary and context-eligibility matrix, historical leases, expiry, epoch validation | Phase 1 | [Written](0006-access-and-eligibility.md) |
| 7 | Lexical index and normalization rules | Phase 4 | pending — gates Phase 4 |
| 8 | Observation identities, obligation matcher/claim versions, evidence applicability fingerprints, mutation grants, invalidation rules | Phase 3 | pending — gates Phase 3 |
| 9 | Provider transport libraries, retry policy, OpenAI API surface, verified reasoning replay rules | Phase 5 | pending — gates Phase 5 |
| 10 | Benchmark fixture/oracle, comparative statistics and run counts, baseline profiles, shared resource limits/projections, reproducible hardware/data profile | Phase 5 (initial fixture/profile); finalized Phase 8 | pending — gates Phase 5 |
| 11 | Render templates and delimiters per provider | Phase 5 | pending — gates Phase 5 |
| 12 | Capability profiles/verification, three-valued edit safety, canonical provider blocks and source coverage, REQUIRE/ALLOW_RESET policies, checkpoint compaction protocols | Phase 5 | pending — gates Phase 5 |
| 13 | Deployment model: embedded library, sidecar, or both | Phase 1 | [Written](0013-deployment-model.md) |
| 14 | Content redaction and retention after V1 | Phase 11 | pending — gates Phase 11 |
| 15 | Forecast horizon/growth/output/compaction assumptions, recorded cache/timing inputs, uncertainty/savings margins, local resource pricing, simulator validation limits | Phase 9 | pending — gates Phase 9 |
| 16 | Common mutation authorization, directive/obligation version replacement, independent residency/goal status, immutable source snapshots | Phase 1 | [Written](0016-mutation-authorization.md) |
| 17 | Provider call/attempt state machine, conversation reservation, outcome reconciliation, streaming completion, crash/retry tests | Phase 1 | [Written](0017-call-ledger.md) |
| 18 | Semantic state tool schemas, result formats, reference instruction block | Phase 5 | pending — gates Phase 5 |

Phase 1 (Contracts, domain, and stores) requires ADRs 1, 3, 4, 6, 13, 16, 17,
all written here. Phase 1's exit gate (state-transition, restart, graph, and
concurrency tests for the foundational event traces) is tracked in
`internal/domain/*_test.go` and `internal/store/storetest`, not in this
directory.

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

Independent review: Codex gpt-6-sol xhigh reviewed all seven Phase 1 ADRs
against the committed code and the SDD in two passes — the initial review
(`scratchpad/codex-decision-review-out.md`) and a verification pass after
contract v2 landed (`scratchpad/codex-contract-v2-review.md`), which found
several of the first pass's fixes PARTIAL and one (ADR 6's temporal-
eligibility deferral) DEFERRED-WRONG. Findings that changed a decision are
noted in that ADR's Review section, labeled by pass.
