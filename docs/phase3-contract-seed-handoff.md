# Phase 3 contract seed handoff

Branch: `p3/contracts`. Base: `b5f6b1f`. Implementation tip: `f81023f`.
This freezes the accepted W2–W7 contract fields and encodings for their implementation.
It is a dependency seed, not completed Phase 3 behavior or backend acceptance.
The schema and enforcement manifest is [phase3-schema-manifest.md](phase3-schema-manifest.md).

## Integration instructions

- W2 implements the `store.SemanticTxBase` facet and read provider on the SAME
  transaction/snapshot as the legacy API. See `internal/store/semantic.go`.
  New writes share Guard's poison state. Unsupported facets fail closed.
  Persist every manifest row, every new existing-row field, and strict nested
  leaves through frozen forward migrations. Keep `0001` unchanged.
- W3 uses `graph.AuthorizeAtSequence`, exact typed targets, paged all-owner goal,
  obligation, exchange and call queries, and immutable completion/GC receipts.
  W3 owns the single pure lease-live predicate in policy; domain defines its data.
- W4 uses exact obligation versions, immutable declaration/binding fields,
  observation/run ordering, proof dependencies, causal transition records and
  indexed invalidation queries. `graph.FileObservationState` is available;
  W4 must prove applicability and perform subject watermark CAS in the same tx.
- W5 uses the membership facet for registration/admission/acknowledgment/cancellation
  and the shared invocation identity for authentication/replay. W6's retrieval
  writes run in this same transaction. `graph.LinkCompletionClaimReference`
  creates the narrowly authorized REFERENCES edge without completing the goal.
- W6 owns retrieval lease/result/projection/event writes; the frozen source content,
  access intersection, dependency coverage and observed lifecycle snapshot travel
  together. Denied results never carry hidden source IDs/counts.
- W7 uses Event.Operations, Alias, References, Control and OutcomeBinding.
  Resolve aliases only from authenticated earlier creation results, then call
  `ValidateResolved`; operation shape validation alone is insufficient.
  Dispatch retry hashes using the recorded RequestHashVersion and recorded limits.
  Redact command v2 execution outcome together with DetailAccess-protected details.
- Every service accepts `(store.Tx, Principal, Intent, seq uint64)` and returns an
  immutable typed result. Outer entry points alone open Update. Authorize the
  actual allocated sequence. Poison on any error after a constituent write,
  including semantic validation/read errors that occur after earlier writes.

## Canonical and migration freeze

V2 ingest hashing stays `ingest-payload/v2`; Phase 3 uses `ingest-payload/v3`.
Envelope/receipt schema versions are independent from request hash and policy versions.
`CanonicalSemanticArguments` freezes type names, field names/order, explicit
pointer and ordered-slice presence, and sorted unique `canonical:"set"` fields.
Adding an intent field or operation variant after this seed requires a NEW request
schema and retention of the previous encoder; do not reinterpret recorded hashes.

Grant targets use `context-runtime/grant-target/v1`; declaration identity uses
`context-runtime/creation-declaration/v1`; current keys use `/current-key/v2`.
Resource locators, observation subjects, obligation targets, coverage members,
coverage records, mutation receipt IDs, proof IDs and operation request IDs each
have distinct `/v1` domains. W4 receipt families are individually registered under
`context-runtime/w4/<family>/v1`. See `semantic_golden_test.go` for frozen vectors.
Any persisted field change after W2's family migration requires a new migration.

## Verification and remaining work

The seed passes gofmt, `go vet ./...`, and compilation of every package through
`go test -run '^$' ./...`. Domain, store-interface, architecture, parser and policy
race tests pass; the new graph authorization and claim-reference tests pass.

Required `go test -race ./...` was run after the final code change. It is RED:

- SQLite lacks W2's forward migrations for new columns/strict leaves and receipt
  fields (e.g. `f_namespace`, `f_coverage_id`, `f_target_spec_present`, grant Targets).
  This also fails SQLite-backed graph, ingest and invocation tests.
- Existing memory/shared conformance tests intentionally continue writing after
  a failed write. The accepted Guard rule now poisons those transactions after
  their first successful write; W2 must isolate failure cases and assert rollback.
  Do not weaken automatic poisoning to preserve those old tests.

There were no reported data races. Backend conformance is W2's gate; this branch
must be integrated with W2 before a green full-suite acceptance claim.

W1's next slice completes immutable-declaration duplicate detection, normalized
coverage graph use, graph portions of P3-8/11–14/25, and exact-version retirement
with allocated sequences (W4-R-15). The old retirement planner still uses the
legacy path and must be replaced before Phase 3 execution is accepted.
No contract question remains open for releasing W2–W7 against this seed.

## Seed commits

The handoff-document commit itself is reported separately in the completion message.

