# Phase 3 contract seed and schema manifest (P3-41)

Status: **seed contract**, 2026-09-26. Implements the FROZEN commander decisions and
accepted W2–W7 requests. This is not Phase 3 gate acceptance. The Go field sets at
this branch head are the migration inputs; W2 owns forward migrations after 0017.
Never edit 0001 or import live canonicalizers into a frozen migration step.

## Transaction and backend handoff

New services accept `(store.Tx, domain.Principal, Intent, seq uint64)` and return an
immutable typed result. Only outer entry points open `Store.Update`. Allocate the
actual sequence before authorization; `graph.AuthorizeAtSequence` rejects predicted
or unallocated sequences and uses exact targets plus indexed `GrantsFor` reads.
Services poison the transaction on an error after any constituent write. Guard
also poisons an ignored write error after a successful write, across both APIs.

`store.Semantic(tx)` exposes the frozen **SemanticTxBase** backend facet through a
shared poison guard. W2 implements `SemanticBackend()` on writable transactions and
`SemanticReadBackend()` on read snapshots. Transaction/fault-injection wrappers
forward `SemanticTransaction()` and preserve the same transaction. This facet keeps
Phase 2 backends compilable until their migrations land; absent support fails with
`ErrUnsupportedSchema`. It supplies `SetCurrentVersion(itemID, expectedPriorItemID)`;
the old one-argument `Tx` method is legacy-only, not a Phase 3 CAS substitute.
There are no successful persistence stubs and no backend acceptance claims here.

Every new persisted companion embeds `SemanticMeta` and exposes `Validate`, `Clone`,
and `SemanticSeq`. Every immutable insert is session-scoped and append-only. Every
introduced sequence must be allocated in the current transaction and must not be
shared with a TargetCall audit. Every mutable update uses an explicit expected
revision; the backend assigns the next revision. A missing pointer is revision 0.
Coverage members share their parent's creation sequence and are inserted atomically.

All list methods in `semantic_*.go` take `Page` or a positive hard limit. A page has
`Records`, `More`, and `Next`; required fan-out exhausts every page or aborts with a
resource-limit error. Access filtering precedes limits on viewer-facing reads.
All-owner completion/invalidation reads are internal and their IDs/counts never
appear in denied outward results. Default order is `(Seq, ID)`; checkpoint lookup
is descending. Run ordinal equals its pre-execution registration sequence.

## Persisted record manifest

W2 enforces every key, reference, immutability, sequence, CAS, and atomicity rule
below in **both** backends. The last column owns authenticated intent, authority,
semantic completeness/applicability, and outward redaction. W1 owns all types.

