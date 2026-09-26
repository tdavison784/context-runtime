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
- **Evidence-gated `UpdateCall`: the store itself, not just ledger-package
  discipline, refuses a call transition unless its evidencing attempt is
  already stored in the matching closed state.** `store.Tx.UpdateCall`'s
  contract requires, for every way of leaving SENT or UNKNOWN:
  `→COMPLETED` needs attempt `c.Attempts` stored COMPLETED with
  `OutcomeHash == c.OutcomeHash`; `→FAILED` needs it stored FAILED with the
  same hash agreement; `→PREPARED` (retry, from SENT only) needs it stored
  FAILED **and** `Retryable`; `→UNKNOWN`/`→ABANDONED` need it stored in the
  matching attempt state. `PREPARED→SENT` needs attempt `c.Attempts` stored
  SENT. Any mismatch fails `ErrInvalidTransition` at the store layer, so a
  caller cannot commit a call-state transition whose attempt-level evidence
  doesn't already exist in the same or an earlier commit — this is stronger
  than "the ledger package happens to write both in one transaction," which
  a differently-written caller could bypass.
- **Attempt transition table and immutability.** `CallAttempt` gains a
  `Retryable` field (a FAILED attempt the retry policy may retry — checked
  by `CallAttempt.Validate`: `Retryable` is only valid on a FAILED attempt).
  `domain.ValidAttemptTransition` fixes the attempt-level state machine
  separately from the call-level one: `SENT→{COMPLETED,FAILED,UNKNOWN}`,
  `UNKNOWN→{COMPLETED,FAILED,ABANDONED}` — an open attempt (SENT or
  UNKNOWN) carries no `FinishedSeq`/`OutcomeHash`; a closed one
  (COMPLETED/FAILED) requires both; ABANDONED is finished without an
  outcome. `store.Tx.PutCallAttempt` is the one unversioned write in the
  store (no CAS revision) precisely because the attempt transition table
  itself is the serialization: once `OutcomeHash` is set, only
  `State`/`OutcomeHash`/`Retryable`/`FinishedSeq`/`FinishedAt` may ever
  change, and closed attempts (COMPLETED, FAILED, ABANDONED) are otherwise
  fully immutable. Together with evidence-gated `UpdateCall` above, there is
  no path that reopens PREPARED from a *presumed* failure: the attempt that
  failed must already be durably FAILED-and-Retryable before the call
  transition is even accepted. Reconciling an UNKNOWN call never takes the
  retry edge: `UNKNOWN→PREPARED` is not in `ValidCallTransition`, so a
  retryable failure discovered by reconciliation is terminal
  (`UNKNOWN→FAILED`).
- **`CallRecord.Validate`'s terminal-state consistency.** A COMPLETED call
  requires its `Outcome`; a FAILED call requires either an `Outcome` or
  (only when `Attempts == 0`) a cancellation `Reason` with no outcome — a
  call that was ever sent cannot fail without one; an ABANDONED call
  requires a `Reason` and **no** `Outcome`; any non-terminal state carries
  no `Outcome` at all. Where an `Outcome` is present, `Outcome.State` must
  equal `State` and `Outcome.Attempt` must equal `Attempts` — closing the
  gap where a completed call's `Outcome` could silently describe a
  different state or a different (non-final) attempt than the record
  itself claims.
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
- **Required check order in `Prepare`: base version, then semantic
  staleness, then the in-flight `ProposalHash` comparison — never the
  reverse.** `Prepare` must first check the conversation's committed
  version against the preview's base version (`ErrVersionConflict` on
  mismatch) and semantic staleness (below), and only then compare an
  existing in-flight PREPARED record's `ProposalHash` (ADR 4) to decide
  whether this is an idempotent repeat. Checking the in-flight record
  first — returning a match before validating the base version or
  semantic sequence — would let a `Prepare` call that is idempotent by ID
  alone return success for a proposal whose semantic basis has since
  changed, silently reusing a now-stale reservation instead of failing
  `ErrVersionConflict`. `DerivedCallID`'s conversation-revision binding
  (ADR 4) and this check order are two halves of the same guarantee: the ID
  prevents *aliasing* across reservations, the order prevents a *correct*
  ID match from skipping revalidation.
- Semantic-sequence revalidation: the check is **strict** in Phase 1; a preview is
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
- **Trusting `DerivedCallID`'s conversation-revision binding alone to make
  re-`Prepare` safe, without also fixing the check order.** Rejected
  (Codex finding N1): the ID scheme prevents an unrelated later operation
  from aliasing an earlier call's ID, but it does not by itself stop a
  *correct* ID match from short-circuiting past a revalidation the base
  version or semantic sequence would otherwise fail. Both the ID scheme and
  the check order are required, and neither substitutes for the other.
- **A CAS revision on `PutCallAttempt`, matching every other store method.**
  Rejected: the attempt-level state machine (`ValidAttemptTransition`,
  immutability once closed) is itself the serialization discipline for
  attempts; a caller can only ever move an attempt forward along that table,
  so a separate revision number would duplicate a guarantee the transition
  table already provides.
