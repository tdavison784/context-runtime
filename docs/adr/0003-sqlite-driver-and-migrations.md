# 3. SQLite driver and migration mechanism

Status: Proposed
Date: 2026-09-25

## Context

FR-PER-001 requires in-memory and SQLite stores implementing the same
`store.Store` interface (`internal/store/store.go`), with Postgres
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
  writer connection; `Update` (`internal/store/store.go`) issues
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
- **Commit is never cancelled; context/I/O errors are returned as
  themselves (round 1, DUR-1.2/1.4, decided in `store.go`'s contract,
  `sqlite-worker`'s implementation in progress).** Two related failures
  were reproduced against the pre-round-1 SQLite store. First (DUR-1.2):
  the final `UPDATE sessions`/`COMMIT` ran under the caller's `ctx`; if that
  context was cancelled while `COMMIT` was in flight, `modernc.org/sqlite`
  can return `ctx.Err()` even though the commit succeeded, so `Update`
  reported failure for a transaction that had, in fact, committed — 3000
  trials with a randomly-timed cancellation reproduced 52 reported
  failures, 26 of which had committed anyway. For the ledger this means
  `MarkSent` could report an error after SENT was already durable (leaving
  the dispatcher unable to send, `Cancel` unable to cancel, and the
  reservation stuck until restart triggers `Recover`), and `Prepare` could
  report an error while still holding a PREPARED reservation with no
  returned `CallID` to reference it by. **Decision:** once the context-
  cancellation check before commit passes, run the last statement and
  `COMMIT` under `context.WithoutCancel(ctx)`, so cancellation can only
  ever prevent a commit, never misreport one that happened.
  Second (DUR-1.4): `get` wrapped every `Scan` error, including driver
  BUSY/IO errors and a cancelled context, as `ErrIntegrity` with `%v` (not
  `%w`), so `errors.Is(err, context.Canceled)` was always false after a
  cancellation and a timeout was indistinguishable from data corruption —
  `RecordOutcome` would report a cancelled context as "integrity check
  failed," which is actively misleading during incident response.
  **Decision:** `ErrIntegrity` is reserved for verification failures only
  (a blob whose bytes no longer match its hash, a row that fails to
  decode); every other error, including context and driver errors, is
  returned as itself (wrapped with `%w`, not converted), and `Update`/
  `View` return `ctx.Err()` directly when the context is done. Both
  decisions are now the documented contract in `store.Store`'s doc comment;
  the memory store already satisfies both trivially (no cancellable
  commit phase, no `Scan`-based decoding).
- **Re-entrant `Store` calls from inside `fn` are forbidden, not detected
  (round 1, DUR-1.5).** With the single-connection-per-store, single-
  writer-mutex-per-session design, calling back into the same `Store` from
  inside `Update`'s or `View`'s `fn` — even for a *different* session —
  deadlocks under SQLite (reproduced: blocks until the context deadline)
  while the memory store happens to tolerate it. No current caller nests
  calls. **Decision:** document the prohibition on the `Store` interface
  rather than add re-entrancy detection now; a ctx-marker-based fail-fast
  check is an available future hardening step (Phase 11) if a caller ever
  needs to nest legitimately, but no such caller exists yet to justify the
  complexity.
- **Concurrent `Open` on a fresh database file, and interrupted-migration
  recovery (round 1, DUR-1.6/1.7, decided; `sqlite-worker`'s implementation
  pending).** Reproduced: several concurrent `Open` calls against a
  brand-new file mostly fail `SQLITE_BUSY` at the `PRAGMA journal_mode=WAL`
  step (3 of 4 failed in 5 of 6 runs) — `busy_timeout` was applied only
  *after* that pragma, and each migration's implicit deferred transaction
  cannot wait out a concurrent writer at the WAL switch. **Decision:**
  apply `busy_timeout` through the connection DSN (`_pragma=busy_timeout`)
  so it is in effect before the WAL switch, retry the WAL-mode switch on
  `SQLITE_BUSY`, and run each migration under `BEGIN IMMEDIATE` rather than
  a deferred transaction. Separately, ADR 3 already requires a migration
  interrupted partway through to leave the database in a state a restart
  can cleanly recover from (no partial `schema_migrations` row, no partial
  `rec_*` tables) — an explicit test for this (interrupt a migration whose
  last statement fails; assert both tables are absent, then reopen
  successfully) does not exist yet and is still required.

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
- **Checking `ctx.Err()` only before commit, accepting that a cancellation
  mid-commit might misreport a success as a failure.** Rejected (DUR-1.2):
  "might" was measured at roughly half of reported cancellation failures
  actually having committed — that is not a rare race to accept, it is the
  common case once a cancellation lands in the commit window, and the
  ledger-level consequences (a stuck reservation, a `MarkSent` that can't
  tell the caller SENT already happened) are exactly the failure modes
  this whole ADR's durability decisions exist to prevent.
- **Detecting re-entrant `Store` calls with a ctx marker now, instead of
  just documenting the prohibition.** Rejected for Phase 1 (DUR-1.5): no
  caller currently nests calls, so the detection code would have no test
  driving it beyond a synthetic one; documenting the constraint is the
  proportionate fix until a real caller needs to violate it, at which point
  the design question is "should this caller nest" rather than "how do we
  detect nesting."
- **Retrying only the WAL-mode switch on `SQLITE_BUSY`, without also moving
  `busy_timeout` earlier or changing the migration transaction mode.**
  Rejected (DUR-1.6): reproduced as insufficient on its own — setting
  `busy_timeout` first together with `_txlock=immediate` still left
  failures at the WAL switch in testing; the fix needs the DSN-level
  pragma, the retry, and `BEGIN IMMEDIATE` migrations together.

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
  (`internal/store/store.go`) now documents different sessions as
  "independent logically but may be serialized by the implementation (the
  SQLite store has a single writer)" — the contract requires *correctness*
  under concurrent cross-session writes (no lost updates, no corruption), not
  that they run in parallel at the storage layer. A single writer connection
  satisfies that contract directly: bounded contention under SQLite's own
  serialization is an accepted Phase 1 cost, not a bug to design around with
  a writer-dispatch pool. Revisit only if NFR profiling (Phase 11) shows
  contention is an actual bottleneck.
