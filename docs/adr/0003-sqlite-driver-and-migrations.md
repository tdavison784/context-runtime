# 3. SQLite driver and migration mechanism

Status: Accepted (2026-09-26, Phase 1 exit; decision unchanged by review rounds 1-3 of PR #2)
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
  `TestMigratedSchemaMatchesTypes` (`internal/store/sqlite/durability_test.go`
  — renamed from `TestEmbeddedSchemaMatchesTypes` at migration 0002, commit
  `7575a47` [SPEC-1.9: corrected from an earlier, wrong "0002-0007" span],
  see below) asserts the compiled column set for
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
  themselves (round 1, DUR-1.2/1.4).** Two related failures were
  reproduced against the pre-round-1 SQLite store. First (DUR-1.2):
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
  recovery (round 1, DUR-1.6/1.7).** Reproduced: several concurrent `Open`
  calls against a brand-new file mostly fail `SQLITE_BUSY` at the `PRAGMA
  journal_mode=WAL` step (3 of 4 failed in 5 of 6 runs) — `busy_timeout`
  was applied only *after* that pragma, and each migration's implicit
  deferred transaction cannot wait out a concurrent writer at the WAL
  switch. **Decision:** apply `busy_timeout` through the connection DSN
  (`_pragma=busy_timeout`) so it is in effect before the WAL switch, retry
  the WAL-mode switch on `SQLITE_BUSY`, and run each migration under
  `BEGIN IMMEDIATE` rather than a deferred transaction. Separately, ADR 3
  already requires a migration interrupted partway through to leave the
  database in a state a restart can cleanly recover from (no partial
  `schema_migrations` row, no partial `rec_*` tables); see Tests, below,
  for the test that locks it.
- **Phase 2 reality: migrations 0002-0011, checksum pinning as a test,
  lossless leaf-list encoding (D3, R8; M1, M5, M6, D10, D13, D14/D16, R19
  via ADR 19).** Ten migrations have landed on top of 0001 (SPEC-1.9:
  corrected from an earlier, incomplete "0002-0010"/"nine migrations"
  count), each for a decision this ADR's "forward-only, never edited" rule
  already covered but Phase 2 is the first phase to actually exercise:
  - `0002_lossless_parts.sql` and `0003_lossless_string_lists.sql` rewrite
    every row's leaf-list columns from plain `encoding/json` (which
    silently replaced invalid UTF-8 with U+FFFD) into the lossless form
    below;
  - `0004_item_provenance_and_claims.sql` adds item role, creation turn,
    source ranges (D8/D18/M1), and obligation claim names (D13), with
    pre-0004 rows reading NULL as each field's zero value rather than an
    invented nonzero default;
  - `0005_current_version_namespace.sql` adds the typed
    DIRECTIVE/AGENT_KEY namespace to the current-version key (M6/R6; see
    ADR 4's amendment note — the untyped `CurrentDirective`/
    `CurrentDirectives` methods this migration originally served were
    later deleted in favor of `CurrentVersion`/`CurrentVersions`, a
    Go-level rename this migration is unaffected by);
  - `0006_obligation_source_index.sql` indexes obligation versions by
    source item so `internal/graph` can find every version bound to a
    replaced source with a bounded query (D13/R9);
  - `0007_ingestion_records.sql` adds `rec_envelope`/`rec_receipt`/
    `rec_receipt_item`/`rec_diagnostic`/`rec_command` for D14's immutable
    receipts and D16's diagnostics;
  - `0008_unresolved_references.sql` adds `rec_reference`, keyed by an
    occurrence-derived ID, indexed on `(session_id, locator_key,
    rule_version, seq, id)`, so a References entry that matched no
    ingested item at parse time survives restart for later linking (M5,
    R2, R18; `domain.UnresolvedReference`);
  - `0009_item_blob_index.sql` adds `item_blobs(session_id, blob_hash,
    item_id)`, backfilled from existing rows' lossless parts, so blob-
    reference authorization (§15/D19/R5) finds every item referencing a
    blob without a session-wide scan (R19);
  - `0010_item_duplicate_index.sql` normalizes pre-0004 NULL `f_role`
    columns to `''` (the semantic-role zero value) and adds an index on
    `(session_id, content_hash, task, section, role, authority, access
    boundary)`, the exact tuple D10's duplicate-candidate comparison uses,
    so it is a bounded lookup rather than a session-wide scan (R19);
  - `0011_item_source_index.sql` adds `item_sources(session_id,
    rule_version, locator_key, item_id)`, so References locator matching
    (§16/M5/R2) is a bounded lookup (`ItemsBySourceKey`) rather than a
    session-wide scan (R19), and registers a **Go migration step**
    (`backfillItemSources`, `internal/store/sqlite/steps.go`) that runs
    inside the same transaction as the SQL, indexing every pre-0011 item's
    source locator key by calling live `domain.LocatorKey`.

  "Migrations are forward-only; no down migrations ship" (above) is now a
  literal test, not only documented policy: `committedMigrations`
  (`internal/store/sqlite/durability_test.go`) pins every embedded
  migration file's SHA-256 checksum, and `TestCommittedMigrationsUnchanged`
  fails if a committed file's bytes — or the set of embedded files —
  changes; a schema change can only ever land as a new numbered file added
  to that map, never an edit to an existing entry. **A Go migration step's
  identity is now part of that checksum too (F5: DUR-1.8/SPEC-1.9,
  landed).** The checksum originally covered only the `.sql` bytes
  (`sqlite.go:142` in the pre-fix build), leaving migration 0011's
  registered Go step outside protection and dependent on live
  `domain.LocatorKey` — an edit to either would have been caught by
  neither `TestCommittedMigrationsUnchanged` nor
  `TestMigrationChecksumMismatch`. `p2-store` fixed this: migration
  0011's backfill now lives in `steps_0011.go` as a frozen, private copy
  of locator rule v1 (no import of `internal/domain`'s live rule), keyed
  by a stable step identity (`"0011/item-sources/reference-locator-v1"`,
  `steps.go`'s `migrationSteps` map); `migrationChecksum(sqlBytes, number)`
  appends a step's identity to its migration's SQL bytes before hashing
  when one exists, so `TestCommittedMigrationsUnchanged`'s pinned checksum
  for 0011 now covers the step too, and `TestCommittedStepsUnchanged`
  separately pins each step's identity and the SHA-256 of the file holding
  its frozen code. A migration with no Go step keeps its plain SQL
  checksum, unaffected. **This changed migration 0011's stored checksum
  value: a pre-release database that applied 0011 under the earlier build
  fails the checksum check on open and must be recreated** — acceptable
  before V1 release, since no production data exists yet (this ADR's own
  "no production data to migrate" precedent for prior breaking ID-scheme
  changes, ADR 4).
- **Lossless leaf-list encoding (D3, R8).** `internal/store/sqlite/lossless.go`
  replaces the plain-JSON leaf-list encoding this ADR originally specified
  ("JSON columns are used only for leaf value lists," above) with a form
  where every string is the lowercase hex of its exact bytes, integers/
  booleans are JSON numbers/booleans, a struct is an object keyed by every
  exported field name, and a nil slice/pointer is JSON `null` — so
  arbitrary byte sequences (invalid UTF-8, an embedded NUL) round-trip
  exactly instead of being silently corrupted by `encoding/json`'s UTF-8
  repair. Decoding is strict: an unknown/missing field, invalid hex, a
  wrong JSON type, or trailing data fails as `domain.ErrIntegrity` at the
  caller rather than a value being invented, so extending a listed type
  needs a forward migration (M8), not a decoder that silently accepts old
  and new shapes alike. Migrations 0002/0003 rewrote every row stored in
  the old plain-JSON form; a row `0001` had already corrupted (e.g. a
  content part whose hash no longer matches its now-`\uFFFD`-repaired
  text) reads back as `domain.ErrIntegrity` after upgrade rather than a
  silently wrong value, matching M8's "no invented executable state for a
  record that predates new metadata."
- **Indexed lookups are asserted, not just indexed (R19).**
  `internal/store/sqlite`'s four new bounded lookups —
  `ItemsByBlob(blobHash, limit)` (migration 0009), `DuplicateCandidates(f
  DuplicateFilter)` (migration 0010), matching an unresolved reference by
  locator key (migration 0008), and `ItemsBySourceKey` (migration 0011,
  SPEC-1.9: added) — are backed by a real index, not merely documented as
  one: `assertIndexed` (`internal/store/sqlite/lookups_test.go`)
  runs `EXPLAIN QUERY PLAN` on the exact query each method issues and fails
  if SQLite's plan contains an unindexed `SCAN` step or no `USING` step at
  all, so a future change that silently drops the index (rather than the
  Go method signature) is caught the same way a schema drift is.

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
- Since Phase 2, "never edit a committed migration" is enforced by
  `TestCommittedMigrationsUnchanged`'s pinned-checksum map, not only by
  `TestMigrationChecksumMismatch`'s runtime check against an already-applied
  database: every future migration adds a new entry to `committedMigrations`
  rather than touching an existing one, and forgetting to add it fails CI
  immediately, before any database ever sees the new file.
- The lossless leaf-list encoding (D3, R8) is a breaking on-disk format
  change for any pre-Phase-2 database; migrations 0002/0003 upgrade
  existing rows automatically on open, but a row `0001` had already
  corrupted (invalid UTF-8 replaced with U+FFFD) is detected, not
  silently repaired: it now reads back `domain.ErrIntegrity` rather than
  the wrong value it held before. Operators restoring a pre-Phase-2 backup
  should expect this on any row a Phase 1 binary had already corrupted.
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
- DUR-1.2's fix is locked by two tests with different scopes:
  `TestCancelledUpdateRollsBack` (an ordinary pre-commit cancellation rolls
  back cleanly) and `TestCancellationAtCommitBoundaryReportsCommitted` (the
  actual race — cancellation lands exactly between the pre-commit check
  and `COMMIT` — deterministically, via a test-only `context.Context` whose
  `Err()` returns nil once after `arm()` and `context.Canceled` on every
  call after, rather than a timing-dependent probabilistic repro).

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
  - `TestMigrationChecksumMismatch` asserts a hand-altered *applied*
    migration fails `Open` rather than proceeding silently.
  - `TestCommittedMigrationsUnchanged` (Phase 2) asserts every *embedded*
    migration file's SHA-256 checksum matches the `committedMigrations` map
    pinned in this test, and that the embedded set has exactly that many
    files — the "committed migrations are never edited" rule from a
    checksum a database recorded on disk to a checksum the source tree
    itself pins.
  - `TestMigratedSchemaMatchesTypes` (Phase 2; renamed from
    `TestEmbeddedSchemaMatchesTypes`) asserts the typed-column schema,
    after all eleven migrations replay on a fresh database (SPEC-1.9:
    corrected from an earlier, stale "seven"), still matches every Go
    struct field exactly, locking the no-opaque-copy design above against
    every migration added since Phase 1, not only 0001.
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
- `internal/store/sqlite/lossless_test.go` (Phase 2, D3/R8):
  `TestLosslessPartsDecodeIsStrict` and
  `TestLosslessStringsRoundTripAndStrictDecode` assert the hex-string JSON
  form round-trips arbitrary bytes (including invalid UTF-8) exactly and
  that a malformed encoding fails decode rather than silently repairing;
  `TestLosslessUsageMatchesPlainJSON` (SPEC-1.9: description corrected
  below) asserts usage-iteration encoding (which holds no strings, only
  numbers/booleans/nulls) against an exact, alphabetically-key-ordered
  JSON literal, and separately that a legacy encoding with Go
  struct-declaration key order (what plain `encoding/json` actually
  produces) decodes to an equal value \u2014 it does not claim the lossless
  and plain-JSON *bytes* are identical, only that both forms decode to the
  same value for a type with no strings to corrupt.
- `internal/store/sqlite/upgrade_test.go` (Phase 2) is the migrated-layout
  suite: `openLegacy(t, upTo)` replays only the migrations up to a given
  version so a test can write a row exactly as an older binary stored it,
  then `.upgrade()` replays every remaining migration and asserts the
  result. `TestUpgradeLosslessParts` is the D3/R8 upgrade path for content
  parts: a valid legacy row survives with its `ContentHash` intact, while a
  row `0001` had already corrupted (its hash no longer matches its
  `\uFFFD`-repaired text) reads back `domain.ErrIntegrity`, both from
  `Item` and from `Items` over a filter that includes it, rather than a
  silently wrong value. **`TestUpgradeLosslessStringLists` has no such
  assertion (SPEC-1.9): a pre-0003 string list (tags, ID lists) that
  `encoding/json` had already corrupted with U+FFFD is preserved as
  stored and returned as-is \u2014 `verifyItemContent` (`read.go:16-18`) checks
  only `Parts` against `ContentHash`/`SemanticBytes`, so integrity
  verification for lossless leaf lists currently covers content parts
  only, not tags or ID lists.** `TestUpgradeProvenanceColumns` and
  `TestUpgradeCurrentNamespace` are the equivalent parity fixtures for
  migrations 0004 and 0005 (M8's pre-existing-record handling for item
  role/creation-turn/source-range/claim-name columns, and for the typed
  directive/agent-key namespace).
- `internal/store/sqlite/lookups_test.go` (R19; SPEC-1.9: these upgrade
  fixtures live here, not in `upgrade_test.go`, despite the `TestUpgrade*`
  name): `TestUpgradeItemBlobIndex` and `TestUpgradeDuplicateIndex` are
  the migrated-layout parity fixtures for migrations 0009 and 0010 \u2014 an
  item with blob parts stored before 0009 is found by `ItemsByBlob` after
  upgrade, and an item with a pre-0004 NULL role is returned by
  `DuplicateCandidates` after upgrade, once `f_role` reads as `''` rather
  than NULL; `TestUpgradeItemSourceIndex` is the same for migration 0011's
  Go step, confirming a pre-0011 item's source locator key is found by
  `ItemsBySourceKey` after upgrade. `TestItemsByBlobUsesIndex`,
  `TestDuplicateCandidatesUseIndex`, `TestUnresolvedReferencesByKeyUseIndex`,
  and `TestItemsBySourceKeyUsesIndex` are the `assertIndexed` checks
  above, one per new lookup, each asserting the query plan against the
  exact SQL the method issues.
- The full `internal/store/sqlite` package runs in about 5-6s under
  `go test -race ./... -count=1` (measured on this branch), consistent with
  a real SQLite file per test rather than a mocked backend.

### Round 1 additions (findings DUR-1.2, 1.4, 1.6, 1.7) — landed

`internal/store/sqlite`'s round-1 implementation is merged and passing:

- `internal/store/sqlite/dur_1_2_test.go:TestCancelledUpdateRollsBack`
  cancels the context from inside the transaction function and asserts an
  ordinary pre-commit cancellation rolls back cleanly.
  `TestCancellationAtCommitBoundaryReportsCommitted` (round 2) is the test
  that actually exercises DUR-1.2's fix: a test-only `context.Context`
  (`commitEdgeContext`) returns a nil error the first time `Err()` is
  called after `arm()`, then `context.Canceled` on every subsequent call —
  landing the cancellation deterministically between the pre-commit check
  and `COMMIT`, rather than relying on a timing-dependent probabilistic
  repro — and asserts the write is present afterward. Reverting
  `commitCtx := context.WithoutCancel(ctx)` back to `commitCtx := ctx`
  makes this test fail (an error is reported for the committed write);
  `TestCancelledUpdateRollsBack` alone does not catch that regression.
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

A regression found independently during verification, not one of the four
DUR findings above, is now also fixed: `TestConformance/DirectiveBoundaries`
— round-1 coverage for ADR 4's boundary-keyed directive identity — briefly
failed on `internal/store/sqlite` only (`CurrentDirective` reported a
different-session boundary as `ErrInvalidRecord` instead of `ErrNotFound`,
the AUTH-1.3 existence-disclosure pattern recurring in a new code path);
fixed in `a8e895f`. Passes on both stores now.

## Open questions

### Resolved at acceptance (2026-09-26)

`busy_timeout` (5s default, `WithBusyTimeout` to override) is decided in
code. None remaining for this ADR's original scope; all DUR findings and
the independently-found `DirectiveBoundaries` regression are fixed and
genuinely tested.

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
have a passing test that genuinely exercises the fix
(`TestScanCancellationIsOperationalError`, `TestConcurrentFirstOpen`,
`TestInterruptedMigrationReplays`, and — after the round-2 fix below —
`TestCancellationAtCommitBoundaryReportsCommitted`). One regression found
independently while verifying, `TestConformance/DirectiveBoundaries`, is
also fixed (`a8e895f`) and now passes on this package.

**Round 2 review** (PR #2; TEST — Claude Sonnet, `test-review-round2.md`,
finding TEST-2.3): `TestCancelledUpdateRollsBack` cancels synchronously
inside the transaction function, before the transaction reaches its commit
sequence — the pre-commit `ctx.Err()` check catches it, so the
`context.WithoutCancel`-guarded commit path DUR-1.2 actually fixed was
never exercised; confirmed by reverting the fix in a throwaway clone, where
the whole package, including this test, still passed. Fixed:
`TestCancellationAtCommitBoundaryReportsCommitted` uses a deterministic
test-only context (`commitEdgeContext`, arms to report not-yet-cancelled
exactly once, then cancelled on every later call) to land the cancellation
in the exact window DUR-1.2 closes, without a probabilistic repro — I
independently reconfirmed it fails when `context.WithoutCancel` is
reverted. Finding closed.
