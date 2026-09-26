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
  ID match from skipping revalidation. `internal/invocation/prepare.go`
  implements this order directly: base version, then `semanticStale`, then
  epoch, and only then the in-flight `ProposalHash` comparison.
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

- `internal/domain/call_test.go`: `TestValidCallTransitionMatrix`,
  `TestValidCallTransition_TerminalStatesHaveNoOutgoingTransition`,
  `TestValidCallTransition_NoDirectResendToSent`,
  `TestValidAttemptTransitionMatrix`,
  `TestValidAttemptTransition_ClosedAttemptsAreImmutable`,
  `TestValidAttemptTransition_NoSelfOrBackwardLoop`;
  `TestOutcomeHashDistinguishesAttempt` and the other `TestOutcomeHash*`
  cases (ADR 4); `TestCallAttemptValidate` (open/closed consistency per
  state, `Retryable` only valid on FAILED); `TestCallRecordValidate` and
  `TestCallRecordValidate_TerminalEvidenceMatrix` (proposal-hash agreement
  and the per-state `Outcome`/`Reason` matrix).
- `internal/store/storetest` (`storetest.Run`, exercised by both
  `internal/store/memory:TestConformance` and `internal/store
  /sqlite:TestConformance`): `TestConformance/CallEvidence`
  (`storetest/calls.go:testCallEvidence`) is the exact evidence-gating test
  — each of `→COMPLETED`/`→FAILED`/`→PREPARED`/`→UNKNOWN` from SENT, and
  `→ABANDONED` from UNKNOWN, fails `ErrInvalidTransition` against an open or
  wrongly-closed attempt, including the case where a *different* outcome
  hash was recorded for the same attempt; `TestConformance/CallAttempts`
  (`testCallAttempts`) covers dense numbering, the attempt transition table,
  and that only `State`/`OutcomeHash`/`Retryable`/`FinishedSeq`/`FinishedAt`
  may change once an attempt is closed; `TestConformance/CallReservation`
  (`testCallReservation`) covers the one-reserving-call rule, including
  across separate transactions and that other conversations are unaffected.
- `internal/store/sqlite/durability_test.go:TestCallTransitionsRequireAttemptEvidence`
  reconfirms the evidence-gating rule specifically against the SQLite
  typed-column write path (ADR 3).
- `internal/invocation`: `ledger_test.go` — `TestRetryableFailureLoop`,
  `TestNonRetryableFailureReleases`, `TestReconciledRetryableFailureIsTerminal`
  (`UNKNOWN→PREPARED` never taken), `TestErrLateOutcomeWrapsInvalidTransition`,
  `TestEveryTransitionHasOneEventAndDenseSeqs`,
  `TestCompactionDoesNotAdvanceLogicalCalls`,
  `TestServiceActorAuthorization`; `TestStaleDuplicatePreview` is the exact
  regression test for the required `Prepare` check order (base version →
  semantic staleness → epoch → in-flight `ProposalHash`), confirming a
  changed semantic sequence fails `ErrVersionConflict` even though the
  repeated request would otherwise match the held reservation; `t10_test.go`
  — `TestT10ConcurrentPrepare`, `TestT10CrashAndReconcile`,
  `TestT10AbandonedLateResponse` are the Trace T10 fixtures directly;
  `property_test.go:TestRandomizedLedgerInvariants` fuzzes the ledger's
  invariants across randomized transition sequences.
- `internal/invocation/sqlite_test.go`: `TestSQLiteRestartBetweenSentAndRecover`,
  `TestSQLiteRandomizedInvariants`, `TestSQLiteConcurrentPrepare` rerun the
  same ledger properties against the real SQLite store, not just the memory
  store.

## Open questions

- Design of the Phase 5 `ActionDispatchCall` service grant: how a
  narrower-scoped dispatcher is authorized to drive a call for a principal
  it doesn't directly own, without weakening the current owner-match floor.
- Whether `CallAttempt.ProviderRequestID` is sufficient for FR-CALL-004's
  reconciliation mechanism, or reconciliation needs more provider-specific
  fields decided in ADR 9.

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

Verified against the integrated ledger fix (commit `0c8ce40` plus tests
`36ba4a2`/`dc49396`, full race suite green on `phase-1-foundation` at
`ddbb53e`): `prepare.go` now checks base version, `semanticStale`, and
epoch before comparing the in-flight record's `ProposalHash`, and
populates `ProposalHash` before `InsertCall`. `TestStaleDuplicatePreview`
locks the regression directly. Finding N1 is closed; the Consequences and
Open questions tracking items above are removed accordingly.
