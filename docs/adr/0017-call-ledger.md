# 17. Call ledger state machine

Status: Proposed
Date: 2026-09-25

## Context

FR-CALL-001 through FR-CALL-005 define the provider-call lifecycle: Prepare
freezes a request under a conversation version/access snapshot and reserves
one outstanding operation; the dispatcher persists SENT immediately before
transport; the result is COMPLETED, FAILED, or UNKNOWN; RecordCallOutcome is
idempotent per attempt and advances conversation state exactly once; at most
one operation is PREPARED/SENT/UNKNOWN per conversation; UNKNOWN retains the
reservation until reconciled or explicitly abandoned. INV-15 requires epochs
to advance only on an atomic recorded completion, with no implicit resend,
fork, or replayed tool effect from an unknown outcome. §8 assigns
PrepareCall/MarkCallSent/RecordCallOutcome/RecoverCall to a trusted
harness/dispatcher acting "under a conversation-specific service grant,"
distinct from the frozen inference principal.

## Decision

- States: `PREPARED, SENT, COMPLETED, FAILED, UNKNOWN, ABANDONED`
  (`domain.CallState`). `Reserving()` is `PREPARED|SENT|UNKNOWN` — the
  states holding `Conversation.InFlightCallID` (FR-CALL-005).
- Transitions, enforced by `ValidCallTransition`: `PREPARED→SENT`;
  `PREPARED→FAILED` (cancel); `SENT→COMPLETED`/`SENT→FAILED`;
  `SENT→PREPARED` (retry); `SENT→UNKNOWN`; `UNKNOWN→COMPLETED`/
  `UNKNOWN→FAILED` (reconciliation); `UNKNOWN→ABANDONED`. No `SENT→SENT` or
  `UNKNOWN→SENT` (FR-CALL-002).
- **`SENT→PREPARED` requires a conclusively closed attempt, not just a
  retryable flag.** The ledger's `RecordOutcome`
  (`internal/invocation/dispatch.go`) only takes this transition from inside
  the same call that just recorded the outcome: it validates `attemptNo ==
  c.Attempts` and `c.State == CallSent`, writes the attempt's own immutable
  `CallAttempt.State = AttemptFailed` / `FinishedSeq` alongside the call
  transition in the same store transaction, and only then moves the call to
  PREPARED (`o.Retryable && c.State == CallSent`). There is no path that
  reopens PREPARED from a *presumed* failure — the attempt that failed is
  durably recorded as FAILED first, atomically with the call's transition,
  before the reservation becomes revalidatable. Reconciling an UNKNOWN call
  never takes this edge: `UNKNOWN→PREPARED` is not in
  `ValidCallTransition`, so a retryable failure discovered by reconciliation
  is terminal (`UNKNOWN→FAILED`), matching the state-machine comment that a
  transport outcome can never be assumed known once it was ever UNKNOWN.
- Restart recovery: `internal/invocation.Recover` turns every `SENT` call
  into `UNKNOWN` at startup — a crash in the send/ack gap is
  indistinguishable from "sent, no ack," and treating it as more certain
  risks either a duplicate send or discarding a call that completed
  server-side (INV-15).
- Outcome idempotency is per attempt (ADR 4): `CallOutcome.Attempt` plus
  `OutcomeHash()` identify one attempt's outcome; repeating the same audited
  outcome for the same attempt is a no-op, a differing one is
  `ErrCallOutcomeConflict`. `RecordOutcome` also recognizes a repeat via a
  stored audit-blob receipt (`putAudit`/`receiptExists`) so a duplicate
  outcome delivered after a crash between commit and acknowledgment is still
  a no-op. `CallOutcome.Validate` binds `ResponseHash` to `Response` bytes
  (ADR 4), so two different response bodies can never share an identity.
  COMPLETED advances `Conversation.Version` exactly once and increments
  `LogicalCalls` only for `OperationInference`. A late outcome for an
  `ABANDONED` call is stored as audit-only data (`recordLate`) and returned
  with `ErrLateOutcome`; it never touches the call or conversation
  (FR-CALL-004, INV-15).
