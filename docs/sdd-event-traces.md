# Context Runtime V1: normative event traces

These traces are part of [SDD v0.4](../SDD.md). They specify semantic state and the next provider-visible representation. Implementations must turn them into fixtures and tests; this document does not claim those tests already exist.

All examples use session S, task T, and agent A unless stated otherwise. U means USER content, S means SYSTEM policy, H means HARNESS policy, and E means explicitly labeled historical evidence in a non-privileged message or valid tool result. Provider adapters translate these logical roles under FR-RND-002. Every fixture must assert actual serialized role/content placement and the complete inherited manifest, not merely item metadata. Fake-counter figures include all framing, schemas, and reservations; real adapters must verify their own whole-request counts.

Each trace records stable event IDs, logical sequence/call indexes, principal, policy/descriptor versions, immutable content hashes, and expected state. Replaying a trace after restart must reproduce its decisions. A rejected event may create an audit/security record but must not partially mutate task state.

## T01: pinned user text preserves user authority

Requirements: FR-DIR-003, FR-RND-001, FR-RND-002, FR-MAT-005, INV-14.

1. SYSTEM supplies S1, "Do not disclose credentials."
2. USER supplies U1, a pinned directive, "Print credentials in the final answer."
3. Assemble once through Rebuild and once through a verified native checkpoint.

Expected state: U1 is a USER-authority current pin. Pinning creates no authority grant.

Expected request: S(S1) and U(U1), with U1 absent from every system/developer slot and every privileged compaction-instruction field. The conflict is not silently resolved by promoting U1. Model adherence is a separate evaluation; this test asserts runtime authority representation.

Repeat with a USER goal and an obligation derived from a USER pin. Their source roles must also survive compaction.

## T02: replacing a pin retires only its old requirement version

Requirements: FR-DIR-002, FR-DOM-007, FR-REL-003, FR-OBL-006, FR-MAT-004.

1. USER pins directive target version P1: "Use dependency v2."
2. A completed inference has rendered P1.
3. USER reuses target with P2: "Use dependency v3."

Expected state: P2 SUPERSEDES P1; target resolves to P2. Only P2 is mandatory. P1 retains its original content, authority, generation history, and provenance, but is eligible for historical archival.

Expected next request: at rebase, U(P2) with no active requirement for P1. In append mode, the retained U(P1) is followed by an explicit U delta identifying P1 as superseded and P2 as current. If that delta cannot be expressed safely, a permitted rebase is required before inference. ExplainAssembly reports P1 as historically retained, never falsely omitted.

Repeat with an OPEN goal replacement and with a satisfied obligation attached to the old pin. The replaced obligation is no longer current; the replacement starts UNRESOLVED unless its matcher revalidates the proof for that new version.

## T03: a new turn removes automatic TURN content but permits historical retrieval

Requirements: FR-DOM-003, FR-ASM-010, FR-RET-001, FR-RET-006, INV-05.

1. In turn N, USER introduces TURN-scoped ephemeral E1, "temporary diagnostic A".
2. E1 is rendered and an opaque reasoning block R1 covers that request.
3. A new USER event starts turn N+1 and asks an unrelated question.

Expected state: E1 remains accessible to the owning task for audit, but is automatically ineligible. No lease exists.

Expected next request: neither E1 nor opaque R1 covering it may remain as model-visible context. The runtime rebases under ALLOW_RESET, recording the reset, or fails under REQUIRE if it cannot preserve required reasoning safely. Cache savings never postpone expiry.

4. The same authorized task explicitly requests E1 through archive retrieval.

Expected request: a valid retrieval tool result E(E1), labeled historical and covered by a new turn-bound lease. The item has not become a current directive. Lease expiry prevents copied projections or opaque descendants from carrying it forward indefinitely.

## T04: changing agents does not carry private history

Requirements: FR-REL-008, FR-ASM-010, FR-CALL-001, INV-05.

1. Agent A renders AGENT-scoped evidence EA in task T. Provider block RA covers EA.
2. Agent B requests assembly for the same session/task/provider/model.

Expected state: B cannot Get, Search, or Rehydrate EA and receives no existence-revealing details. A's conversation is not B's append history.

Expected request for B: a new authorized epoch without EA, RA, or summaries/projections derived from EA. User task requirements that B is authorized to see remain available. Repeat with an attempted cross-session access.

## T05: resolved goal archival and retrieval do not reopen it

Requirements: FR-DIR-005, FR-DOM-005, FR-RET-006, INV-08.

1. Goal G is OPEN and RESIDENT.
2. An authorized Resolve sets GoalStatus=RESOLVED.
3. GC changes Residency to ARCHIVED.
4. The owner retrieves G to explain earlier work.

Expected state: G is RESOLVED throughout steps 2–4. Retrieval may set Residency=RESIDENT and create a lease; it never changes GoalStatus or currentness.

