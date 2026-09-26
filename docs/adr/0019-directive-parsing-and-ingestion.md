# 19. Directive parsing and ingestion

Status: Proposed
Date: 2026-09-26

## Context

Phase 2 (SDD §11 item 2) implements FR-ING-001..007, FR-AUTH-001,
FR-DIR-001..007, section 9 (security), and event traces T01/T02 (ingestion
half), T06 (parse/authorization half), T18 (full), against the Phase 1
foundation (`internal/domain`, `internal/store`, `internal/graph`,
`internal/invocation`) accepted in ADRs 1, 3, 4, 6, 13, 16, 17. The
commander's Phase 2 brief proposed decisions D1-D19 (D20 was added from
ADR 4's acceptance). An adversarial decision review (Codex gpt-6-astra
xhigh) found D2, D4, D9 correct as written and amended the other sixteen,
reproducing three concrete counterexamples against the merged Phase 1 code
(lossy SQLite text storage, a duplicate directive resolving as current by
literal ID, and `SupersedeSnapshot` rejecting a duplicate Working member
instead of retiring the item it should have replaced) and adding eight
missing decisions, M1-M8. The commander accepted every amendment and
missing decision as written, with rulings R1-R8 that further narrow or
override eight of them. This ADR records the resulting decisions — the
brief's choice, the review's amendment, and the commander's ruling where one
applies — grouped by subsystem, for Phase 2's five worker branches
(`p2-contract`, `p2-parser`, `p2-store`, `p2-graph`, `p2-ingest`).

Sources: `phase2-brief.md` (D1-D20), `phase2-decision-review.md`
(amendments and M1-M8), `phase2-amendments.md` (R1-R8, binding).
