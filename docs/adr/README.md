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
| 1 | Go module path and minimum Go version | Phase 1 | [Accepted](0001-module-and-go-version.md) |
| 2 | Strict submitted-input accounting, reservations, verified counter bounds, safety margins, unsupported profiles | Phase 5 | pending — gates Phase 5 |
| 3 | SQLite driver and migration mechanism | Phase 1 | [Accepted](0003-sqlite-driver-and-migrations.md) |
| 4 | Stable IDs, event/call idempotency keys, conflict detection, canonical hashes | Phase 1 | [Accepted](0004-ids-idempotency-and-hashes.md) |
| 5 | Semantic scoring weights, SemanticBytes encoding, fixed-point scale, relevance threshold, resident-byte limits, soft-pressure fraction, retrieval-call windows, stub budget, checkpoint size limit, decision-trace retention | Phase 4 | pending — gates Phase 4 |
| 6 | Access-boundary and context-eligibility matrix, historical leases, expiry, epoch validation | Phase 1 | [Accepted](0006-access-and-eligibility.md) |
| 7 | Lexical index and normalization rules | Phase 4 | pending — gates Phase 4 |
| 8 | Observation identities, obligation matcher/claim versions, evidence applicability fingerprints, mutation grants, invalidation rules | Phase 3 | pending — gates Phase 3 |
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
| 19 | Directive parsing and ingestion: grammar, IDs, span/parse-unit isolation, deterministic classification, deduplication/replacement, Working snapshots, obligations, receipts, and lifecycle-command deferral | Phase 2 | [Proposed](0019-directive-parsing-and-ingestion.md) |

ADR 19 is not one of SDD §15's original 18; Phase 2's adversarial decision
review found the brief's decisions needed a dedicated ADR beyond that list,
and the commander added it by ruling (`phase2-amendments.md` R4). This
index intends it to gate Phase 2 exit the same way ADRs 1-18 gate their
phases (SPEC-1.13: SDD §11 item 2 does not yet name it, and §15 still
enumerates exactly 18 — an SDD amendment recording ADR 19 there, or
retracting this claim, is still open).

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

## SDD conflicts found and applied (v0.9)

- [0019](0019-directive-parsing-and-ingestion.md#sdd-amendment-applied-in-v09):
  Phase 2's decision review found five gaps the brief did not resolve:
  FR-ING-004 did not say a span/part is an isolated parse unit (M1);
  FR-ING-005 did not define a Working snapshot's duplicate identity (D11);
  FR-DIR-002 did not reject an explicit ID shaped like a derived one (D20);
  FR-DIR-005 named no closed vocabulary for "other lifecycle words" (M4);
  FR-DIR-006's `ttl` had no representation bound (D12, narrowed by R1 to
  1..2147483647). All five are applied as the exact sentences ADR 19
  records.

Independent review: Codex gpt-6-sol xhigh reviewed all seven Phase 1 ADRs
against the committed code and the SDD in two passes — the initial review
(`scratchpad/codex-decision-review-out.md`) and a verification pass after
contract v2 landed (`scratchpad/codex-contract-v2-review.md`), which found
several of the first pass's fixes PARTIAL and one (ADR 6's temporal-
eligibility deferral) DEFERRED-WRONG. Findings that changed a decision are
noted in that ADR's Review section, labeled by pass.
