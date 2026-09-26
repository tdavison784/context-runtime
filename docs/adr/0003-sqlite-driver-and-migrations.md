# 3. SQLite driver and migration mechanism

Status: Proposed
Date: 2026-09-25

## Context

FR-PER-001 requires in-memory and SQLite stores implementing the same
`store.Store` interface (`internal/store/store.go:26`), with Postgres
possible later behind the same contract. FR-PER-002/003/004 require the
store to persist every listed record type, reconstruct identical logical
state on restart, and preserve integrity under concurrent mutation. FR-CALL-002
requires durability of a SENT call before transport ("durability of SENT
before transport is a correctness requirement"). §9 requires SQLite files
readable only by their owner. INV-10 requires restart/concurrent mutation to
preserve valid state.

## Decision

- Driver: `modernc.org/sqlite` (pure Go, no cgo). Already an indirect
  dependency in `go.mod`. Chosen for cgo-free cross-compilation and so the
  race detector runs without a C toolchain in CI (ADR 1's no-cgo default
  build).
- Migrations: numbered SQL files embedded via `embed.FS`, applied in
  ascending order, each inside its own transaction. Applied migrations are
  recorded in a `schema_migrations(version, name, checksum)` table. A
  checksum mismatch against an already-applied migration is a startup error
  — no silent drift between a binary and the database it opens. Migrations
  are forward-only; no down migrations ship.
- Connection: WAL mode, `foreign_keys=ON`, a `busy_timeout`, and
  `synchronous=FULL` (required for FR-CALL-002's SENT durability guarantee —
  `NORMAL` risks losing a committed WAL frame on power loss, which would
  make a SENT call vanish rather than surface as UNKNOWN on restart). One
  writer connection; `Update` (`internal/store/store.go:28`) issues
  `BEGIN IMMEDIATE` so writer serialization is enforced by SQLite itself, not
  only by the in-process mutex.
- File permissions: the database file is created `0600` (§9: "SQLite files
  readable only by their owner").
- JSON columns are used only for leaf value lists that are never queried
  directly (parts, tags, evidence IDs, usage iterations); every field a
  `store.ItemFilter`/`RelationshipFilter`/`CallFilter` filters or orders by
  (FR-PER-002's structured fields — session/task/agent ID, kind, residency,
  Seq, PreparedSeq) is a real column.

## Alternatives considered

- **`mattn/go-sqlite3` (cgo).** Rejected for V1: faster in some benchmarks,
  but cgo complicates cross-compilation and CI matrix builds, and the race
  detector needs a C toolchain wired up per platform. Revisit under NFR
  profiling in Phase 11 if `modernc.org/sqlite`'s overhead is a measured
  bottleneck — not before, per SDD Appendix A's "profiling shows a real need"
  policy for non-Go tooling in general.
- **`golang-migrate`/`goose` for migrations.** Rejected: dependency weight
  for what Phase 1 needs is a handful of numbered files; a hand-rolled
  embedded-FS runner is under 100 lines and avoids taking on a library whose
  migration DSL this project does not otherwise need.
- **`synchronous=NORMAL` with WAL.** Rejected: WAL+NORMAL is durable across
  application crashes but can lose the last commit on OS/power loss; since
  FR-CALL-002 treats SENT durability as a correctness requirement (a lost
  SENT record after a crash would be indistinguishable from a call that was
  never sent, violating "no automatic resend" by removing the record that
  prevents one), FULL is required despite its write-latency cost.
- **Postgres for V1.** Rejected: ADR 13 fixes V1 as an embedded, single-process
  deployment; Postgres targets a separate deployment model FR-PER-001 defers
  ("may implement the same interface after SQLite acceptance").

## Consequences / compatibility impact

- `synchronous=FULL` trades write latency for the durability FR-CALL-002
  needs; this is a candidate for NFR profiling in Phase 11 but is not
  negotiable before then given the correctness requirement.
- Forward-only migrations mean a bad migration in production requires a new
  forward migration to fix, never a down-migration rollback; this must be
  documented for operators once V1 ships (Phase 11 hardening).
- The checksum-mismatch startup failure means a hand-edited or manually
  patched migration file makes the binary refuse to start against a database
  that already applied the old version — this is intentional (no silent
  drift) but needs a clear startup error message pointing at the mismatched
  version.
- One writer connection plus `BEGIN IMMEDIATE` means writer throughput is
  bounded by SQLite's single-writer model. `store.Store.Update`
  (`internal/store/store.go:41-46`) now documents different sessions as
  "independent logically but may be serialized by the implementation (the
  SQLite store has a single writer)" — the contract requires *correctness*
  under concurrent cross-session writes (no lost updates, no corruption), not
  that they run in parallel at the storage layer. A single writer connection
  satisfies that contract directly: bounded contention under SQLite's own
  serialization is an accepted Phase 1 cost, not a bug to design around with
  a writer-dispatch pool. Revisit only if NFR profiling (Phase 11) shows
  contention is an actual bottleneck.

## Tests that lock the behavior

- `internal/store/storetest` (already the shared conformance suite used by
  `builders.go`/`storetest.go`) must run against both the memory store and
  the SQLite store from the same test table, so any SQLite-specific
  transaction behavior is caught by the same assertions as the memory store.
- Required: an SQLite-specific test that starts a migration, kills the
  process mid-migration (or simulates it by truncating the WAL), and asserts
  a restart replays cleanly to a consistent `schema_migrations` state
  (T10-style crash recovery, generalized to schema setup rather than calls).
- Required: a test asserting a checksum mismatch on a previously-applied
  migration fails startup with a descriptive error rather than proceeding.
- Required: a file-permission test asserting a freshly created database file
  has mode `0600` on the platforms CI runs (skip or adapt on Windows).
- Required: a concurrency test under `-race` that runs `Update` against two
  or more *different* session IDs concurrently and asserts **correctness**
  (each session's writes commit atomically and are all present afterward,
  with no corruption or lost update) rather than asserting the sessions
  serialize or don't — bounded contention from SQLite's single writer is
  expected and is not itself a failure.

## Open questions

- Exact `busy_timeout` value; needs a number before the SQLite store lands,
  not just "a busy_timeout".

## Review

Scrutinized by Codex gpt-6-sol xhigh (`codex-decision-review-out.md`,
finding 14/low). Changed: the "assert no serialization" test framing (which
conflicted with SQLite's single-writer design) is replaced with a
correctness-under-concurrency test, and the consequences section no longer
treats single-writer contention as a gap to be engineered away.