- `cf8f49b` feat(domain): seed semantic schema and finite policy contracts (P3-40/41/42)
- `b1e4184` feat(domain): define exact typed grant targets and actions (P3-5/10/18/37)
- `c6f4ac1` fix(domain): isolate exact-version grant authorization from legacy IDs (P3-5)
- `a5f7c0f` feat(domain): add explicit semantic namespaces with frozen legacy fallback (P3-3/41)
- `b632a99` feat(domain): freeze immutable creation declaration identity (P3-4)
- `dce0c58` feat(domain): define normalized purpose-specific coverage (P3-6/27)
- `c403608` feat(domain): define explicit exchange membership and acknowledgment (P3-7)
- `1e2751c` feat(domain): freeze admission frontiers checkpoints and owner records (P3-7/27/32)
- `07865e1` feat(domain): separate resource locators and immutable workspace bindings (P3-20)
- `bed934c` feat(domain): freeze obligation targets and immutable matcher bindings (P3-12/18)
- `ba3cb80` feat(domain): define ordered resource reporting and unknown gaps (P3-19/23)
- `6b4706e` feat(domain): bind observation subjects to pre-execution run order (P3-21/22)
- `c3bee28` feat(domain): validate typed observation evidence and terminal outcomes (P3-21/22)
- `7a30432` feat(domain): define applicability proofs and indexed resource dependencies (P3-14/23)
- `f63237f` feat(domain): distinguish assertion freshness and invalidation authority (P3-15/16/23)
- `814c5f6` feat(domain): define authenticated lifecycle and grant intents (P3-8/9/10/11)
- `bfc8111` feat(domain): separate obligation intents from persisted authorization rows (P3-13/17/18)
- `bc9dcbc` feat(domain): bind tool invocation identity to completed output (P3-24/26)
- `7011b4d` feat(domain): define immutable holder-bound retrieval leases (P3-28/29)
- `09dac1a` feat(domain): preserve retrieval result and projection lease dependencies (P3-30)
- `047fb09` feat(domain): freeze GC candidates decisions and durable request results (P3-38/39)
- `2ab4930` feat(domain): define frozen lifecycle completion and tool results (P3-2/24/36)
- `cc88472` feat(domain): freeze bounded typed canonical argument encoding (P3-2/40)
- `b5cdad1` feat(domain): make committed mutation outcomes a closed immutable union (P3-2)
- `27c2b58` feat(domain): bind immutable mutation and tool receipts to exact requests (P3-2/24)
- `459cd58` feat(domain): define trusted resource workspace and observation intents (P3-19/21/34)
- `c332722` feat(domain): restrict semantic tool intents to authenticated agent writes (P3-25/26/27)
- `b845f4a` feat(domain): freeze ordered typed semantic operation stream (P3-34)
- `f4b64a4` feat(domain): add v3 request hashing without reinterpreting v2 (P3-34/40)
- `2df79ba` test(domain): lock v3 ordering source authority and legacy conflicts (P3-34/40)
- `df42126` feat(domain): persist request schema separately from envelope and policy (P3-40/41)
- `d2d0778` feat(domain): redact lifecycle command v2 outcomes with execution details (P3-35)
- `cf861df` feat(domain): connect declaration coverage proof and semantic audit companions (P3-4/6/36)
- `d1eee37` feat(domain): add accepted aliases control and immutable outcome bindings (W7-1/2/3/6/7)
- `7b09578` feat(domain): fold accepted immutable obligation binding fields into seed (W4-R-1/7/9/13)
- `1161db9` feat(domain): enforce restricted invalidation and transition cause fields (W4-R-2/12)
- `0b82af5` feat(domain): register accepted receipt domains IDs and sequence accessors (W2-C1/5 W4-R-9/11)
- `773dc47` feat(store): seed sequenced semantic facet and exact current-pointer CAS (P3-1/3/41 W2-C2)
- `253bcf7` fix(store): poison ignored write failures across legacy and semantic guards (P3-1 W2-C2)
- `49d8cde` feat(store): expose exact obligation proof and completion-owner contracts (W4-R-8/12)
- `9b62964` feat(store): expose ordered resource observation and subject-state contracts (W4-R-5/6/7/8)
- `7265dc1` feat(store): freeze membership frontier and in-flight completion queries (W3-3/6 W5-1/2)
- `abc5b54` feat(store): expose immutable retrieval receipt and bounded GC contracts (W3-3 W5-5 W6)
- `3b24524` feat(graph): authorize exact stored targets at allocated sequence (P3-1 W3-2 W4-R-10)
- `c1e79b3` feat(graph): add creation-only completion-claim reference helper (P3-26 W5-4)
- `50f375a` feat(domain): bind membership acknowledgment to inference and separate cancellation (W3-4 W5-1)
- `501fb61` feat(domain): freeze typed prior-operation alias references (W7-2)
- `0d7574d` fix(domain): close grant target kinds and record all finite policy limits (P3-4/5 W3-5 W5-2)
- `e08bed6` feat(graph): add trusted observation-state filing with expected-prior CAS (P3-3 W4-R-14)
- `e566a2b` fix(store): preserve semantic transaction proxies and bounded audit reads (W2-C2/3)
- `0172486` feat(domain): complete proof workspace and coverage member field contracts (W2-C1 W4-R-4/6)
- `5e6ebdc` test(architecture): freeze semantic service dependency boundaries (W4-R-17)
- `3453333` test(domain): pin semantic canonical domains and v2-v3 hash vectors (P3-40/41)
- `ea06995` docs(schema): freeze persisted record and enforcement-owner manifest (P3-41 W2-C4/6)
- `f81023f` fix(domain): enforce typed action kinds before direct authorization (P3-5/9)