| Record / storage key within session | Required structural references and indices | Semantic owner |
|---|---|---|
| CreationDeclaration / ItemID | Unique item; key/authority/content/defaults match immutable creation; policy/signature; legacy absence is unknown | W1 graph, W7 creation |
| SnapshotDeclaration / ID | Ordered item/declaration/signature members, authority/task/boundary partition; signature ignores occurrence IDs | W1 graph |
| CoverageRecord / ID | Purpose/schema/signature/count; boundary; optional conversation/revision/frontier | W1 graph, W5/W6 producers |
| CoverageMember / (CoverageID, member key) | Exact item/content, lease/source, nested coverage, or exchange; same session/sequence; sorted unique; reverse source index; reject cycles | W1 graph, W5/W6 completeness |
| ObligationDeclaration / exact ObligationRef | Source, declaration slot, matcher/target/workspace versions agree with version's immutable binding fields | W4 |
| ApplicabilityProof / ID | Exact obligation/target hash, satisfying transition, sorted evidence, coverage, observation or assertion, dependency IDs; boundary | W4 |
| ProofDependency / ID | Proof plus resource/revision and workspace fingerprint or canonical path/content hash; resource/path/ALL index | W4 |
| AssertionRecord / ID | Exact obligation/transition, actor, mode, citations, optional resource proof; ATTESTATION cannot carry a resource proof | W4 |
| TransitionDetail / TransitionID | Exact version, immutable history row, old/new proofs, cause row, historical authorization; append with status/cache CAS | W4 |
| ResourceBinding / ResourceID | Immutable owner/reporter identity and access; unique registration; reporter does not acquire target read access | W4 |
| ResourceState / ResourceID (CAS) | Binding and LastUpdateID; monotonic authoritative revision; KNOWN requires fingerprint, UNKNOWN forbids one | W4 |
| ResourceUpdate / ID | Request, reporter, prior/result revision, canonical changed paths or ALL; gaps become UNKNOWN | W4 |
| ResourcePathState / locator key (CAS) | Resource update/revision plus canonical resource/base/path and current content or explicit unknown | W4 |
| WorkspaceBinding / (ID, Version) | Registered resource, exactly one SOURCE/TASK/CONVERSATION context, canonical base, opaque environment/suite/coverage specs | W4 |
| ObservationRun / ID and (subject, Ordinal) | Execution identity, trusted reporter, exact binding version, subject/task/boundary; Ordinal = Seq | W4 |
| ObservationRecord / occurrence ID | Existing run/execution/binding; exact TOOL evidence and boundary; typed outcome/completeness/counts; reporting matcher version | W4 |
| SubjectState / (subject, task, boundary) (CAS) | Current TOOL observation item, accepted run ordinal, observation and applicability; resource index | W4 |
| LogicalExchange / ID, (conversation, ordinal) (CAS) | Immutable principal/turn/conversation; legal open/executing/closed/cancelled state; acknowledgment for closure | W5 membership |
| ExchangeMember / ID, (exchange, position) | Exact source content; input/output/call/result role; completed output and tool-call association; reverse item index | W5 membership |
| ExchangeAcknowledgment / ID | Successful consuming inference + manifest, or explicit cancellation with no fabricated consumption/coverage | W5 membership |
| AdmissionManifest / ID | Holder/turn/exchange/call, purpose, exact source and nested lease coverage, membership revision | W5 membership |
| ConversationMembershipState / conversation (CAS) | Last ordinal, monotonic revision, complete acknowledged closed prefix; independent of provider Conversation revision | W5 membership |
| Checkpoint / ID and ItemID | CHECKPOINT-role item; source coverage and distinct closed-exchange coverage; generation manifest; issuing/open round excluded; prior checkpoint chain | W5 |
| OwnerRegistration / (kind, owner ID) | Trusted registration/source and optional workflow; immutable session-lifetime association | W3/W7 |
| RetrievalLease / ID | Exact holder including authority, conversation/turn, immutable source/content, issued logical-inference index, finite allowance/policy; holder/source indices | W6; liveness solely W3 policy |
| RetrievalResult / ID | Exact lease/projection/event and RetrievalOrigin; ValidateOriginEvent binds holder/request/source/result; invocation required for AGENT, absent only for authenticated HARNESS; frozen observed revision/status | W6 |
| ProjectionRecord / ID and ItemID | TOOL PROJECTION item; exact source/lease/dependency coverage; Origin equals referenced retrieval result's Origin (including invocation presence/fields); intersected access and registered delivery policy | W6 |
| RetrievalEvent / ID | Principal/trigger/invocation/request; success references result/source; denial carries no source identity or count | W6 |
| MutationReceipt / (Family, RequestID) | Exact principal/method/canonical arguments/hash schema/policy and typed immutable result; referenced result records exist | W3/W4/W5/W6 |
| ToolExecutionReceipt / composite invocation ID | Completed logical output/call + tool-call + conversation, authenticated principal, mutation receipt and frozen result | W5 |
| GCRequest / ID, pending index | Stable trigger/request/scope/origin/policy; append-only; no receipt means pending | W3 |
| CollectReceipt / ID | Frozen snapshot/candidates/decisions/archived revisions in deterministic order; exact authenticated collector | W3 |
| GCResult / GCRequestID | Unique request-to-collection receipt association, atomic with collection effects | W3 |
| SemanticChange / ID, target index | Exact target/source authority, old/new revisions/status/currentness, actual actor, audit and immutable cause | W1/W3/W4/W7 |

