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
- **Evidence-gated `UpdateCall`, judged against the stored *current*
  attempt (round 1, DUR-1.1).** `store.Tx.UpdateCall`'s contract requires,
  for every way of leaving SENT or UNKNOWN: `→COMPLETED` needs attempt
  `c.Attempts` stored COMPLETED with `OutcomeHash == c.OutcomeHash`;
  `→FAILED` needs it stored FAILED with the same hash agreement;
  `→PREPARED` (retry, from SENT only) needs it stored FAILED **and**
  `Retryable`; `→UNKNOWN`/`→ABANDONED` need it stored in the matching
  attempt state. `PREPARED→SENT` needs attempt `c.Attempts` stored SENT.
  Critically, `next.Attempts` itself is now constrained: it must equal
  `old.Attempts` for every transition **except** `PREPARED→SENT`, which
  requires exactly `old.Attempts+1`. Before this constraint, a caller could
  present evidence for an *older* attempt number while a newer attempt was
  still open — e.g. move a call to a terminal state citing attempt 1's
  closed, failed outcome while attempt 2 (opened by a retry) was still
  SENT, then release the reservation and prepare a second operation while
  attempt 2 remained live in flight, forking the conversation (INV-15,
  FR-CALL-005). Fixing `Attempts` to the stored value (or its exact
  successor) makes "the evidence" and "the current attempt" the same
  question, closing that gap. **Terminal calls (COMPLETED, FAILED,
  ABANDONED) are now fully immutable at the store layer:** any `UpdateCall`
  against a terminal call fails `ErrImmutable`, regardless of what it tries
  to change — before this, a same-state update (leaving `State` unchanged)
  was accepted with no constraint on `Attempts`/`Outcome`/`OutcomeHash`/
  `Reason`/`FinishedSeq`, so a caller could rewrite a COMPLETED call's
  recorded outcome after the fact; the two stores even disagreed about it
  (memory silently allowed rewriting a COMPLETED outcome, SQLite refused
  with `ErrCallOutcomeConflict`). Any mismatch on any of the rules above
  fails `ErrInvalidTransition` (or `ErrImmutable` for a terminal call) at
  the store layer, so a caller cannot commit a call-state transition whose
  attempt-level evidence doesn't already exist, or whose call is already
  finished — this is stronger than "the ledger package happens to write
  both in one transaction," which a differently-written caller could
  bypass, and it makes both stores agree.
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
- **`RecoverAll` closes the "which sessions exist" gap in startup recovery
  (round 1, DUR-1.8).** `Recover` operates one session at a time and needs
  a `Principal` for that session; nothing previously let the runtime
  discover *which* sessions might have a SENT call to recover, so T10 step
  3 ("restart recovers UNKNOWN calls") silently depended on the embedding
  harness independently tracking every session ID it had ever used and
  calling `Recover` for each — a SENT call in an untracked session would
  never become UNKNOWN and would keep its reservation forever, and
  `Abandon` cannot clear it because `Abandon` requires UNKNOWN first (a
  liveness gap, not a safety one). The fix: `store.Store.Sessions(ctx)
  ([]string, error)` lists every session with committed records, in
  ascending order; `Ledger.RecoverAll(ctx, actorFor func(sessionID string)
  domain.Principal)` iterates it, recovers each session in its own
  transaction under the actor `actorFor` supplies (rejecting a mismatched
  session for that actor), continues past a failing session rather than
  stopping, and returns every recovered UNKNOWN call plus the joined
  errors. Startup recovery no longer depends on anything the store itself
  doesn't already know.
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
- **The store now guarantees `semanticStale`'s assumption instead of the
  ledger merely hoping it holds (round 1, DUR-1.3).** `semanticStale`
  assumes every semantic change consumes a sequence number visible as a
  non-`TargetCall` lifecycle event. That assumption was previously false at
  the store level: `UpdateObligationVersion` (changing `Current`/
  `MaterializationDisabled`), `SetCurrentDirective` alone, and `PutTask`
  without a status change could all commit without allocating a `Seq` or
  writing any audit record — a preview planned before such a change would
  incorrectly pass `Prepare`/`MarkSent` as still current, breaking
  FR-CALL-001's "a stale preview fails with `ErrVersionConflict`" and T10
  step 2. The fix is a **store-wide semantic-write rule**, not a per-method
  patch: any transaction that performs a semantic write (any write other
  than `PutConversation`/`InsertCall`/`UpdateCall`/`PutCallAttempt`/
  `TargetCall` lifecycle events) must also write at least one record
  carrying a `Seq` allocated in that same transaction — an item,
  relationship, event record, obligation version or transition, grant, or
  non-`TargetCall` lifecycle event — or the commit fails
  `domain.ErrInvalidRecord`. This makes "did a semantic write happen" and
  "did a sequence number get allocated for it" the same question, which is
  exactly what `semanticStale`'s count comparison needs to be sound.
  `PutTask` now always requires its audit event for this reason (previously
  optional, non-status changes could skip it). Blobs are exempted from this
  rule: a blob is content-addressed and inert until a sequenced record
  references it, and the ledger itself stores response/audit blobs in
  ledger-only transactions that must not be forced to also carry a
  semantic write.
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
- **Service grant deferred; the owner-match floor is decided but not yet
  applied everywhere it should be (round 1, SPEC-1.2, unassigned as of this
  writing).** `CallRecord` freezes the inference `Principal` separately
  from `ServiceActor`, but Phase 1 does not yet implement §8's
  "conversation-specific service grant": `internal/invocation
  .checkServiceActor` requires the actor's authority be SYSTEM or HARNESS,
  and `checkActorScope` requires the actor's non-empty owner fields match
  the call's inference principal — an owner-match check, not a grant
  lookup. **This decision is not yet enforced on the reservation path
  itself:** `Prepare` calls only `checkServiceActor` (authority) and never
  `checkActorScope` (owner-match), while `MarkSent`, `RecordOutcome`,
  `Cancel`, and `Recover` all call both. A HARNESS actor scoped to task
  A/agent A can therefore successfully `Prepare` a reservation for task
  B/agent B's conversation; because later methods *do* check scope, the
  wrongly-scoped actor cannot itself drive or cancel what it reserved, so
  the reservation is stuck until task B's correctly-scoped actor happens to
  clear it (`ErrCallInFlight` in the meantime) — reproduced directly against
  this worktree. This is a real Phase 1 gate blocker, not a documentation
  gap; it is not currently assigned to a worker in the round-1 fix table
  and needs one. The fix is mechanical: apply `checkActorScope(req
  .ServiceActor, call)` in `Prepare` before any idempotent return or
  reservation write, matching every other ledger method. A dedicated
  `ActionDispatchCall` grant type (letting a narrower-scoped dispatcher
  drive a call it doesn't directly own, replacing the owner-match floor
  entirely) remains open for Phase 5.
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
  ledger against — provided it is actually applied everywhere, which
  SPEC-1.2 found `Prepare` currently is not (see above).