- One reserving call per conversation is enforced by the **store**, not
  only by ledger-level logic: `store.Tx.InsertCall`/`UpdateCall` themselves
  fail with `ErrCallInFlight` if the conversation already holds another
  reserving call, so no store implementation (memory or SQLite) can be
  written to skip this rule.
- Idempotent re-`Prepare` and semantic-sequence revalidation: `DerivedCallID`
  v2 and `CallProposalHash` (ADR 4) let `Prepare` recognize a repeated
  identical proposal against the conversation's currently held in-flight
  record. The semantic-sequence check is **strict** in Phase 1: a preview is
  stale iff any sequence number in `(SemanticSeq, LastSeq]` is **not** a
  `TargetCall` lifecycle event
  (`internal/invocation.semanticStale`) — i.e. any committed sequence number
  after the preview's semantic seq that the ledger itself didn't produce is
  treated as a semantic change, even one the ledger doesn't recognize. Since
  sequence numbers are dense (`store.Tx.NextSeq`), this reduces to a count
  comparison over call lifecycle events after `SemanticSeq`, with no
  separate state to keep in sync, and it errs toward over-invalidating
  rather than risking a stale preview passing. Only the invocation package
  writes call lifecycle events, which is what makes the count exact.
  Relevance-based filtering (deciding a newer event doesn't actually matter)
  is deferred to Phase 4/5 once the planner exists.
- Sequence allocation and audit: every ledger transition allocates its
  sequence number with `store.Tx.NextSeq()` inside the same transaction as
  the state change and appends exactly one `domain.LifecycleEvent` with
  `TargetKind: TargetCall`, whose ID is itself derived
  (`lifecycleEventID(sessionID, seq)`, a canonical hash of session and
  sequence) so replay after a restart reproduces the same event IDs
  (ADR 4, INV-09). `store.Tx.AppendLifecycleEvent`'s doc comment reserves
  `TargetCall` events for `internal/invocation` specifically, so no other
  package can forge a ledger audit entry.
- No database transaction is held across transport: `Prepare`, `MarkSent`,
  `RecordOutcome` are separate `store.Update` calls.
- **Service grant deferred.** `CallRecord` freezes the inference `Principal`
  separately from `ServiceActor`, but Phase 1 does not yet implement §8's
  "conversation-specific service grant": `internal/invocation
  .checkServiceActor` requires the actor's authority be SYSTEM or HARNESS,
  and `checkActorScope` requires the actor's non-empty owner fields match
  the call's inference principal — an owner-match check, not a grant lookup.
  A dedicated `ActionDispatchCall` grant type (letting a narrower-scoped
  dispatcher drive a call it doesn't directly own) is open for Phase 5.
- `CallRecord.Validate` enforces `ProposalHash == CallProposalHash(c)` and
  `State.Terminal() == (FinishedSeq != 0)`, so a record's frozen identity and
  its state/audit trail can never disagree.

## Alternatives considered

- **A single call-level `OutcomeHash` covering the ledger's final state
  instead of per-attempt identity.** Rejected (ADR 4, Codex finding 3): loses
  duplicate detection across a retry sequence.
- **Allowing `SENT→PREPARED` on a bare `Retryable` flag without requiring the
  failing attempt to be durably closed first.** Rejected: an uncertain
  attempt (one that might still complete server-side) must become UNKNOWN,
  never silently reopen PREPARED — reopening before the attempt's own
  FAILED state and `FinishedSeq` are committed would let a live attempt and
  a revalidatable reservation coexist.
- **Enforcing "one reserving call per conversation" only in the ledger
  package, trusting the store to be a dumb record store.** Rejected:
  FR-CALL-005 is a correctness invariant, not a caller convenience; pushing
  the check into `InsertCall`/`UpdateCall` means it holds regardless of
  which package or future caller drives the store.
- **Holding the transaction open across the provider HTTP call.** Rejected:
  FR-CALL-001 requires no open transaction during transport.
- **Relevance-aware `PrepareCall` revalidation now.** Rejected: requires the
  Phase 4 planner; the strict per-sequence-number rule never
  under-invalidates and is the documented interim.
- **Implementing the §8 service grant now instead of an owner-match check.**
  Rejected for Phase 1: no grant-issuance machinery for dispatch actions
  exists yet, and SYSTEM/HARNESS-plus-owner-match is a sound, conservative
  subset (never permits an unrelated dispatcher) to build the rest of the
  ledger against.

## Consequences / compatibility impact

- The strict semantic-sequence check produces more `ErrVersionConflict`
  invalidations than a relevance-aware Phase 4+ planner would; narrowing it
  later is a compatible relaxation, not a breaking change.
- Reserving `TargetCall` lifecycle events for `internal/invocation` means any
  other package that needs to record a call-related audit entry must do so
  through this package's API, not by writing a `LifecycleEvent` directly.
- The deferred service grant means Phase 1's dispatcher authorization is
  strictly narrower than §8 eventually requires (owner-match only); Phase 5
  must add the grant type without weakening the current SYSTEM/HARNESS +
  owner-match floor.
- `LogicalCalls` advancing only on inference completions means usage
  accounting keyed to "completed logical inference index" must read
  `LogicalCalls`, not a raw completed-call count.

## Tests that lock the behavior

- `internal/domain/call_test.go`: `ValidCallTransition` exhaustive;
  `CallOutcome.Validate`/`OutcomeHash` per-attempt behavior (ADR 4);
  `CallRecord.Validate` proposal-hash and finished-sequence agreement.
- `internal/invocation` (`ledger_test.go`, `t10_test.go`, `property_test.go`,
  `helpers_test.go`, already committed): retry only from a durably closed
  attempt; reconciling UNKNOWN never takes `UNKNOWN→PREPARED`; late-outcome
  audit path for ABANDONED calls; `semanticStale` true/false boundary at
  `SemanticSeq == LastSeq` and across intervening non-ledger sequence
  numbers; lifecycle event ID determinism across a simulated restart.
- `internal/store/storetest`: `InsertCall`/`UpdateCall` reject a second
  reserving call for the same conversation with `ErrCallInFlight`; a
  transition violating `ValidCallTransition` rejected at the store layer.
- Trace T10 is the primary integration fixture; a race-detector test running
  two goroutines' `Prepare` against the same conversation concurrently,
  asserting exactly one succeeds.

## Open questions

- Design of the Phase 5 `ActionDispatchCall` service grant: how a
  narrower-scoped dispatcher is authorized to drive a call for a principal
  it doesn't directly own, without weakening the current owner-match floor.
- Whether `CallAttempt.ProviderRequestID` is sufficient for FR-CALL-004's
  reconciliation mechanism, or reconciliation needs more provider-specific
  fields decided in ADR 9.

## Review

Scrutinized by Codex gpt-6-sol xhigh (`codex-decision-review-out.md`,
finding 3). Changed: outcome identity moved to per-attempt (ADR 4, applied
here to the retry edge); documented that `SENT→PREPARED` only follows a
durably closed FAILED attempt in the same transaction as the call
transition, never a bare retryable flag; recorded that reconciliation from
UNKNOWN is terminal (`UNKNOWN→FAILED`), never `UNKNOWN→PREPARED`. Also
updated against the committed `internal/invocation` implementation (not
reviewed by Codex, which predates it): the store-enforced single-reservation
rule, per-transaction sequence allocation with derived lifecycle-event IDs,
the exact semantic-staleness rule, and the deferred service-grant status of
dispatcher authorization.