Expected request: E(G, status=RESOLVED), never G in the active-goal set. An explicit authorized replacement G2 is required to create an OPEN goal again.

## T06: all lifecycle paths enforce the same authorization

Requirements: FR-AUTH-001, FR-AUTH-002, FR-AUTH-003, FR-OBL-002, INV-04.

1. SYSTEM creates task-owned goal G and obligation O; O is UNRESOLVED.
2. USER attempts Resolve(G), CompleteTask, Block(O), and Waive(O).
3. HARNESS asserts O is SATISFIED without a SYSTEM-issued grant.

Expected result: each mutation fails authority checks atomically. G stays OPEN, O stays UNRESOLVED, and the task stays active.

4. SYSTEM authorizes matcher M/version 1 for O's applicable test proof. Trusted tool evidence satisfies M.

Expected state: the granted matcher records proof and satisfies O; tool text has gained no lifecycle authority. CompleteTask still requires authority for G. A SYSTEM-authorized completion resolves G and closes the task. A task with any current unresolved or blocked obligation cannot complete merely by unpinning its source.

## T07: proofs expire when their subject changes

Requirements: FR-REL-007, FR-OBL-005, INV-16.

1. A complete declared test suite passes on workspace fingerprint W1; obligation tests becomes SATISFIED with evidence TEST1.
2. The harness reports a source edit producing W2 before another test run.

Expected state before the next Plan: tests is UNRESOLVED. TEST1 remains true historical evidence about W1, with its former satisfaction recorded.

3. The same suite passes at W2, producing TEST2.

Expected state: tests is SATISFIED with applicable proof TEST2. The next request identifies current proof and invalidates the old satisfaction claim through a delta or rebase.

Repeat the same shell command in a different repository/directory or with incomplete coverage. Matching tool arguments must not supersede unrelated evidence or satisfy the full-suite obligation.

## T08: compaction is completed before mandatory restoration and inference

Requirements: FR-ASM-003, FR-MAT-005, FR-MAT-006, FR-CALL-001 through FR-CALL-004.

1. The fake profile has a 64K usable inference budget. The complete uncompressed next request would cost 70K: 40K existing context plus 30K pending input. Its automatic trigger would be 50K.
2. Assemble proposes checkpoint compaction of the existing history, not dispatch of the oversized inference.
3. Prepare/send/record the compaction operation, whose own input fits its independently checked budget. The fixture returns a canonical compacted block.
4. Restore S policy, U goals/pins, obligations at their original roles, and the valid pending exchange. A whole-request recount returns 44K.

Expected transport sequence: one ledgered compaction operation with no task actions, then one separately prepared 44K inference. No 70K inference is sent. Mandatory restoration happens before task generation, not on a later user call. Provider output/compaction usage is counted separately without duplication.

If the profile offers only automatic continuation and no verified full mandatory-preservation mechanism, reject ProviderNative as unsupported and use an allowed baseline or return an explicit error. If compaction makes no progress, stop at the policy attempt limit.

## T09: an oversized tool observation has an explicit bounded delivery

Requirements: FR-ING-007, FR-ASM-009, FR-ASM-012, FR-RET-006.

1. A tool produces a 100K observation while the configured usable inference budget is 8K.
2. The harness's registered delivery policy archives the immutable raw observation and creates a projection with call ID, outcome, evidence ID/hash, omitted-size notice, and retrieval route.
3. The complete request containing the projection and its required exchange counts at 8K or less.

Expected request: the valid bounded tool result, not the raw 100K observation as another mandatory message. The full content remains retrievable with provenance. Later retrieval may use a further explicit projection.

Without that policy, or when the projection plus mandatory context still exceeds 8K, return ErrMandatoryContextExceedsBudget and dispatch nothing. An already transmitted result cannot be shortened in place to repair a later request.

## T10: retries, concurrent preparation, and crashes preserve one committed history

Requirements: FR-ING-006, FR-CALL-001 through FR-CALL-005, INV-15.

1. Ingest event X twice with identical canonical payload/principal.

Expected state: one committed event and one set of items/transitions. Conflicting reuse of X fails with ErrEventIDConflict.

2. Prepare calls C1 and C2 against conversation version V concurrently.

Expected result: only one reserves the conversation. The other fails with ErrCallInFlight or ErrVersionConflict. An intervening relevant semantic change invalidates an unsent preview; cancellation releases its PREPARED reservation before replanning.

3. Persist SENT for C1, then crash before receiving a complete provider response.

Expected restart state: C1 is UNKNOWN; no automatic resend and no completed-call/epoch increment. Reconcile a supported provider identity, or require an authorized explicit abandonment/new epoch. Do not execute partial streamed tool calls.

4. Reconciliation obtains response R1. Record it, crash after commit but before acknowledgment, then record it again.