- `context.WithoutCancel` on the final statement/`COMMIT` means a caller's
  cancellation can no longer abort a transaction that has begun committing;
  this is intentional (the alternative is misreporting), but it does mean
  a cancelled `ctx` bounds *when* a transaction can still be aborted, not a
  hard guarantee that cancellation always aborts it.
- Reserving `ErrIntegrity` for verification failures only is a breaking
  behavior change for any caller that was matching on `ErrIntegrity` to
  detect a broad "something went wrong reading from SQLite" condition;
  such a caller must now handle context/driver errors separately, which is
  the point of the fix (they are different failure classes with different
  correct responses).
- The DUR-1.6/1.7 SQLite implementation work is not yet landed as of this
  ADR update; until it is, concurrent `Open` on a fresh file remains
  unreliable and no interrupted-migration test exists, both genuine gaps
  against this ADR's decided design.

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

### Round 1 additions (findings DUR-1.2, 1.4, 1.6, 1.7) — landed

`internal/store/sqlite`'s round-1 implementation is merged and passing:

- `internal/store/sqlite/dur_1_2_test.go:TestCancelledUpdateRollsBack`
  cancels the context at a randomly-timed point during `Update` and
  asserts that any reported error corresponds to a transaction that did
  **not** commit — the exact DUR-1.2 regression, as a bounded deterministic
  test rather than a one-off experiment.
- `internal/store/sqlite/dur_1_4_test.go:TestScanCancellationIsOperationalError`
  asserts a cancelled context surfaces as
  `errors.Is(err, context.Canceled)` from a read, never wrapped as
  `ErrIntegrity`; `TestEmptyAndCorruptBlob` (pre-existing) confirms a
  genuine decode failure still produces `ErrIntegrity`.
- `internal/store/sqlite/dur_1_6_test.go:TestConcurrentFirstOpen` runs N
  goroutines calling `Open` on the same fresh file path concurrently and
  asserts they all succeed — the exact DUR-1.6 regression.
- `internal/store/sqlite/dur_1_7_test.go:TestInterruptedMigrationReplays`
  is the interrupted-migration replay test this ADR has called for since
  its first version: a migration whose last statement fails leaves
  `schema_migrations` empty and no `rec_*` tables present, and a
  subsequent `Open` on the same file succeeds cleanly.
- `internal/store/sqlite/contract_review_test.go:TestSessionsListsCommittedRecords`
  covers `Store.Sessions` (DUR-1.8, ADR 17) SQLite-specifically.

**Known regression found during verification, not one of the four DUR
findings above:** `TestConformance/DirectiveBoundaries` — new round-1
coverage for ADR 4's boundary-keyed directive identity — fails on
`internal/store/sqlite` only. Querying `CurrentDirective` with a boundary
from a different session than the transaction's returns `invalid record:
record belongs to another session` instead of the `ErrNotFound` the memory
store and the test both expect; this is the AUTH-1.3 existence-disclosure
pattern recurring in a new code path. `go test ./internal/store/sqlite/...`
fails on this subtest as of this update. See ADR 4/17 for the full
citation; flagging here too since it is this package's bug.

## Open questions

None remaining for this ADR's original scope; `busy_timeout` (5s default,
`WithBusyTimeout` to override) is decided in code. Round 1's four DUR
findings are fixed and tested; the `DirectiveBoundaries` regression above
is the one open item against this package.

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

**Round 1 review** (PR #2; DUR — Claude Opus, `dur-review-round1.md`).
DUR-1.2 (MEDIUM): a context cancellation during `COMMIT` could report
failure for a transaction that actually committed (measured: about half of
reported failures had committed); fixed by running the final statement and
`COMMIT` under `context.WithoutCancel` once the pre-commit cancellation
check passes. DUR-1.4 (MEDIUM): every `Scan` error, including context
cancellation, was mapped to `ErrIntegrity`, making a timeout indistinguishable
from data corruption; fixed by reserving `ErrIntegrity` for verification
failures and returning context/driver errors as themselves. DUR-1.5 (LOW):
re-entrant `Store` calls deadlock under SQLite; documented as forbidden
rather than detected, since no caller currently nests. DUR-1.6 (LOW):
concurrent `Open` on a fresh file mostly fails `SQLITE_BUSY` at the WAL-mode
switch; decided fix is DSN-level `busy_timeout`, a WAL-switch retry, and
`BEGIN IMMEDIATE` migrations. DUR-1.7 (LOW): the interrupted-migration
replay test this ADR has called for since its first version still doesn't
exist. All five decisions are recorded above.

Verified against the merged `sqlite-worker` branch: all four DUR findings
now have a passing test (`TestCancelledUpdateRollsBack`,
`TestScanCancellationIsOperationalError`, `TestConcurrentFirstOpen`,
`TestInterruptedMigrationReplays`), cited in the "Round 1 additions"
subsection. One regression found independently while verifying, not one
of the four DUR findings: `TestConformance/DirectiveBoundaries` fails on
this package (see Tests, above, and ADR 4/17 for the full citation).
