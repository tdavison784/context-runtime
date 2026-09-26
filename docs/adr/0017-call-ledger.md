# 17. Call ledger state machine

Status: Proposed
Date: 2026-09-25

## Context

FR-CALL-001 through FR-CALL-005 define the provider-call lifecycle: Prepare
freezes a request under a conversation version/access snapshot and reserves
one outstanding operation; the dispatcher persists SENT immediately before
transport; the result is COMPLETED, FAILED, or UNKNOWN; RecordCallOutcome is
idempotent and advances conversation state exactly once; at most one
operation is PREPARED/SENT/UNKNOWN per conversation; UNKNOWN retains the
reservation until reconciled or explicitly abandoned. INV-15 requires epochs
to advance only on an atomic recorded completion, with no implicit resend,
fork, or replayed tool effect from an unknown outcome.

## Decision

- States: `PREPARED, SENT, COMPLETED, FAILED, UNKNOWN, ABANDONED`
  (`domain.CallState`, `internal/domain/call.go:9-17`). `Reserving()`
  (`internal/domain/call.go:30-32`) is exactly `PREPARED|SENT|UNKNOWN` — the
  states that hold `Conversation.InFlightCallID`
  (`internal/domain/call.go:88`), implementing FR-CALL-005's "at most one
  operation reserving per conversation."