CompletionReceipt, MutationResult, ToolResult, OperationResult, OutcomeBinding,
ObservedItemState, and typed references/intents are **nested values**, not independent
tables. Source item content and source ranges retain their existing immutable stores.

## Existing rows and legacy treatment

- ContextItem adds explicit Namespace plus CHECKPOINT/PROJECTION roles. Migrate old
  DIRECTIVE/AGENT_KEY identities unchanged. OBSERVATION is new; never infer it from
  Section=None. New semantic inserts require `ValidateSemantic` and role companions.
- Relationship adds CoverageID, mutually exclusive with legacy Coverage. Legacy
  coverage never gains a new purpose, membership proof, or complete frontier.
- MutationGrant adds decoded Targets with canonical keys. Stable-ID obligation
  grants remain inert for exact-version authorization. Item grants retain occurrence
  identity. New matcher grants target only obligation versions and AssertObligation.
- ObligationVersion adds immutable declaration/binding/target fields and proof/assertion
  caches. ObligationTransition adds closed cause, assertion mode, proof/prior proof,
  cause record, historical authorization, request/reason/rationale. New transitions
  reject legacy Fingerprints. Status/history/detail/caches update atomically.
- Legacy Matcher=nil stays unbound. Unknown assertion freshness stays unknown.
  Reconcile unverifiable matcher satisfaction once via a checksum-pinned, atomic
  SYSTEM `UPGRADE_RECONCILIATION` transition; preserve original evidence/history.
- EventRecord/Envelope/IngestReceipt store RequestHashVersion independently. V1
  envelope/receipt zero means frozen v2; envelope-less legacy event identity is
  `unknown` and cannot authorize replay. New envelopes/receipts are v2 with v3 hashes.
- LifecycleCommand v1 stays PARSED_NOT_EXECUTED. V2 execution outcome, result,
  grants, revisions, and target-dependent diagnostics are all under DetailAccess.
  Outside it, success and nonexistence have the same WITHHELD representation.
- Strict nested lossless leaves require frozen forward rewrite steps for changed
  items, grants, events, operations, receipt snapshots and command details.
- No migration invents creation identity, proof applicability, resource baselines,
  workspace bindings, run order, membership, owner liveness, leases, or successful
  semantic receipts. **Superseded by migration 0034 (PR #6 round-1 review,
  SPEC-1.3/SPEC-2.5): legacy declaration absence is reconciled to a known
  declaration where the ingest receipt snapshot establishes a pre-upgrade
  keyed item's creation identity, and remains unknown (fails closed) only
  where it does not. This seed document is not otherwise updated after the
  contract freeze; see ADR 3's Phase 3 amendment for the authoritative,
  current migration list.**

## PR #6 round 3 additions (DUR-3.1/DUR-3.2; added by explicit commander
request, an exception to the "not otherwise updated" note above — ADR 3's
own migration bullets for 0045-0047 remain the authoritative, current
list)

These are internal lookup/index tables and a policy field, not persisted
domain records, matching how migration 0012's `lookup_canonical`/
`lookup_working`/`lookup_source`/`lookup_blob` tables were never added to
the "Persisted record manifest" above either:

- `lookup_live_proof_path` (`session_id, resource_id, key, seq, proof_id`)
  — DUR-3.1 (A): each live proof's exact-path/ancestor-directory `CURRENT_PATH`
  keys, and its `WORKSPACE` key (`"ws"`), so a report reads only the
  proofs it can affect.
- `lookup_live_dependents` (`session_id, resource_id, dependents`) —
  the live non-`FIXED_CONTENT` dependency-row count per resource, the
  cap `Phase3Policy.MaxLiveProofDependents` used to validate against.
  **K1 (round 4, landed) retires that validation entirely** (below): the
  table is kept only as an unused write-time metric, not read by any
  correctness path.
