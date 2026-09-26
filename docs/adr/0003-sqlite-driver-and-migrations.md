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
- **Typed-column schema, no opaque payload copy** (`internal/store/sqlite
  /schema.go`, migration reworked in place in commit `361a293` while still
  pre-release): each record kind gets its own `rec_*` table keyed on
  `(session_id, id, subkey)`; every other scalar or nested field occupies
  its own typed column (e.g. `rec_item` has one column per `ContextItem`
  field, including `f_access_scope`/`f_access_session_id`/... for the
  embedded `AccessBoundary`). A `_present` column distinguishes a nil
  pointer from a zero-valued nested record, and a parallel nil-marker
  column distinguishes a nil byte slice from an empty BLOB, so no record is
  ever stored as a second, opaque serialized copy alongside its columns —
  every column is independently inspectable and indexable.
  `TestEmbeddedSchemaMatchesTypes` asserts the compiled column set for
  every Go struct field against the migration's actual `PRAGMA
  table_info`, so the schema and the struct cannot drift silently.
- Connection settings are concrete, not placeholders: WAL mode,
  `foreign_keys=ON`, `synchronous=FULL`, and a `busy_timeout` defaulting to
  5 seconds (`Store` option `WithBusyTimeout`, `internal/store/sqlite
  /sqlite.go`) — resolving this ADR's earlier open question.
- Migrations are tracked in `schema_migrations(version INTEGER PRIMARY KEY,
  name TEXT, checksum TEXT)`; `Open` compares each applied version's stored
  checksum against the embedded file's and fails to open on any mismatch.

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

- `internal/store/storetest.Run` is the shared conformance suite
  (`internal/store/storetest/storetest.go`); `internal/store/sqlite
  /sqlite_test.go:TestConformance` and `internal/store/memory
  /memory_test.go:TestConformance` both run it, so every assertion below
  applies identically to both stores.
  - `TestConformance/ConcurrentUpdatesDense` (`storetest/transactions.go`)
    is the correctness-under-concurrency test this ADR calls for: 16
    workers write concurrently to each of two different sessions (with
    every fifth transaction deliberately rolled back), and the test asserts
    each session's committed sequence numbers are dense and gapless and its
    stored items match exactly — never that the sessions ran in parallel or
    serialized. Run under `go test -race`, this is the assertion that
    matters, not a timing observation.
  - `TestConformance/SessionIsolation` and `.../ForeignSessionRecords`
    assert cross-session data never leaks regardless of write ordering.
- `internal/store/sqlite/durability_test.go`:
  - `TestRestartPreservesRecords` closes and reopens the store and asserts
    identical logical state (FR-PER-003).
  - `TestMigrationChecksumMismatch` asserts a hand-altered applied migration
    fails `Open` rather than proceeding silently.
  - `TestEmbeddedSchemaMatchesTypes` asserts the typed-column schema matches
    every Go struct field, locking the no-opaque-copy design above.
  - `TestFileCreatedPrivate` asserts a freshly created database file is mode
    `0600`.
  - `TestConcurrentSequenceDensity` asserts dense, gapless sequence
    allocation under 24 concurrent writers to one session.
  - `TestCallTransitionsRequireAttemptEvidence`, `TestObligationTransitionCAS`,
    and `TestAuditedGrantAndTaskMutations` are SQLite-specific
    reconfirmations of the store contract rules ADRs 16 and 17 describe
    (evidence-gated `UpdateCall`, obligation-transition CAS, atomic audit
    writes) — genuinely SQLite-specific because they exercise the typed-
    column write path, not just the in-memory one.
- The full `internal/store/sqlite` package runs in about 5-6s under
  `go test -race ./... -count=1` (measured on this branch), consistent with
  a real SQLite file per test rather than a mocked backend.

## Open questions

None remaining for this ADR's original scope; `busy_timeout` (5s default,
`WithBusyTimeout` to override) is now decided in code.

## Review

First pass (Codex gpt-6-sol xhigh, `codex-decision-review-out.md`, finding
14/low): the "assert no serialization" test framing conflicted with
SQLite's single-writer design; replaced with a correctness-under-
concurrency test, and the consequences section stopped treating
single-writer contention as a gap to engineer away.

Verified against the integrated `internal/store/sqlite` implementation
(commits `361a293`, `70d169e`, and the full durability/conformance suite):
the typed-column schema, concrete connection settings, and the specific
tests above replace what were "Required" placeholders in the first-pass
version of this ADR. No further Codex finding is open against this ADR.