- Transitions, enforced by `ValidCallTransition`
  (`internal/domain/call.go:47-63`):
  `PREPARED→SENT` (durable dispatch immediately before transport, FR-CALL-002);
  `PREPARED→FAILED` (cancel an unsent operation, used by RecoverCall per
  FR-CALL-004's "cancel a still-PREPARED operation as a known unsent
  failure");
  `SENT→COMPLETED` / `SENT→FAILED` (known outcomes);
  `SENT→PREPARED` (a known retryable failure: the attempt is closed FAILED,
  the reservation is kept, and the request must be revalidated before the
  next `MarkCallSent` — this is how a recorded retry policy resends without
  ever going `SENT→SENT`, which the state machine forbids);
  `SENT→UNKNOWN` (acceptance/completion cannot be established — a crash in
  the send/ack gap, INV-15);
  `UNKNOWN→COMPLETED` / `UNKNOWN→FAILED` (reconciliation);
  `UNKNOWN→ABANDONED` (authorized explicit abandonment, which also sets
  `Conversation.RequireNewEpoch`, per FR-CALL-004, and records uncertain
  usage rather than discarding it). There is no `SENT→SENT` or
  `UNKNOWN→SENT` transition in the table — no automatic resend without a
  verified provider idempotency/reconciliation mechanism (FR-CALL-002).
- Restart recovery: any call left `SENT` when the process restarts must be
  reclassified `UNKNOWN` before further use — a crash in the send/ack gap is
  indistinguishable from "sent but no acknowledgment received," and treating
  it as anything more certain than UNKNOWN would risk either a silent
  duplicate send or silently discarding a call that did complete
  server-side (INV-15). This restart rule is implemented by `RecoverCall`
  (§8's `Runtime` interface), which is Phase 5 work; the state machine
  itself (`ValidCallTransition` already allowing `SENT→UNKNOWN`) is the
  Phase 1 contract that rule depends on.
- `RecordCallOutcome` idempotency: `CallOutcome.OutcomeHash()`
  (`internal/domain/call.go:141-160`) canonically encodes state, response
  hash, failure reason, retryable flag, and every usage iteration's fields
  (nil vs. present distinguished explicitly, so "unknown" and "zero" usage
  never collide in the hash — FR-COST-001's "unknown usage is labeled
  unknown, never zero" reflected directly in the hash encoding). A repeated
  identical outcome hash is a no-op returning the stored record; a
  differing hash for the same call is `ErrCallOutcomeConflict`. A COMPLETED
  outcome advances `Conversation.Version` exactly once and increments
  `LogicalCalls` only for `OperationInference`
  (`internal/domain/call.go:76-79`) — compaction operations
  (`OperationCompaction`) never advance the semantic logical-call index,
  matching FR-CALL-003's "only completed inference responses advance the
  semantic logical-call index." A late outcome for an already-`ABANDONED`
  call is audit-only and must not silently join a newer epoch (FR-CALL-004).
- `PrepareCall` checks (implemented above the ledger, using
  `domain.DerivedCallID` from ADR 4, and `CallRecord.Validate`
  (`internal/domain/call.go:224-244`) for structural integrity): the
  conversation's committed `Revision`/`Version` must equal the preview's
  base version, else `ErrVersionConflict`; no operation may already be
  reserving for the conversation, else `ErrCallInFlight` — unless the
  request's derived `CallID` matches an existing PREPARED record for the
  same base version, in which case the original is returned idempotently
  (FR-CALL-001's "repeating an identical PrepareCall ... returns the same
  ... record"); and the semantic sequence must equal the preview's sequence.
  Phase 1 makes this last check **strict**: any newer committed semantic
  event invalidates the preview, full stop. Relevance-based filtering
  (deciding a newer event doesn't actually matter to this request) is
  deferred to Phase 4/5 once the planner exists to make that judgment
  (FR-CALL-001: "Counts and visibility are revalidated before dispatch if
  their inputs changed").
- Every ledger transition consumes a session sequence number and writes a
  `domain.LifecycleEvent` with `TargetKind: TargetCall`
  (`internal/domain/records.go:174-183`, `TargetCall` at line 182), so replay
  (INV-09, FR-OBS-004) sees a total order of ledger transitions alongside
  every other lifecycle change in the same session.
- No database transaction is held across transport: `PrepareCall`,
  `MarkCallSent`, and `RecordCallOutcome` are separate `store.Update` calls
  (`internal/store/store.go:26-30` documents `Update` as one atomic
  transaction per invocation), matching FR-CALL-001's "No database
  transaction remains open during token-count endpoints or provider
  transport."
- `CallRecord` freezes both the inference `Principal` and the trusted
  dispatcher `ServiceActor` separately (`internal/domain/call.go:172-176`),
  so the ledger records who the response is *for* independently of who
  *drove* the transport — required because §8 states "the trusted
  harness/dispatcher drives PrepareCall ... under a conversation-specific
  service grant; these methods are not agent tools" while "the ledger
  freezes the inference principal separately from that service actor."
- `CallRecord.Validate` (`internal/domain/call.go:224-244`) additionally
  enforces `State.Terminal() == (FinishedSeq != 0)` — a terminal state
  (COMPLETED/FAILED/ABANDONED) must have a recorded finishing sequence, and
  a non-terminal state must not, so a record's state and its audit trail
  can never disagree.

## Alternatives considered

- **Allowing `SENT→SENT` for automatic retry.** Rejected: FR-CALL-002
  explicitly forbids automatic resend without a verified provider
  idempotency mechanism; a same-state retransition would hide a resend
  inside what looks like "no state change," making it invisible to replay
  and to the reservation model. `SENT→PREPARED` (closing the attempt FAILED
  first) is the only sanctioned retry path, and it is visible as two
  distinct lifecycle events.
- **A single "FAILED" terminal state instead of separate FAILED/UNKNOWN/
  ABANDONED.** Rejected: FR-CALL-002 requires distinguishing a *known*
  failure (safe to treat as not-sent) from an *unknown* outcome (must not be
  treated as either sent-and-failed or sent-and-succeeded); collapsing them
  would force either an unsafe implicit resend or an unsafe implicit
  discard of a call that may have completed server-side.
- **Letting reconciliation silently attach a late response to the current
  epoch.** Rejected: FR-CALL-004 requires an abandoned attempt's late
  response be audited without joining a newer epoch — attaching it silently
  would let a stale response overwrite or duplicate a semantic transition
  already committed under a subsequent epoch, breaking INV-15's "atomic
  recorded completion" guarantee.
- **Holding the database transaction open across the provider HTTP call.**
  Rejected: this would serialize all other session writes behind
  network latency for the duration of every provider round trip, and
  FR-CALL-001 explicitly requires no transaction remain open during
  transport.
- **Relevance-aware `PrepareCall` revalidation in Phase 1.** Rejected for
  now: relevance requires the planner (Phase 4), which does not exist yet;
  a strict "any newer semantic event invalidates" rule is safe (never
  under-invalidates) and is the documented Phase 1 interim per FR-CALL-001.

## Consequences / compatibility impact

- The strict semantic-sequence check means Phase 1–3 callers will see more
  `ErrVersionConflict` invalidations than a relevance-aware Phase 4+ planner
  would produce; this is a deliberate over-invalidation, not a bug, and
  narrowing it in Phase 4/5 is a compatible relaxation, not a breaking
  change, since it only turns some invalidations into successes.
  Loosening it earlier could hide a would-be race the current tests should
  catch.
- `LogicalCalls` only advancing on `OperationInference` completions means
  any usage/cost accounting keyed to "completed logical inference index"
  (FR-RET-004's recurrence window, for example) must read `LogicalCalls`,
  not a raw completed-call count that would include compaction operations.
- Freezing `ServiceActor` separately from `Principal` means any future audit
  or `ExplainAssembly`-style report must decide, per field, whether it wants
  "who this call was for" or "who actually dispatched it" — conflating them
  would misattribute a harness-driven dispatch to the inference principal.

## Tests that lock the behavior

- Required: `internal/domain/call_test.go` — `ValidCallTransition`
  exhaustively over all state pairs (asserts exactly the transitions listed
  above and no others, including confirming `SENT→SENT` and `UNKNOWN→SENT`
  are both rejected); `CallState.Reserving()`/`Terminal()` classification;
  `CallOutcome.OutcomeHash()` stability (same outcome → same hash) and
  sensitivity (differing usage nil-vs-zero produces different hashes,
  locking the "unknown never equals zero" property).
- Required: `internal/domain/call_test.go` — `CallRecord.Validate`: request
  hash must match request bytes; principal/service-actor session must match
  the record's session; `Terminal()`/`FinishedSeq` agreement in both
  directions.
- Required: `internal/store/storetest` — `InsertCall`/`UpdateCall`:
  compare-and-swap on `Revision` (a stale `expectedRevision` fails); a state
  change violating `ValidCallTransition` is rejected at the store layer, not
  just trusted to callers; immutable request fields (RequestHash, Request
  bytes, Principal) cannot change across an `UpdateCall`.
- Trace T10 ("retries, concurrent preparation, and crashes preserve one
  committed history," `docs/sdd-event-traces.md`) is the primary integration
  fixture for this ADR: (1) duplicate event ingestion is idempotent; (2)
  concurrent `PrepareCall` against the same version reserves exactly once,
  the loser failing `ErrCallInFlight` or `ErrVersionConflict`; (3) a call
  persisted SENT then "crashed" reads back UNKNOWN on restart with no
  completed-call/epoch increment; (4) recording the same outcome twice is a
  no-op, recording a conflicting outcome fails, and epoch/usage advance
  exactly once. This needs `internal/store/storetest` cases plus a
  restart-simulation harness (close and reopen the store) once the SQLite
  store exists (ADR 3).
- Required: a race-detector test that runs two goroutines' `PrepareCall`
  against the same conversation concurrently through `store.Update` and
  asserts exactly one succeeds — validates FR-CALL-005's reservation
  exclusivity under real concurrency, not just sequential unit tests of
  `ValidCallTransition`.

## Open questions

- Where the "known retryable failure" policy (which failures qualify for
  `SENT→PREPARED` rather than `SENT→FAILED` terminally) is configured —
  not yet a type in `internal/domain`; likely a Phase 5 provider-adapter
  concern (ADR 9/12) layered on top of this state machine rather than a
  change to the state machine itself.
- Whether `CallAttempt`'s per-attempt `ProviderRequestID`
  (`internal/domain/call.go:277`) is sufficient for FR-CALL-004's "verified
  provider idempotency or reconciliation mechanism," or whether
  reconciliation needs additional provider-specific fields decided in ADR 9.
