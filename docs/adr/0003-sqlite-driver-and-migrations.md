# 3. SQLite driver and migration mechanism

Status: Accepted (2026-09-26, Phase 1 exit; decision unchanged by review rounds 1-3 of PR #2)
Date: 2026-09-25

## Amended in Phase 3 (ADR 8, 2026-09-26; reconciled against integration head `fc87199`)

Phase 3 (worker W2, `internal/store`) adds thirty-one forward migrations,
0018 through 0048 (K1 corrects the prior "thirty ... 0018 through 0047",
itself SPEC-4.4/DUR-4.10's correction of a stale "twenty-seven ... 0018
through 0044": 0018-0028 from the initial Phase 3 merge; 0029-0034 fixing
PR #6 round-1 review findings; 0035-0040 fixing round-2 findings;
0041-0044 fixing round-3 J1-J7 findings; 0045-0047 fixing round-3
DUR-3.1/DUR-3.2 findings; 0048 landing the commander's FROZEN K1 ruling
in round 4), after this ADR's Phase 2 migrations (0001 unchanged, per
this ADR's own rule). The full record/column/index manifest is
`docs/phase3-schema-manifest.md` (P3-41); this section records the
migration list itself and its upgrade-parity tests, matching how this ADR
already tracks 0001-0017 above.

**Pre-release exception, 0021 (SPEC-2.5, DUR-2.9).** `0021_phase3_declarations.sql`
was edited in place at `d7e8c13` (DUR-1.10's `SELECT DISTINCT` fix), with its
checksum pin updated in `durability_test.go` in the same commit. This is
otherwise exactly what this ADR's own "never edit a committed migration"
rule (below, enforced by `TestCommittedMigrationsUnchanged`'s pinned-checksum
map, not only by `TestMigrationChecksumMismatch`'s runtime check) forbids; it
is accepted only because no release has shipped Phase 3 migrations yet, per
H6/DUR-2.9 below.

**Unreleased-database exception (H6, DUR-2.9).** A SQLite database created
against any pre-`914afef` Phase 3 development head cannot reopen against
this migration list: 0021's edit above changes its checksum, and 0030's
uniqueness index (`observation_run_closing`) can reject a database whose
services (at the `c22a53c` PR #6 round-1 head) accepted more than one
closing observation for a run before the G1 fix landed. Both are accepted,
undocumented-until-now consequences of iterating on unreleased Phase 3
migrations, not upgrade-path defects: **no Phase 3 database predating this
head is supported.** `internal/tools/execute.go`'s `dispatchedRequest` hash
input changed in this same window with no schema-version dispatch, so a
tool receipt written before that change also conflicts on retry — the same
exception covers it. None of this affects the frozen Phase 2 fixture
(`TestPhase2FixtureReplay`), which predates every Phase 3 migration.
W2's own `internal/store/sqlite/CONFORMANCE_NOTES.md` (PR #6 round 2)
records this same exception, naming 0021's root cause precisely: its
legacy grant backfill inserted duplicate `TargetIDs` twice and failed
before any later migration could run. **One further consequence within
the supported range (DUR-3.10, PR #6 round 3):** `lifecycle.replayReplacement`
(`replace.go:247`) accepts either a 3-ID or a 4-ID replacement receipt and
returns `GrantID=""` for a 3-ID one. A replacement receipt written exactly
at `914afef`, before the 4-ID (grant-inclusive) receipt shape existed,
replays correctly with no grant — this is the old format's honest
absence of a grant, not data loss, and is unaffected by the unreleased-
database exception above (`914afef` is the supported boundary, not
excluded by it).

- `0018_phase3_row_fields.sql` — Phase 3 fields on existing record tables
  (P3-3/5/6/12/13/35/40/41): item `Namespace`, decoded grant `Targets`, and
  the flattened lifecycle-command-result/obligation-binding columns. Every
  new column reads NULL on a pre-Phase-3 row, decoded as that field's zero
  value, which the domain treats as the frozen legacy form (namespace ""
  is the pre-Phase-3 directive/agent-key fallback; nil `Targets` leaves the
  legacy `TargetIDs` path in force).
- `0019_command_execution_result.sql` — stores a lifecycle command's
  execution result whole (P3-35), correcting 0018's per-field flattening,
  which collided `Result.Before.Version` with `BeforeVersion` on one column
  name.
- `0020_phase3_membership.sql` — coverage, logical membership, checkpoint,
  owner, and request-receipt companions (P3-2/6/7/24/27/32): one typed
  `rec_*` table per companion, keyed `(session, ID)` like every earlier
  record.
- `0021_phase3_declarations.sql` — creation/snapshot declarations, semantic
  change records, and the indexed grant/audit reads (P3-3/4/5/36/39/41). A
  creation declaration is keyed by its item, one per item. **At 0021 alone,
  absence was unknown identity, never backfilled; 0034 below reconciles a
  known declaration for every pre-upgrade keyed item where the ingest
  receipt snapshot establishes its creation identity, and records unknown
  (non-executable) otherwise (SPEC-2.5, corrects P3-41's original text).**
- `0022_phase3_resources.sql` — resource registration/reporting, per-path
  content, workspace bindings, pre-execution runs, typed observations, and
  subject state (P3-19..22/41). Nothing is backfilled: no migration guesses
  a repository, baseline fingerprint, reporter, binding, or run order.
- `0023_phase3_proofs.sql` — obligation declarations, applicability proofs
  and their dependencies, assertions, and transition details, with the
  indexed reads completion/invalidation need (P3-9/12..18/23/41). No legacy
  obligation gains a declaration, proof, assertion mode, or dependency.
- `0024_phase3_retrieval.sql` — retrieval leases, results, projections, and
  events (P3-28..30/41). No legacy content gains a lease or admission, and
  no item's residency changes.
- `0025_phase3_gc.sql` — GC requests, collect receipts/results, and the
  indexed reads collection/completion need (P3-9/38/39/41). No request,
  receipt, or result is invented for earlier data.
- `0026_reconcile_legacy_matcher_satisfaction.sql` — the checksum-pinned Go
  step (`steps_0026.go`, `reconcileMatcherSatisfactionV1`) that returns each
  Phase 2 obligation version SATISFIED by a matcher transition, with no
  applicability proof a Phase 3 binary can establish, to UNRESOLVED exactly
  once, through an audited SYSTEM `UPGRADE_RECONCILIATION` transition that
  preserves original history (P3-41, ADR 8's residual-risk-adjacent
  legacy-treatment rule). Tests:
  `TestUpgradeReconcilesLegacyMatcherSatisfaction`,
  `TestInterruptedReconciliationRollsBack`.
- `0027_current_version_observation_namespace.sql` — admits every domain
  namespace, including OBSERVATION, in the current-version key's CHECK
  constraint (P3-3/22); SQLite cannot alter a CHECK, so the table is rebuilt
  with the same columns/key and every existing pointer copied unchanged.
- `0028_phase3_policy_gc_triggers.sql` — the explicit enabled GC-trigger set
  of the recorded Phase 3 policy (P3-38/39, ADR 16's amendment): a row
  without a recorded Phase 3 policy (every Phase 2 envelope/receipt) is
  unaffected, and no trigger set is backfilled onto it.
- `0029_observation_run_ordinal.sql` (PR #6 round 1, SEC-1.13) — a unique
  index on `rec_observation_run(session_id, f_subject_key, f_ordinal)`: two
  runs of one subject can never share an ordinal, which would make run
  order, and so the subject watermark (ADR 8 §12), ambiguous. No Phase 2
  database has runs, so the index builds over an empty or already-unique
  table.
- `0030_observation_run_closes_once.sql` (PR #6 round 1, G1/DUR-1.1) — a
  unique index enforcing at most one closing observation (a complete
  PASS/FAIL, or an ERROR/TIMEOUT/CANCELLED) per run, matching `store.ClosesRun`
  (`internal/store/semantic_resource.go`; SQLite's SQL form is
  `closingObservation`, `sqlite/semantic_resource.go:51` — not, as this
  bullet previously said, a predicate in `internal/obligation`, PR #6
  round 3, SPEC-3.8/DUR-3.10). **Consequence
  (H6, DUR-2.9): a database that accepted more than one closing observation
  per run under an earlier, pre-G1-fix service version cannot reopen
  against this index; no Phase 3 database predating `914afef` is supported
  (see the unreleased-database exception above).**
- `0031_grant_target_liveness.sql` (PR #6 round 1, G2/SEC-1.5/DUR-1.4) —
  each grant-target index row carries its grant's revocation and expiry
  sequence (0 for none), backfilled from `rec_grant` and kept current by
  `RevokeGrant`, so a live-grant read serves the unrevoked range without
  visiting revoked or expired history.
- `0032_subject_state_live_index.sql` (PR #6 round 1, G2/SEC-1.8/DUR-1.2) —
  a partial index in first-filing order, originally meant to hold exactly
  the CURRENT subject states so a resource report's invalidation work
  never costs STALE/UNKNOWN history. **Stale since DUR-3.1 (B), corrected
  here (DUR-4.7): `PutSubjectState` now only ever writes
  `ApplicabilityCurrent` (`internal/obligation/subject_state.go`), so this
  index holds every filed state, not exactly the current ones — "a state
  enters and leaves the index as its applicability changes" no longer
  happens.** Current applicability is instead read through
  `obligation.Service.SubjectApplicability` (ADR 8's DUR-3.1 (B)), derived
  at read time from the authoritative resource state; neither this index
  nor `store.SubjectStatesByResource`'s equivalent "only CURRENT states"
  filter has a production caller today (SEC-4.11/SPEC-4.10/DUR-4.7) — both
  are recorded as an explicit Phase 4 deferral in ADR 8, not removed here.
- `0033_resource_update_paths.sql` (PR #6 round 1, G2/SEC-1.7/DUR-1.2) — an
  index of resource updates by the paths they may affect (a path or one of
  its ancestor directories, plus every ALL-paths/UNKNOWN update), so a
  path's currency check never walks unrelated history. **Resolved (DUR-2.2 /
  SEC-2.5 / XREV-2.2, PR #6 round 2, corrects a round-2 doc error repeated
  in DUR-3.10):** `internal/obligation`'s `currentPathState` now calls
  `LatestResourceUpdateAffectingPath` (`resource.go:266`, commit `5b96fce`),
  the exact-key keyed form of this index, so the intended cost bound is
  realized in production, not only in `storetest`. The *paged* form,
  `ResourceUpdatesAffectingPath`, and the unrelated `LifecycleEvent(id)`
  exact read are what still have no production caller as of this pass.
- `0034_reconcile_legacy_creation.sql` (PR #6 round 1, G5/SPEC-1.3/FROZEN
  C-1, P3-4/41) — the checksum-pinned Go step
  (`steps_0034.go`, `reconcileLegacyCreationV1`) that reconciles a creation
  declaration for every pre-upgrade keyed item stored without an explicit
  namespace, exactly as the corrected 0021 bullet above describes; atomic
  with its own version row, idempotent, and deterministic. Tests:
  `internal/store/sqlite`'s `TestUpgradeReconcilesLegacyCreation`;
  `internal/ingest`'s `TestLegacyRestatementDedupsAfterUpgrade` and
  `TestUpgradeRestatesEveryCurrentDirective_G5`.
- `0035_item_exchange_index.sql` (PR #6 round 2, H2/SPEC-2.7) — an item's
  exchange within a conversation, by ordinal: `EarliestExchangeWithItem`
  answers which exchange first holds an item with one keyed `LIMIT 1`
  search, independent of the conversation's length (closing the
  checkpoint-coverage-lookup cost growth SPEC-2.7 found). A member's
  exchange row is written when the member is inserted; pre-migration rows
  are backfilled from the member/exchange tables.
- `0036_grant_target_liveness_ranges.sql` (PR #6 round 2, H2/DUR-2.10) —
  replaces 0031's single OR-based index (which still scanned and sorted
  every revoked/expired row) with three disjoint live-grant ranges
  `LiveGrantsFor` reads directly, so the cost is the live grants, not the
  target's whole history.
- `0037_subject_high_water.sql` (PR #6 round 2, H1/SEC-2.1/SPEC-2.1/DUR-2.1)
  — the per-`(subject, task, access)`-partition high-water mark (ADR 8 §6):
  the highest run ordinal with a complete PASS or FAIL, raised by every
  such observation whatever its fingerprint or applicability.
- `0038_current_workspace_binding.sql` (PR #6 round 2, H2) — one row per
  workspace binding ID at its latest version, in that version's context
  (ADR 8 §10): a page counts live bindings, not historical versions, and a
  rebind moves the row to the new context, retiring it from the old one.
- `0039_gc_result_outcome.sql` (PR #6 round 2, H3/SEC-2.4/SPEC-2.4/DUR-2.7)
  — `GCResult` gains a closed `Outcome` (`COLLECTED`/`FAILED`) and failure
  `Reason`; every pre-migration result is backfilled `COLLECTED` with no
  reason, since every such result already linked a collect receipt.
- `0040_gc_progress.sql` (PR #6 round 2, H3) — one CAS-written row per GC
  request holding the durable `(Seq, ID)` candidate cursor, completed
  batches, and attempts; operational metadata only, never a substitute for
  a batch's collect receipt or the request's result, and carries no
  semantic sequence. **Correction (DUR-3.10, PR #6 round 3):** the row is
  not removed once the request reaches a terminal outcome — it stays
  alongside the request's result, not only "per pending" request as this
  bullet previously said.
- `0041_gc_snapshot.sql` (PR #6 round 3, J2/SPEC-3.6) — `GCProgress` gains
  `SnapshotSeq`, the eligibility ceiling the request's first batch pins;
  later batches traverse only candidates at or before it, so a moving
  target set can never be re-evaluated mid-request.
- `0042_gc_batch_size.sql` (PR #6 round 3, J3/XREV-3.2) — `GCProgress`
  gains `BatchSize`, the durable adaptive item-count bound that halves
  (floor one) on transaction-budget exhaustion, so a receipt is never
  sized by an incomplete object.
- `0043_gc_item_attempts.sql` (PR #6 round 3, J4/SEC-3.1/SPEC-3.3) —
  `GCProgress` gains `ItemAttempts`, counting attempts against the
  specific unprocessed next candidate, not the request as a whole.
- `0044_gc_retry_item.sql` (PR #6 round 3, J4/SPEC-3.3) — `GCProgress`
  gains `ItemAttemptID`, so a retry's attempt count is tied to the exact
  candidate even if another operation archives the previously failing one
  between batches.
- `0045_live_proof_paths.sql` (PR #6 round 3, DUR-3.1; SPEC-4.4/DUR-4.10:
  previously missing from this list) — `lookup_live_proof_path` files each
  live proof's `CURRENT_PATH` dependency under its exact path plus the hex
  of every ancestor directory, and each `WORKSPACE` dependency under
  `"ws"`, so a resource report reads only the proofs it can actually
  affect (ADR 8's DUR-3.1 (A)); `lookup_live_dependents` counts live
  non-`FIXED_CONTENT` dependency rows per resource, the policy cap ADR 8's
  DUR-3.1 (C) validates against (superseded by the commander's FROZEN K1
  ruling, ADR 8 K1e, once K1 lands). The frozen Go step
  `reconcileLiveProofPathsV1` (`steps_0045.go`, registered as
  `"0045/proofs/reconcile-live-proof-paths-v1"` in `steps.go`) rebuilds
  `lookup_live_dependency` and fills both new tables from the live proofs.
- `0046_policy_max_live_proof_dependents.sql` (PR #6 round 3, DUR-3.1;
  SPEC-4.4/DUR-4.10) — adds `Phase3Policy.MaxLiveProofDependents` to
  `rec_envelope`/`rec_receipt`, backfilled with the largest value each
  recorded policy's own work budget allows, capped at the default 256, so
  historical envelopes and receipts still validate and replay verbatim
  (P3-38). **DUR-4.6, resolved as a side effect of K1 A6 (round 4), not
  by fixing the backfill:** a recorded policy with `MaxTransactionWork <
  10` still backfills `MaxLiveProofDependents` to 0, but
  `Phase3Policy.Validate` (`internal/domain/semantic.go`) no longer
  validates that field at all once K1 lands (below), so the
  previously-rejecting 0 value is never checked and the exact-retry
  regression this bullet originally described cannot occur.
- `0047_gc_queue.sql` (PR #6 round 3, DUR-3.2; SPEC-4.4/DUR-4.10) —
  `lookup_pending_gc_trigger` indexes pending GC requests by trigger,
  backfilled from `lookup_pending_gc`, so a collector reading its enabled
  triggers never pages a disabled trigger's requests; `gc_queue_cursor` is
  each session's CAS-written, durable scan position, replacing the
  in-process `gcQueueCursors` `sync.Map` a new service instance or a
  restart used to reset.
- `0048_k1_pointers.sql` (PR #6 round 3 commander ruling K1, landed round
  4) — the write-time validity pointers ADR 8's K1 section (A1) derives
  proof validity from, an audit cursor, and a live-proof index:
  `lookup_workspace_divergence` (per resource, each raise's revision and
  causing update ID: lost freshness or a changed workspace fingerprint);
  `lookup_affecting_raise` (per resource/key — the `"all"` key for
  UNKNOWN/ALL-paths reports, else `"path:"` plus the hex of a changed
  path, exactly migration 0033's keys); `lookup_live_proof` (every live
  proof, in `(Seq, ID)` order, for the SYSTEM async settlement worker);
  `settlement_cursor` (each session's CAS-written audit scan position,
  unsequenced operational state like `gc_queue_cursor`, never evidence a
  proof was settled). The frozen Go step
  `reconcileK1PointersV1` (`steps_0048.go`, registered
  `"0048/k1/reconcile-workspace-divergence-v1"`) backfills
  `lookup_workspace_divergence` exactly, walking each resource's updates
  in revision order and raising on `Freshness == UNKNOWN` or a changed
  fingerprint from the previous report's (the first report's fingerprint
  always counts as a change). The migration's own plain SQL backfills the
  other two raise tables conservatively rather than exactly: **the ALL
  key from every UNKNOWN or all-paths report, and every recorded
  `ChangedPath` of every stored report** — reports' same-content history
  is not reconstructible, so a backfilled raise can settle a proof a live
  report would have spared (an accepted, one-time-upgrade
  overapproximation). **A KNOWN report after UNKNOWN raises divergence**
  through the same general rule as any fingerprint change: going UNKNOWN
  clears the resource's stored fingerprint, so the next KNOWN report's
  fingerprint (never empty) always differs from it, with no special-case
  code needed. Migration 0045's `lookup_live_proof_path`/
  `lookup_live_dependents` tables are unaffected and unused by any of
  this: they stay maintained only as an unused write-time metric (K1d),
  since 0048 introduces its own dedicated pointers rather than reusing
  them. Tests: `TestK1ReportsNeverFanOut`, `TestK1ValidityIsMonotone`,
  `TestK1DependencySemantics`, `TestConformance/SemanticProofDerivedValid`,
  `TestConformance/SemanticA5CommitGuard` (storetest). **Gap, not yet
  fixed as of this pass:** unlike 0045-0047, this migration has no
  dedicated `TestUpgrade*` fixture confirming the backfill against a
  real pre-0048 database — only its checksum and Go-step identity are
  pinned (`TestCommittedMigrationsUnchanged`, `TestCommittedStepsUnchanged`).

**Tests that lock this list (all in `internal/store/sqlite`, extending this
ADR's existing migration-checksum/upgrade discipline):**
`TestMigrationChecksumCoversStep`, `TestCommittedMigrationsUnchanged`,
`TestMigratedSchemaMatchesTypes`, `TestMigrationChecksumMismatch`,
`TestInterruptedMigrationReplays`, `TestUpgradePhase3RowFields`,
`TestUpgradeGrantTargetIndex`, `TestUpgradeItemExchangeIndex`,
`TestUpgradeSubjectHighWater`, `TestUpgradeCurrentWorkspaceBindings`,
`TestUpgradeGCResultOutcome`, `TestH2LatestReadsAreKeyed`, and
`TestLiveGrantRangesSkipDeadRows` (PR #6 round 3, DUR-3.10: these six were
missing from this list). **`TestUpgradeLiveProofPaths`,
`TestUpgradePolicyMaxLiveProofDependents`, and
`TestUpgradePendingGCByTrigger` (0045-0047's own upgrade-parity fixtures),
plus `TestCursorPagesSeekRange` and `TestLiveProofPathReadsSeek`
(keyset-cursor seeks over the new indexes) and
`TestLatestBindingVersionIsKeyed` (DUR-3.7) were also missing from this
list (DUR-4.10).** `internal/obligation`'s own SQLite suite
(50/50 subtests, 8/8 failure-injection scenarios, ADR 8) runs against these
migrations through W2's `sqlitetest` template.

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
- **Phase 2 reality: migrations 0002-0014, checksum pinning as a test,
  lossless leaf-list encoding, and a superseded-then-dropped lookup
  generation as a worked example of "forward-only, never edited" (D3, R8;
  M1, M5, M6, D10, D13, D14/D16, R19, F1/F5 via ADR 19).** Thirteen
  migrations have landed on top of 0001:
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
    occurrence-derived ID, so a References entry that matched no ingested
    item at parse time survives restart for later linking (M5, R2, R18;
    `domain.UnresolvedReference`);
  - `0009_item_blob_index.sql`, `0010_item_duplicate_index.sql`, and
    `0011_item_source_index.sql` added the first generation of R19's
    bounded lookups — `item_blobs`, an index on
    `rec_item(content_hash, task, section, role, authority, access
    boundary)`, and `item_sources` respectively, the last via a **Go
    migration step** (`backfillItemSourcesV1`) run inside the same
    transaction as the SQL. **This generation was superseded one review
    round later and no longer exists** (below); it is kept in this list
    because a committed migration is a historical fact this ADR's rule
    forbids un-writing, not because its tables still exist in a fresh
    database.
  - `0012_access_filtered_lookups.sql` (F1: SEC-1.1, SEC-1.2, DUR-1.1,
    SPEC-1.3) replaced 0009-0011's design with access-filtered lookup
    tables carrying each item's owner columns (`workflow_id`, `task_id`,
    `agent_id`) directly, so a viewer's access filter is an index equality
    probe applied *inside* the query, before any limit — the fix for the
    finding that R19's three lookups were properly indexed but returned
    every matching row in the *session*, regardless of whether the caller
    could see it (SEC-1.1/SEC-1.2): `lookup_blob` (blob → item, replacing
    `item_blobs`), `lookup_canonical` (duplicate/current-version
    candidates, replacing the `item_duplicate` index — live items only, no
    incoming `SUPERSEDES`, no outgoing `DUPLICATE_OF`), `lookup_working`
    (a `lookup_canonical` subset for `WORKING` sections), and
    `lookup_source` (locator → item, replacing `item_sources`), plus a
    `relationship_to` index on `rec_relationship(type, to_id)` and a
    `reference_visible` index on `rec_reference` adding the owner columns.
    Backfilled from 0009-0011's tables, which still held data at 0012's
    replay point. The store deletes an item's rows from
    `lookup_canonical`/`lookup_working`/`lookup_source` in the same write
    that retires it (`SUPERSEDES`/`DUPLICATE_OF`), so repeated identical
    content never grows these three tables (DUR-1.1). **(SPEC-3.3)
    `lookup_blob` does not follow the same rule:** its row is dropped only
    when the retiring item is itself a duplicate, not on an ordinary
    supersession (the canonical item's content and boundary still
    authorize the same blob references), and 0012's `lookup_blob` backfill
    carries forward even a pre-existing duplicate's row, with no
    `DUPLICATE_OF` exclusion — unlike the other three tables' backfills.
  - `0013_drop_pre_f1_lookups.sql` drops `item_blobs`, `item_sources`, the
    `item_duplicate` index, and the `reference_locator` index once 0012
    has carried their data forward — "nothing reads or writes these"
    (the migration's own comment). `TestLegacyLookupsDropped` asserts all
    four are gone from a fresh database.
  - `0014_receipt_max_reference_links.sql` adds
    `rec_receipt.f_versions_limits_max_reference_links`, so a receipt
    records the `domain.Limits.MaxReferenceLinks` budget an event's
    References edges were checked against (D14/D17); a pre-0014 receipt
    reads NULL as 0 (unrecorded), never backfilled with the current
    default (M8). `TestUpgradeReceiptLimits`
    (`internal/store/sqlite/upgrade_test.go`, SPEC-2.6: previously
    uncited) locks the pre-0014-reads-as-0 upgrade path.
  - `0015_ordered_graph_indexes.sql` (SPEC-2.1, p2-store) adds
    `relationship_from_seq`/`relationship_to_seq` on
    `rec_relationship(session_id, f_type, f_from_id|f_to_id, f_seq, id)`
    and `item_task_seq` on `rec_item(session_id, f_task_id, f_seq, id)`,
    dropping the key-only `relationship_from`/`relationship_to`/`item_task`
    indexes they supersede — see "The access-filtered lookup API" below
    for why the key-only indexes were not enough on their own.
    `TestUpgradeOrderedGraphIndexes` (SPEC-3.2, previously missing) is the
    migrated-layout parity fixture: a relationship and items stored before
    0015 read back correctly through the new indexes after upgrade, and
    the three superseded indexes are confirmed gone.
  - `0016_command_detail_access.sql` (SEC-3.2, recorded in ADR 19 §31) adds
    five `f_detail_access_*` columns to `rec_command`
    (`scope`/`session_id`/`workflow_id`/`task_id`/`agent_id`), the boundary
    that narrows who may read a lifecycle command record's resolution
    while the record itself stays readable at its transcript boundary.
    **Compatibility rule (M8):** a record written before 0016 reads all
    five columns as NULL, decoded as the zero `domain.AccessBoundary`,
    which the record's `Redacted` method treats as "no narrowing beyond
    `Access`" — a pre-existing record's resolution stays exactly as
    visible as it was when recorded, never retroactively hidden or
    exposed by the migration itself.
  - `0017_lookup_item_indexes.sql` (SPEC-3.1 item 1) adds a `(session_id,
    item_id)` index to each of `lookup_canonical`/`lookup_working`/
    `lookup_source`/`lookup_blob`. Every `SUPERSEDES`/`DUPLICATE_OF` edge
    deletes the retired item's rows from these tables by item ID, but
    their primary keys start with the lookup key, not the item ID, so that
    `DELETE` searched the whole session before this index existed. Test:
    `TestRetireLookupsUseIndex` (`internal/store/sqlite/access_lookups_test.go`).

  **The access-filtered lookup API (F1), landed:** `store.BlobReferrer`,
  `CanonicalCandidates`, `CurrentWorking`, and `SourceItems`
  (`internal/store/lookups.go`, `internal/store/sqlite/access_lookups.go`)
  each take a `Viewer domain.Principal` and return a `Lookup{Items,
  Unverified, More, Next}`: `Items` holds verified visible matches in
  `(Seq, ID)` order; `Unverified` names a visible match whose stored
  content failed verification (DUR-1.4 — a legacy row `0001` altered
  before the lossless fix) so it is excluded from candidates without
  failing the lookup or blocking unrelated identical content, while
  reading it directly still fails `domain.ErrIntegrity`
  (`TestLegacyUnverifiedNeverBlocks`); `More`/`Next` page a bounded read.
  `store.VisibleReferences` follows the same shape for References.
  `store.PermittedOwners` turns `domain.AccessBoundary.Permits` into the
  indexed equality clause every lookup issues. `TestAccessLookupsUseIndex`
  asserts each lookup's `EXPLAIN QUERY PLAN` searches an index whose
  constraint covers every key column the lookup filters on, never merely
  an unindexed scan or a session-prefix-only `SEARCH` (SPEC-2.1: `assertIndexed`
  was strengthened, below, to take the expected key columns and require
  them in the plan's constraint — it no longer passes on a plan that
  narrows only by `session_id`); `TestUpgradeAccessLookups`
  checks the 0012 backfill end to end (a canonical item, a blob referrer,
  and a sourced item, each found post-upgrade); `TestUpgradeItemBlobIndex`/
  `TestUpgradeDuplicateIndex`/`TestUpgradeItemSourceIndex` still exist and
  now assert the compound upgrade path (e.g. "migration 0009 then 0012")
  reaches the same correct result through the new API.

  SPEC-1.3 separately found that *other* per-item graph/ingest reads —
  `Relationships` filtered by type/from/to, and `Items` filtered by task —
  were not indexed even though R19's three named lookups were. The
  key-only indexes behind them did not actually close this (SPEC-2.1: they
  carried the key but not the `(Seq, ID)` order, so SQLite preferred a
  session-wide order index to avoid a sort — each read still grew with the
  session, and `TestGraphReadsUseIndex`'s plan guard could not yet catch it,
  since it only rejected an unindexed `SCAN`, not a session-prefix `SEARCH`).
  **(SPEC-3.3, corrected) Only `relationship_to` is from 0012 — `relationship_from`
  and `item_task` are 0001_init.sql originals**, so this gap predates 0012
  and was never actually about 0012's own additions.
  **`0015_ordered_graph_indexes.sql` (SPEC-2.1, p2-store) fixes this
  properly:** `relationship_from_seq`/`relationship_to_seq` on
  `rec_relationship(session_id, f_type, f_from_id|f_to_id, f_seq, id)` and
  `item_task_seq` on `rec_item(session_id, f_task_id, f_seq, id)` carry both
  the key and the order in one index, dropping the three key-only indexes
  they supersede (`relationship_from`/`item_task` from 0001,
  `relationship_to` from 0012); a
  strengthened plan guard now requires the exact key columns in the index's
  search constraint, not merely that some index is used
  (`internal/store/sqlite/lookups_test.go`'s `assertIndexed`,
  `TestGraphReadsUseIndex`). Obligation reads (`Obligation`,
  `ObligationVersions`) were also found quadratic in the same review round —
  `read.go` decoded every obligation in the session per lookup — and are
  now read by `(session_id, id)` primary key (`obligationVersionsQuery`),
  with `ObligationsBySource` going through the same builder over the 0006
  index. `InsertRelationship`'s supersession-cycle check no longer walks
  every `SUPERSEDES` edge in the session either: a cycle through `from ->
  to` needs an existing edge into `from` (one indexed read, or none), and
  otherwise the check walks only the chain reachable from `to`.
  **Still an unfiltered whole-session read, deliberately deferred**
  (SPEC-4.2: corrected — `tx.Grants()` is the one exception that *does*
  run on the per-item ingest path, deferred anyway; the other four
  genuinely don't): `tx.Grants()` (obligation retirement, lifecycle
  command authorization — runs once per replaced item with a bound
  obligation and once per lifecycle command; a `MutationGrant` can only be
  created by an authorized issuer, so the read discloses nothing and only
  costs time), `ObligationTransitions`, `LifecycleEvents`,
  `Obligations(taskID)` (the whole-task listing, distinct from the
  now-keyed per-ID/per-source reads above), and
  `Diagnostics`/`LifecycleCommands` when called with no `OccurrenceID`
  (`visibleReceipts` then lists every receipt) — these last four, unlike
  `Grants()`, never run during ingestion itself (ADR 19 §13 records the
  ruling in full).

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
  repair. Decoding the wire format itself is strict: an unknown/missing
  field, invalid hex, a wrong JSON type, or trailing data fails as
  `domain.ErrIntegrity` at the caller rather than a value being invented,
  so extending a listed type needs a forward migration (M8), not a decoder
  that silently accepts old and new shapes alike. Migrations 0002/0003
  rewrote every row stored in the old plain-JSON form; a row `0001` had
  already corrupted a *content part* (its hash no longer matches its
  now-`\uFFFD`-repaired text) reads back as `domain.ErrIntegrity` after
  upgrade rather than a silently wrong value, matching M8's "no invented
  executable state for a
  record that predates new metadata." **(SPEC-2.6) This content-hash check
  is content-part-specific** (`verifyItemContent`, below): a lossless
  tag/ID list corrupted the same way decodes cleanly (the wire format
  itself is intact) and is returned as-is with no `ErrIntegrity`, since
  nothing compares it against a separate hash — see
  `TestUpgradeLosslessStringLists` below.
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
  corrupted a *content part* (invalid UTF-8 replaced with U+FFFD) is
  detected, not silently repaired: it now reads back `domain.ErrIntegrity`
  rather than the wrong value it held before. **(SPEC-3.6) This detection
  is content-part-specific, not general** — see "The access-filtered
  lookup API" above: a corrupted tag or ID list decodes cleanly and is
  returned as-is, with no `ErrIntegrity`, since nothing compares it
  against a separate hash. Operators restoring a pre-Phase-2 backup
  should expect the `ErrIntegrity` detection only for a row whose
  corruption was in a content part, and a silently-preserved-as-is old
  value for one whose corruption was in a tag or ID list.
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
    after all forty-eight migrations replay on a fresh database (K1:
    corrected from a stale "forty-seven," itself PR #6 round 4's
    SPEC-4.4/DUR-4.10 correction of a stale "forty-four," which
    was this ADR's own count before 0045-0047 landed; PR #6 round 3,
    SPEC-3.8/DUR-3.10 had corrected a stale "seventeen," which
    was Phase 2's own count before Phase 3's 0018-0044 landed; PR #5
    round 4's own SPEC-4.5 (a different PR's numbering, not to be confused
    with PR #6's) had corrected that "seventeen" from a stale "fifteen" once 0016 and
    0017 landed; SPEC-3.6 had already corrected that from a stale
    "fourteen" once 0015 landed, itself correcting an earlier stale
    "eleven", itself
    corrected from a
    stale "seven"), still matches every Go
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
  produces) decodes to an equal value — it does not claim the lossless
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
  stored and returned as-is — `verifyItemContent` (`read.go:16-18`) checks
  only `Parts` against `ContentHash`/`SemanticBytes`, so integrity
  verification for lossless leaf lists currently covers content parts
  only, not tags or ID lists.** `TestUpgradeProvenanceColumns` and
  `TestUpgradeCurrentNamespace` are the equivalent parity fixtures for
  migrations 0004 and 0005 (M8's pre-existing-record handling for item
  role/creation-turn/source-range/claim-name columns, and for the typed
  directive/agent-key namespace).
- `internal/store/sqlite/lookups_test.go` (R19, then F1; these upgrade
  fixtures live here, not in `upgrade_test.go`, despite the `TestUpgrade*`
  name): `TestUpgradeItemBlobIndex`, `TestUpgradeDuplicateIndex`, and
  `TestUpgradeItemSourceIndex` are the migrated-layout parity fixtures for
  the now-superseded 0009/0010/0011 generation — an item with blob parts
  stored before 0009 is found by `tx.BlobReferrer` after the full replay
  ("migration 0009 then 0012," each test's own phrasing), an item with a
  pre-0004 NULL role is returned by `tx.CanonicalCandidates` once `f_role`
  reads as `''` rather than NULL, and a pre-0011 item's source locator key
  is found by `tx.SourceItems` — all three exercise the compound upgrade
  path through today's API, not the deleted 0009-0011 methods.
  `TestLegacyLookupsDropped` asserts `item_blobs`, `item_sources`, the
  `item_duplicate` index, and the `reference_locator` index are all gone
  from a fresh database.
- `internal/store/sqlite/access_lookups_test.go` (F1: SEC-1.1, SEC-1.2,
  DUR-1.1, DUR-1.4, SPEC-1.3): `TestAccessLookupsUseIndex` asserts every
  access-filtered lookup's `EXPLAIN QUERY PLAN` searches an index whose
  constraint covers every expected key column (`lookup_blob`,
  `lookup_canonical`, `lookup_working`, `lookup_source`, `rec_reference` by
  locator key, `rec_relationship` by type/target) — SPEC-2.1 found the
  original version only checked that *some* index was used, which a
  same-session-prefix scan would also pass; `assertIndexed` now takes the
  expected keys and requires a `SEARCH` step whose parenthesized
  constraint contains each one, closing that hole for both this test and
  `TestGraphReadsUseIndex` (below); `TestUpgradeAccessLookups`
  checks migration 0012's backfill end to end — a canonical item, a blob
  referrer, and a sourced item are each found through the new API after
  upgrade, while a `DUPLICATE_OF` item is not; `TestLegacyUnverifiedNeverBlocks`
  is the DUR-1.4 regression: a legacy row `0001` altered is reported in
  `Lookup.Unverified`, excluded from `Items`, and does not block identical
  new content from finding its own canonical item, while `tx.Item` on it
  directly still fails `domain.ErrIntegrity`; `TestGraphReadsUseIndex`
  locks the SPEC-1.3 fix for `Relationships`/`Items` reads issued once per
  ingested item, separate from the four named F1 lookups.
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