- **Implementing the §8 service grant now instead of an owner-match check.**
  Rejected for Phase 1: no grant-issuance machinery for dispatch actions
  exists yet, and SYSTEM/HARNESS-plus-owner-match is a sound, conservative
  subset (never permits an unrelated dispatcher) to build the rest of the
  ledger against.

## Consequences / compatibility impact

- **Known implementation gap (not yet fixed as of this ADR):** the
  committed `internal/invocation/prepare.go` still checks the conversation's
  in-flight held record *before* the base-version and semantic-staleness
  checks, and its `nextCallID` still derives the call ID from
  `(session, conversation, BaseConversationVersion, requestHash)` with a
  generation-probing loop rather than `(session, conversation,
  conversationRevision, CallProposalHash)`, and never populates
  `CallRecord.ProposalHash` before `InsertCall` — which now fails
  `CallRecord.Validate`'s `ProposalHash == CallProposalHash(c)` check. This
  ADR's Decision is the target contract; bringing `internal/invocation` in
  line with it (check order, `DerivedCallID` v2 call site, `ProposalHash`
  population) is required before Phase 1's gate, not optional cleanup.
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

- `internal/domain/call_test.go`: `ValidCallTransition` and
  `ValidAttemptTransition` exhaustive; `CallOutcome.Validate`/`OutcomeHash`
  per-attempt behavior (ADR 4); `CallAttempt.Validate` — open/closed
  consistency per state, `Retryable` only valid on FAILED;
  `CallRecord.Validate` — proposal-hash and finished-sequence agreement,
  and now the per-state `Outcome`/`Reason` matrix (COMPLETED requires
  Outcome; FAILED requires Outcome or a zero-attempt cancellation Reason;
  ABANDONED requires Reason and no Outcome; `Outcome.State == State` and
  `Outcome.Attempt == Attempts` when Outcome is present).
- Required: `internal/store/storetest` — `UpdateCall`'s evidence gating:
  each of `→COMPLETED`/`→FAILED`/`→PREPARED`/`→UNKNOWN`/`→ABANDONED` and
  `PREPARED→SENT` fails `ErrInvalidTransition` when attempt `c.Attempts`
  isn't already stored in the required closed/matching state; `PutCallAttempt`
  rejects any field change on a closed attempt other than
  `State`/`OutcomeHash`/`Retryable`/`FinishedSeq`/`FinishedAt`.
- `internal/invocation` (`ledger_test.go`, `t10_test.go`, `property_test.go`,
  `helpers_test.go`, already committed, but see the Consequences gap above):
  retry only from a durably closed attempt; reconciling UNKNOWN never takes
  `UNKNOWN→PREPARED`; late-outcome audit path for ABANDONED calls;
  `semanticStale` true/false boundary at `SemanticSeq == LastSeq` and across
  intervening non-ledger sequence numbers; lifecycle event ID determinism
  across a simulated restart.
- Required: an `internal/invocation` test asserting `Prepare`'s check
  order directly — construct a conversation with an in-flight PREPARED call
  whose `ProposalHash` matches a new request, but whose base version or
  semantic sequence is now stale, and assert `ErrVersionConflict`, not a
  silent idempotent return (locks the N1 fix once `prepare.go` is corrected).
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
- Tracking item: land the `internal/invocation/prepare.go` fix described in
  Consequences (check order; `DerivedCallID` v2 call site; populate
  `ProposalHash`) before Phase 1's gate is claimed met.

## Review

First pass (Codex gpt-6-sol xhigh, `codex-decision-review-out.md`, finding
3): outcome identity moved to per-attempt (ADR 4, applied here to the retry
edge); documented that `SENT→PREPARED` only follows a durably closed FAILED
attempt, never a bare retryable flag; reconciliation from UNKNOWN recorded
as terminal. Also updated (not itself a Codex finding) against the then-new
`internal/invocation` implementation: store-enforced single-reservation
rule, per-transaction sequence allocation, the exact semantic-staleness
rule, and the deferred service-grant status.

Second pass (Codex gpt-6-sol xhigh, `codex-contract-v2-review.md`, findings
N1, N2, N5, verifying PARTIAL on finding 3): the first pass's retry-edge fix
was real but incomplete, and a new high-severity defect (N1) surfaced in the
committed ledger code. Changed: documented the store-level evidence gating
now required on every `UpdateCall` transition out of SENT/UNKNOWN, not just
ledger-package sequencing (N2/finding 3, closing the "a caller can commit
the state change alone" gap); added `ValidAttemptTransition` and the
attempt-immutability rule now enforced by `PutCallAttempt` (N5); added
`CallRecord.Validate`'s per-state `Outcome`/`Reason` consistency matrix
(N5, closing "a COMPLETED CallRecord with nil Outcome passes"); recorded
the required `Prepare` check order (base version → semantic staleness →
in-flight `ProposalHash`) as a decided rule, and flagged in Consequences
and Open questions that the committed `internal/invocation/prepare.go`
does not yet implement it (N1) — this ADR's Decision is the target
contract that package must still be brought into line with.