- **Constraining `next.Attempts` to equal `old.Attempts` (or +1 only on
  `PREPARED→SENT`) instead of trusting the evidence-gating rule alone.**
  Rejected as insufficient on its own (DUR-1.1): evidence-gating checks that
  *some* attempt in the right closed state exists, but without also pinning
  `Attempts` to the stored current value, a caller could satisfy that check
  using an *older* attempt's evidence while a newer attempt was still open.
  Both constraints are required together.
- **A per-method patch for the semantic-write rule (e.g. only fixing
  `PutTask`) instead of a store-wide rule.** Rejected (DUR-1.3): the same
  gap existed independently in `UpdateObligationVersion` and
  `SetCurrentDirective`; a store-wide rule (any semantic write commits with
  a sequenced record, blobs exempted) closes the whole class rather than
  the three instances found so far, and is the only formulation
  `semanticStale` can rely on going forward without re-auditing every
  store method again for the next one.

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
- **SPEC-1.2 is an open Phase 1 gate blocker with no assigned owner as of
  this writing.** Until `Prepare` applies `checkActorScope`, the owner-match
  floor this ADR documents as decided is not actually enforced on the
  reservation path, and a misconfigured or compromised dispatcher scoped to
  one task/agent can reserve — though not itself complete — another
  conversation's call slot. Flagging prominently rather than silently
  updating the Decision text to match the code, since the code is the thing
  that needs to change here, not the ADR.
- `LogicalCalls` advancing only on inference completions means usage
  accounting keyed to "completed logical inference index" must read
  `LogicalCalls`, not a raw completed-call count.
- The store-wide semantic-write rule (DUR-1.3) and the `Attempts`/terminal-
  immutability constraints (DUR-1.1) are breaking changes to the Phase 1
  store interface and any existing caller of `UpdateObligationVersion`,
  `SetCurrentDirective`, `PutTask`, or `UpdateCall`; no production data
  exists yet, so this is a clean signature/behavior change, not a
  migration, but every store implementation and conformance test must be
  updated together.

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

### Round 1 additions (findings DUR-1.1, 1.3, 1.8; SPEC-1.2)

