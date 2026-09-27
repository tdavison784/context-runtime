# SQLite store notes

## Missing records and compare-and-swap

The store contract's general compare-and-swap paragraph says a missing record
reads as version 0, so an update with an expected value above 0 conflicts.
The conformance suite and memory store instead return `ErrNotFound` for
update-only methods (`UpdateItem`, `UpdateObligationVersion`, and
`UpdateCall`) when their target is missing. SQLite follows the suite.

Recommendation: limit the version-0 wording to the create-or-replace methods
(`PutTask` and `PutConversation`), and state explicitly that update-only
methods return `ErrNotFound` for a missing target.

## Phase 3 development databases (H6, DUR-2.9)

Databases written by a Phase 3 development head before `914afef` (the PR #6
round-1 fixes) are unsupported and cannot be opened:

- Migration `0021_phase3_declarations.sql` was corrected in place (DUR-1.10).
  Its legacy grant backfill inserted duplicate `TargetIDs` twice and failed
  before any later migration could run. A database that applied the old file
  fails every open with `migration 21 checksum mismatch`.
- Migration `0030_observation_run_closes_once.sql` builds a unique index over
  each run's closing observation. The `c22a53c` services could record two
  closing observations for one run, and on such data the index fails with
  `UNIQUE constraint failed: rec_observation.session_id, rec_observation.f_run_id`.
  The migration's comment claims services kept to one closing outcome per
  run; that holds from `914afef` on, not for `c22a53c`.

This is accepted because those heads were never released (commander ruling
H6). Phase 2 databases, including the frozen fixtures, upgrade through
every migration. Released databases are never edited in place: every change
after a release is a forward migration.
