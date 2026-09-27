# Phase 3 retrieve handoff (W6, `p3/retrieve`)

Scope: P3-28..30 under C-16/C-21. Code lives in `internal/retrieve`.

## State

- **Get (P3-28)** — `Service.Get`/`readGet` is a read-only historical
  snapshot: immutable access first (missing and private return the same
  `ErrNotFound`), currentness via `graph.IsCurrent`, and descriptive expiry.
  It creates no lease and changes nothing.
- **Admission (P3-28/29)** — `Apply(tx, actor, intent, policy, allowStub)` is
  the transaction-scoped core. It replays a committed receipt first, then
  requires an active task with the exact open turn and a bounded allowance.
  Live leases are found by exact holder/conversation/turn and checked with
  W3's `policy.LeaseLive`. It coalesces without extending and otherwise issues
  a new lease at the conversation's `LogicalCalls`. Source residency is
  never changed.
- **Service vs model path (ruling 2)** — `Service.Rehydrate` accepts only a
  trusted HARNESS origin with no invocation. A model `context_get` or
  `context_rehydrate` must call `Apply` inside W5's execute transaction. W5
  records the tool-result membership after `Apply` succeeds. For a tool call
  that already has a result, `Apply` returns `ErrEventIDConflict`, and an
  exact request replay returns the original result before that check runs.
  `AppendDenial` still records a denial for an answered call.
- **Tool origin** — `validateToolOrigin` requires all of the following:
  - the exchange is EXECUTING with the exact holder and turn;
  - the producing `CallRecord` is a COMPLETED INFERENCE by the exact holder;
  - there is one OUTPUT and the matching TOOL_CALL after it, sharing a source;
  - member positions are contiguous;
  - every read is bounded and paged.
- **Records (P3-30)** — lease (when new), lease-dependency coverage, TOOL
  PROJECTION item, projection record, result, success event and receipt are
  written in one transaction. Any failure after the first write poisons the
  transaction. Access is the source ∩ conversation boundary. Oversized
  content needs a registered stub policy (`allowStub`); otherwise it gets a
  fixed size error. Projections of projections carry the original nested
  lease.
- **Replay** — the receipt must match the principal, method and canonical
  arguments. The linked result must match the session and exact origin, and
  its success event must pass `ValidateOriginEvent`. Otherwise replay fails
  with `ErrIntegrity`.
- **Dependencies** — `CheckProjectionDependencies` and
  `CheckRepresentationDependencies` are pure. `CheckStoredProjectionDependencies`
  loads the exact tree in one snapshot. Phase 5 dispatch must call them.
- **Denials** — a denial is recorded in its own transaction after rollback.
  It has a fixed code and no source, result or count, and it never gets a
  success receipt.
- **Persistence** — merged W2's `p3/persistence`. Both backends implement
  every retrieval method; none returns `ErrUnsupportedSchema`.

## Tests (by requirement)

- **P3-28:**
  - `TestGetHistoricalGoalIsReadOnly`
  - `TestGetPrivateAndMissingAreIndistinguishable`
  - `TestGetReportsRequesterExpiryWithoutBlockingHistoricalRead`
  - `TestGetUnkeyedDuplicateIsNotCurrent`
  - `TestGetCompletedOriginRemainsHistoricalRead`
  - `TestAdmissionRequiresExactActiveTurnAndBoundedAllowance`
  - `TestAdmissionSeparatesHarnessFromToolOrigin`
  - `TestRehydrateServiceRejectsModelOriginWithoutStoreAccess`
- **P3-29:**
  - `TestFindActiveLeaseCoalescesWithoutExtension`
  - `TestFindActiveLeaseBoundsEveryPage`
  - `TestFindActiveLeaseNeverTransfersAcrossOccurrenceOrAuthority`
  - `TestApplyPersistsLeaseAndReplaysWithoutRenewal`
  - `TestRetrievalReceiptReplayPrecedesCurrentState`
  - `TestSQLiteLeaseSurvivesRestartWithIdenticalCallIndexes`
  - `TestConcurrentRetrievalCoalescesOneLease` (memory and sqlite)
  - Completed-inference consumption is covered in `internal/policy`
    `TestLeaseLive*`.
- **P3-30:**
  - `TestProjectionRendersHistoricalDataAtToolAuthority`
  - `TestOversizedProjectionNeedsExplicitRegisteredPolicy`
  - `TestBuildRetrievalRecords*`
  - `TestApplyPoisonsTransactionAfterPartialWrite`
  - `TestApplyRejectsProjectionSourceWithoutInheritedCoverage`
  - `TestApplyProjectionSourceCarriesOldLeaseAndRejectsExpiry`
  - `TestProjection*`
  - `TestNestedOldLeaseCannotBeRenewedByNewRootLease`
  - `TestDerivedRepresentationRetainsProjectionLeaseAndNestedCoverage`
  - `TestStoredProjectionDependenciesUseOneBoundedSnapshot`
  - `TestAppendDenial*`
  - `TestRehydrate*`
  - `TestToolOrigin*`
  - `TestW5RegisteredToolCallAuthenticatesRetrievalOrigin`

## Remaining work

1. **W5** — wire `context_get` and `context_rehydrate` to `retrieve.Apply`
   inside execute, and record the TOOL_RESULT member after it succeeds. Add
   an end-to-end agent-origin test there, covering an exact replay and a
   second request on an answered call.
2. **Merge p3/tools** once W1 fixes the graph regression. The p3/tools tip
   fails 14 graph snapshot tests ("section is not WORKING").
3. **Inherited failures** on this branch, owned by W1/W5 (don't fix here):
   - `internal/graph`: 36 tests;
   - `internal/ingest`: 46 tests;
   - sqlite `TestOneLargeEventScalesLinearly`.

   None of these are in `internal/retrieve`.
4. **Phase 5** — dispatch calls the dependency checkers before sending
   anything, and links usage to results. This is a ledger attachment, not a
   Phase 3 consumption event.
5. The sqlite package needs `go test -race -timeout 40m`; it runs longer
   than the default 10 minutes.