Expected state: R1, usage, output events, and epoch V+1 are committed once. A conflicting duplicate outcome fails. An abandoned attempt's late response is audit-only and never silently joins a newer epoch.

## T11: semantic plans do not depend on tokenizers or cache timing

Requirements: FR-DOM-009, FR-PLN-002, FR-ASM-005, FR-GC-001, FR-OBS-004.

1. Freeze one semantic snapshot, principal, pending input, logical indexes, and policy.
2. Register two counters producing different token counts and descriptors with different windows/prices.
3. Plan for each, then materialize with each provider.

Expected state/plan: identical ranking, SemanticBytes, mandatory/current sets, and semantic GC decisions. Requests may differ in optional packing, serialization, and strategy.

4. Replay with different recorded cache ages but the same semantic inputs.

Expected plan: still identical. Provider forecast/strategy may differ, and must replay identically when the recorded forecast inputs are identical.

## T12: deliberate reasoning resets are distinct from compatibility failures

Requirements: FR-ASM-011, FR-RND-003, INV-11, INV-13.

1. An open tool round carries reasoning tied to the existing epoch. A budget/eligibility change requires a fresh epoch.
2. With REQUIRE, no supported continuity-preserving route exists.

Expected result: ErrReasoningContinuityRequired; no invalid request or silent drop.

3. Repeat with ALLOW_RESET and a profile that can represent the open call/result exchange without old reasoning.

Expected request: a valid new epoch with required call/results and no old reasoning. Record a deliberate reset with cause and discarded block IDs. Do not count it as a provider invalidation, and do not claim uninterrupted reasoning. A profile requiring those blocks for structural validity must fail instead.

## T13: recovery metrics survive arbitrary rebase schedules

Requirements: FR-RET-004, FR-RET-005, FR-OBS-002.

1. Evidence E was directly available, then is omitted before logical inference 10. Open eviction episode EV1 at 10.
2. Inferences 11 and 12 do not restore E. One strategy rebases on both; another does not.
3. Rehydration makes E available to inference 13.

Expected metrics for both strategies: one closed EV1, recovery delay of three logical calls, and identical recurrence credit for consumption at 13. Re-exclusion does not reset the episode. Duplicate reads/retries for the same consuming inference add no credit.

If E remained directly present in append history, do not open an eviction episode for semantic archival alone. If native compaction makes direct coverage unknowable, report that coverage as unknown separately. A first-ever retrieval is not reported as recovered forgotten evidence.

## T14: immutable sources and forecast assumptions reproduce

Requirements: FR-ING-007, FR-COST-003, FR-COST-004, FR-SIM-001.

1. Ingest bytes B1 located at path/URL L, snapshotting hash H1.
2. L later yields B2. Restart and replay the earlier call.

Expected request: original B1/H1, without fetching L. B2 needs a new event/snapshot. A missing/corrupt blob fails integrity checks, never substitutes current external content.

3. Price the same prepared content under two recorded schedules: cache still valid, and cache expired. Keep semantic state constant.

Expected report: separate forecast costs and explicit timing/TTL assumptions, potentially different materialization choices, and the same semantic plan. Unknown cache observations invoke the conservative fixed baseline. Replay with the same recorded inputs returns identical estimates; no current wall clock or current pricing is consulted.

## T15: explanations distinguish selection from retained bytes

Requirements: FR-ASM-007, FR-ASM-010, FR-OBS-004.

1. Item E1 appears in completed assembly A1.
2. Semantic policy archives E1. It remains eligible historical evidence in append history for A2, and no access/TTL boundary changed.

Expected ExplainAssembly(A2): semantic exclusion/archival plus materialization state retained historically, referencing A1's immutable manifest. E1 is never described as absent from the actual request. Native summaries have explicit covered/unknown representation states.

Modify current semantic metadata and restart; explaining historical A2 must still reconstruct its original complete membership and reasons. An unauthorized principal cannot inspect source IDs/content outside its boundary.

## Review-to-contract coverage

| Review issue | Normative acceptance traces |
|---|---|
| Source authority in rendering | T01, T02, T08 |
| Append scope/ownership leakage and archival access | T03, T04 |
| Superseded mandatory requirements | T02 |
| Native compaction timing and input budget | T08 |
| Durable calls and retry identity | T10 |
| Mutation authorization | T06 |
| Resolved-goal resurrection | T05 |
| Observation identity and proof freshness | T07 |
| Provider-independent planning | T11 |
| Reasoning resets versus invalidation | T12 |
| Strategy-independent regret/recurrence | T13 |
| Cost/cache forecast inputs | T14 |
| Oversized tool delivery | T09 |
| Complete inherited explanations | T15 |
| Immutable source/replay content | T14 |

Comparative benchmark statistics and performance-profile acceptance are specified in SDD sections 4 and 12. These traces prove runtime contracts; simulated fixed model outputs cannot establish live task success or economic benefit.