The store contract changes for DUR-1.1/1.3/1.8 are merged in
`internal/store/store.go`, but `internal/store/memory`,
`internal/store/sqlite`, and `internal/store/storetest` do not currently
compile against the new signatures (`PutTask`'s event now required,
`Store.Sessions`, boundary-keyed `CurrentDirective`) — that implementation
and its tests are `memstore-worker`/`sqlite-worker`'s in-flight fix.
Required once landed:

- `internal/store/storetest`: a case rejecting `UpdateCall` where
  `next.Attempts != old.Attempts` on any transition other than
  `PREPARED→SENT`, and rejecting `PREPARED→SENT` unless `next.Attempts ==
  old.Attempts+1` (the exact DUR-1.1 reproduction: citing an older attempt's
  evidence while a newer attempt is still open must fail, not succeed); a
  case asserting **any** `UpdateCall` against an already-terminal call fails
  `ErrImmutable`, including a byte-identical same-state update and
  rewriting a COMPLETED call's `Outcome` — and that memory and SQLite agree
  (they previously didn't). A case for each of `UpdateObligationVersion`,
  `SetCurrentDirective`, and non-status `PutTask` alone (no accompanying
  sequenced record) failing `ErrInvalidRecord`, and passing when paired
  with one; a ledger-only transaction (conversation/call/attempt/
  `TargetCall` event writes alone, or a lone blob insert) succeeding
  without one.
- `internal/invocation`: a `TestPrepare*` regression for DUR-1.3 — seed an
  item and an obligation, `Prepare` a preview at the current `LastSeq`,
  change only `MaterializationDisabled` via `UpdateObligationVersion`
  (no accompanying item/relationship/event write), then assert `MarkSent`
  now fails `ErrVersionConflict` where it previously silently succeeded.
  A `TestRecoverAll*` suite: recovers SENT calls across multiple sessions
  from one `Store.Sessions` listing; a mismatched `actorFor` session is
  reported in the joined error and does not stop other sessions'
  recovery; an empty store recovers nothing without error.
- **Required, unassigned:** an `internal/invocation` test for SPEC-1.2 —
  `Prepare` with a `ServiceActor` scoped to a different task/agent than
  the conversation's inference principal must fail (matching what
  `MarkSent`/`Cancel`/`RecordOutcome` already enforce via `checkActorScope`),
  and must leave no reservation held afterward.

## Open questions

- Design of the Phase 5 `ActionDispatchCall` service grant: how a
  narrower-scoped dispatcher is authorized to drive a call for a principal
  it doesn't directly own, without weakening the current owner-match floor.
- Whether `CallAttempt.ProviderRequestID` is sufficient for FR-CALL-004's
  reconciliation mechanism, or reconciliation needs more provider-specific
  fields decided in ADR 9.
- **Needs an owner: SPEC-1.2** (`Prepare` doesn't apply `checkActorScope`).
  Round-1 fix assignments didn't name a worker for this finding; it should
  be picked up alongside `internal/invocation`'s other round-1 work
  (`ledger-worker`) since it's the same package and the same actor-scope
  helper the DUR-1.1/1.8 fixes already touch.

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

**Round 1 review** (PR #2; SPEC — Codex GPT-6, `spec-pr-comment-round1.md`;
DUR — Claude Opus, `dur-review-round1.md`). DUR-1.1 (HIGH): `UpdateCall`
judged evidence by the *new* record's `Attempts` with no constraint tying
it to the stored current attempt, and terminal calls were only immutable
by convention, not enforcement — reproduced as a forkable conversation and
an outcome-rewrite that the two stores even disagreed on; fixed by pinning
`Attempts` to `old.Attempts` (+1 only on `PREPARED→SENT`) and making every
`UpdateCall` on a terminal call fail `ErrImmutable`. DUR-1.3 (MEDIUM):
`semanticStale`'s "every semantic change gets a sequence number" assumption
was false for three store methods; fixed by a store-wide semantic-write
rule instead of patching each one. DUR-1.8 (LOW): startup recovery had no
way to discover which sessions to recover; fixed by `Store.Sessions` +
`Ledger.RecoverAll`. **SPEC-1.2 (HIGH, newly found, currently unassigned):**
`Prepare` never applies the owner-match check (`checkActorScope`) this ADR
documents as the Phase 1 floor, unlike every other ledger method — I
reproduced this directly against the merged worktree (a HARNESS actor
scoped to task A/agent A can `Prepare` task B/agent B's reservation) and
recorded it in the Decision, Consequences, and Open questions rather than
silently editing the Decision text to match the unfixed code. DUR-1.2,
1.4, 1.5, 1.6, 1.7 (commit-cancellation semantics, error mapping,
re-entrancy, concurrent `Open`, interrupted migration) are ADR 3's scope,
not this ADR's.
