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
  (line 30) is `PREPARED|SENT|UNKNOWN` — the states holding
  `Conversation.InFlightCallID` (FR-CALL-005's single reservation).
- Transitions, enforced by `ValidCallTransition` (`internal/domain/call.go
  :47-63`): `PREPARED→SENT` (durable dispatch immediately before transport);
  `PREPARED→FAILED` (cancel an unsent operation); `SENT→COMPLETED` /
  `SENT→FAILED` (known outcomes); `SENT→PREPARED` (known retryable failure —
  the attempt closes FAILED, the reservation is kept, and the request must
  be revalidated before the next `MarkCallSent`); `SENT→UNKNOWN` (crash in
  the send/ack gap, INV-15); `UNKNOWN→COMPLETED` / `UNKNOWN→FAILED`
  (reconciliation); `UNKNOWN→ABANDONED` (authorized abandonment, sets
  `Conversation.RequireNewEpoch`, records uncertain usage rather than
  discarding it). No `SENT→SENT` or `UNKNOWN→SENT`: no automatic resend
  without a verified provider idempotency mechanism (FR-CALL-002).
- Restart recovery: any call left `SENT` at process restart must become
  `UNKNOWN` — a crash in the send/ack gap is indistinguishable from "sent,
  no ack," and treating it as more certain risks either a duplicate send or
  discarding a call that completed server-side (INV-15). `RecoverCall` (§8)
  implements this; it is Phase 5 work built on this Phase 1 state machine
  (`ValidCallTransition` already allows `SENT→UNKNOWN`).
- `RecordCallOutcome` idempotency: `CallOutcome.OutcomeHash()`
  (`internal/domain/call.go:141-160`) canonically encodes state, response
  hash, failure reason, retryable flag, and every usage iteration with
  nil-vs-present distinguished explicitly, so "unknown" and "zero" usage
  never collide (FR-COST-001). A repeated identical hash is a no-op; a
  differing hash for the same call is `ErrCallOutcomeConflict`. COMPLETED
  advances `Conversation.Version` exactly once and increments
  `LogicalCalls` only for `OperationInference` — compaction never advances
  the semantic logical-call index (FR-CALL-003). A late outcome for an
  already-`ABANDONED` call is audit-only and must not join a newer epoch.
- `PrepareCall` checks: the conversation's committed Revision/Version must
  equal the preview's base version, else `ErrVersionConflict`; no operation
  may already be reserving, else `ErrCallInFlight` — unless the request's
  derived `CallID` (ADR 4) matches an existing PREPARED record at the same
  base version, returned idempotently; the semantic sequence must equal the
  preview's sequence. Phase 1 makes this last check **strict**: any newer
  committed semantic event invalidates the preview. Relevance-based
  filtering is deferred to Phase 4/5 once the planner can make that
  judgment.
- Every ledger transition consumes a session sequence number and writes a
  `domain.LifecycleEvent` with `TargetKind: TargetCall`
  (`internal/domain/records.go:174-183`), so replay sees a total order of
  ledger transitions (INV-09, FR-OBS-004).
- No database transaction is held across transport: `PrepareCall`,
  `MarkCallSent`, `RecordCallOutcome` are separate `store.Update` calls
  (FR-CALL-001).
- `CallRecord` freezes the inference `Principal` and the trusted dispatcher
  `ServiceActor` separately (`internal/domain/call.go:172-176`), since the
  trusted harness/dispatcher drives the ledger under a conversation service
  grant (§8) while the ledger records who the response is *for*.
- `CallRecord.Validate` (`internal/domain/call.go:224-244`) enforces
  `State.Terminal() == (FinishedSeq != 0)`, so state and audit trail can
  never disagree.

## Alternatives considered

- **`SENT→SENT` for automatic retry.** Rejected: FR-CALL-002 forbids
  automatic resend without verified idempotency; a same-state retransition
  would hide a resend from replay. `SENT→PREPARED` (closing the attempt
  first) is the only sanctioned retry path.
- **A single FAILED terminal state instead of FAILED/UNKNOWN/ABANDONED.**
  Rejected: FR-CALL-002 requires distinguishing a known failure from an
  unknown outcome; collapsing them forces an unsafe implicit resend or
  discard.
- **Silently attaching a late response to the current epoch.** Rejected:
  FR-CALL-004 requires an abandoned attempt's late response be audited
  without joining a newer epoch, or it could duplicate a transition already
  committed under a later epoch (INV-15).
- **Holding the transaction open across the provider HTTP call.** Rejected:
  serializes all other session writes behind network latency; FR-CALL-001
  requires no open transaction during transport.
- **Relevance-aware `PrepareCall` revalidation now.** Rejected: requires the
  Phase 4 planner. A strict "any newer event invalidates" rule never
  under-invalidates and is FR-CALL-001's documented interim.

## Consequences / compatibility impact

- The strict semantic-sequence check produces more `ErrVersionConflict`
  invalidations than a relevance-aware Phase 4+ planner would; narrowing it
  later is a compatible relaxation (turns some invalidations into
  successes), not a breaking change.
- `LogicalCalls` advancing only on inference completions means any usage
  accounting keyed to "completed logical inference index" (e.g. FR-RET-004's
  recurrence window) must read `LogicalCalls`, not a raw completed-call
  count that would include compaction.
- Freezing `ServiceActor` separately from `Principal` means any audit or
  `ExplainAssembly`-style report must pick, per field, "who this call was
  for" vs. "who dispatched it" — conflating them misattributes harness-driven
  dispatch to the inference principal.

## Tests that lock the behavior

- `internal/domain/call_test.go`: `ValidCallTransition` exhaustive over all
  state pairs (confirms `SENT→SENT`/`UNKNOWN→SENT` rejected);
  `Reserving()`/`Terminal()` classification; `OutcomeHash()` stability and
  nil-vs-zero usage sensitivity; `CallRecord.Validate` request-hash
  agreement, session agreement, `Terminal()`/`FinishedSeq` agreement.
- `internal/store/storetest`: `InsertCall`/`UpdateCall` compare-and-swap on
  `Revision`; a transition violating `ValidCallTransition` rejected at the
  store layer; immutable request fields cannot change across `UpdateCall`.
- Trace T10 ("retries, concurrent preparation, and crashes preserve one
  committed history," `docs/sdd-event-traces.md`) is the primary
  integration fixture: duplicate-event idempotency; concurrent `PrepareCall`
  reserving exactly once; a SENT call reading back UNKNOWN after a
  simulated crash with no completed-call/epoch increment; repeated/
  conflicting outcome recording. Needs `storetest` cases plus a
  restart-simulation harness once the SQLite store exists (ADR 3).
- A race-detector test running two goroutines' `PrepareCall` against the
  same conversation concurrently, asserting exactly one succeeds
  (FR-CALL-005 under real concurrency, not just sequential unit tests).

## Open questions

- Where the "known retryable failure" policy (which failures qualify for
  `SENT→PREPARED` vs. terminal `SENT→FAILED`) is configured — likely a
  Phase 5 provider-adapter concern (ADR 9/12), not a state-machine change.
- Whether `CallAttempt.ProviderRequestID` is sufficient for FR-CALL-004's
  reconciliation mechanism, or reconciliation needs more provider-specific
  fields decided in ADR 9.