- `Phase3Policy.MaxLiveProofDependents` (int; default 256) — an added
  field on the recorded policy, persisted on `rec_envelope`/`rec_receipt`
  (migration 0046) so P3-40's exact historical retry keeps validating
  under the policy it was recorded with. **K1 (round 4) stops validating
  or using this field at all** (`Phase3Policy.Validate`,
  `internal/domain/semantic.go`); it stays recorded for that same replay
  reason, never re-added to any check.
- `lookup_pending_gc_trigger` (`session_id, trigger, seq, request_id`) —
  DUR-3.2: pending `GCRequest`s indexed by trigger, so a collector never
  pages a disabled trigger's requests.
- `gc_queue_cursor` (`session_id` PK, `cursor_seq, cursor_id, revision`)
  — DUR-3.2: each session's CAS-written, durable scan position,
  replacing an earlier in-process cursor that a restart or a new service
  instance used to reset.

## PR #6 round 4 addition (K1; migration 0048, landed — same exception as
above, ADR 3's own 0048 bullet remains authoritative)

Internal lookup/index tables implementing ADR 8's K1 A1/A4 (derived-at-read
proof validity), replacing the round-3 cap mechanism above rather than
building on it:

- `lookup_workspace_divergence` (`session_id, resource_id, revision,
  update_id`) — K1 A1: each resource's monotone divergence raises (lost
  freshness or a changed workspace fingerprint), read as
  `LastWorkspaceDivergenceRev`/`FirstWorkspaceDivergenceAfter`.
- `lookup_affecting_raise` (`session_id, resource_id, path_key, revision,
  update_id`) — K1 A1: the `(resource, key)` raises, `path_key` `"all"`
  for UNKNOWN/all-paths reports or `"path:"` plus the hex of a changed
  path (migration 0033's keys), read as
  `LastAffectingRev`/`FirstAffectingUpdateAfter`.
- `lookup_live_proof` (`session_id, seq, proof_id`) — K1 A4: every live
  proof (the current proof of a current obligation version) in `(Seq,
  ID)` order, the SYSTEM async settlement worker's scan input.
- `settlement_cursor` (`session_id` PK, `cursor_seq, cursor_id,
  revision`) — K1 A4: the session's CAS-written audit scan position,
  unsequenced operational state like `gc_queue_cursor` above, never
  evidence a proof was settled.

## PR #6 round 5 addition (SPEC-5.2; migration 0050, landed at
integration head `b8efc67` — same exception as above, ADR 3's own 0050
bullet remains authoritative)

`rec_gc_progress` is W3's internal GC batch-progress row — operational
state like `gc_queue_cursor`/`settlement_cursor`, never a persisted
domain record, which is why it does not appear in the "Persisted record
manifest" above; migrations 0041-0044 added its J1-J7 cursor fields, and
0050 is its first change since:

- `rec_gc_progress` adds `f_viewer_session_id`, `f_viewer_workflow_id`,
  `f_viewer_task_id`, `f_viewer_agent_id`, `f_viewer_authority` —
  `domain.GCProgress.Viewer`, the principal whose visibility paged batch
  1 (SPEC-5.2). Every later batch of the same request, including a
  continuation by a different authorized same-task collector (SEC-4.5),
  pages the same frozen candidate set (J1/J2); per-target Archive
  authorization stays with each batch's executing collector (P3-38), and
  a frozen candidate that collector cannot read gets an explicit
  INELIGIBLE decision rather than vanishing. Rows written before 0050
  backfill lazily from batch 1's committed collect receipt
  (`firstBatchReceipt`, `internal/lifecycle/gc_requests.go`) — no SQL
  backfill runs, and an upgraded request keeps exactly the candidate set
  its first batch saw. 0050's numbering skips 0049, reserved for the
  K1-api.3 confirmation-record migration on a sibling round-5 branch
  (ADR 3).
