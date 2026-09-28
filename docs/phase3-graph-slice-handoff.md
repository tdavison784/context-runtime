# W1 graph slice and urgent worker contract handoff

Branch: `p3/contracts`. Contract seed base: `dafced1`.

Priority worker contracts (independently cherry-pickable onto the seed):

- W5: `83bdfbf` adds RegisterExchangeMemberIntent with Validate/Clone and tests.
- W6: apply `a14faf5` then `b4bdb43`. RetrievalResult and ProjectionRecord now use
  `Origin RetrievalOrigin` instead of `Invocation ToolInvocation`. Origin has an
  exact Holder, ConversationID, TurnID and optional Invocation pointer. Only a
  HARNESS origin may omit Invocation. Result.ValidateOriginEvent binds its exact
  request/holder/source/result to the successful retrieval event; both backends
  enforce this at commit. Projection origin equals the linked result's origin.
  The schema manifest contains the updated enforcement requirements.

W7 contract additions:

- `73e4026`: ResolveLifecycleCommand returns LifecycleAuthorization with an
  empty GrantID. It resolves candidates and kind/state mismatches without
  reading grants or predicting a sequence. Execution authorizes at its actual
  allocated sequence.
- `436facb`: domain.NewSemanticEventEnvelope owns v3 validation, effective limits
  and immutable blob snapshots; delete the temporary ingest implementation.
- `330a077`: domain.OutcomeEventID(OutcomeBinding) returns (string, error), using
  the registered context-runtime/ingest/outcome-event-id/v1 canonical domain.
- `917a379`: Event.Operations payload RequestID must be empty. Ingest derives
  OperationRequestID(session, occurrence, opIndex, ordinal) after acceptance;
  ValidateResolved requires the derived ID and resolved references.
  **(PR #6 round 3, SPEC-3.8: this signature is stale, and so was round 2's
  correction.** G3/SEC-1.2 first changed it to `OperationRequestID(p
  Principal, occurrence string, operation, command uint64)`; H5 (round 2,
  landed, commit `a1d734f`) changed it again to bind two principals plus the
  event sequence. This handoff document is a point-in-time commit log and
  is not otherwise kept current; see ADR 19's Phase 3 amendment for the
  actual current signature.)
- `6594958`: graph.DeclareCreation(tx, item, CreationAcceptance) is the shared
  declaration writer. CreationAcceptance supplies PolicyVersion,
  AcceptedAttributes, ObligationDeclarationHash and SupportIDs. Stored item
  defaults are authoritative. Insert the item, declare it, then call SameDirective.

Graph integration changes:

- SameDirective compares persisted creation declarations under the canonical
  record's policy. Producers must insert the complete declaration before the
  comparison; the legacy claim string is ignored. Missing/unknown declarations
  cannot match. Current lifecycle status is excluded.
- Working snapshots require explicit namespaces and declarations, and persist
  ordered snapshot companions. Current-pointer writes use the semantic facet's
  expected-prior CAS. New filing requires a transaction-created occurrence.
- Supersession plans reserve sequences once. Source replacement authorizes at
  its SUPERSEDES edge sequence; each exact-version obligation retirement
  authorizes at its separately reserved audit sequence. Indexed GrantsFor reads
  replace whole-session grant scans on these mutation paths.
- Agent key filing/replacement requires the exact principal owner boundary and
  explicit AGENT_KEY namespace. Display prefixes confer no permission.
- Graph mutation failures poison the transaction, including ignored validation
  errors after caller-created items. Compare before attempting a mutation;
  do not catch a failed mutation and continue with a different operation.
- New LinkDerivedCoverage accepts an explicit purpose and finite member limit,
  writes one complete coverage set and linear edges, and preserves projection
  source/lease/nested dependencies. Projection dependency coverage must have
  LEASE_DEPENDENCY purpose. LinkDerived now writes normalized PROVENANCE only;
  legacy range/frontier metadata cannot authorize new semantic coverage.
- Every persisted coverage member ID equals member.Key(), scoped by its parent
  CoverageID. Direct-source and projection dependency regression tests reproduce
  rejection of the former graph-generated IDs before the fix.

Domain vet/race and focused graph behavioral tests pass. Repository compilation
and vet pass. Full repository race tests remain red during integration: W2's
schema/facet implementations are absent on this branch, and legacy graph/ingest
fixtures still assume inferred namespaces, mutable dedup and non-poisoning errors.
No full-suite acceptance is claimed. Both real-backend graph acceptance and W7
producer integration remain required. The old read-only Phase 2 lifecycle preview
is still PARSED_NOT_EXECUTED and cannot authorize Phase 3 execution.

## Commits

This list excludes this handoff document's own final commit.

- `5c1c833` fix(domain): require explicit agent-key namespace and exact owner (P3-3/25)
- `83bdfbf` feat(domain): add authenticated exchange member request intent (P3-7 W5)
- `85190f5` refactor(graph): reserve and apply supersession plans once (P3-1)
- `2b14dd8` fix(graph): retire exact obligation versions at allocated audit sequences (P3-1/5 W4-R-15)
- `a14faf5` feat(domain): distinguish tool and trusted harness retrieval origins (P3-28 W6)
- `b4bdb43` fix(domain): bind retrieval results and projections to validated origins (P3-28 W6)
- `4b79098` fix(graph): bind agent first filing to exact namespace and owner (P3-3/25)
- `d9f4441` fix(graph): deduplicate immutable declarations under recorded policy (P3-4/25)
- `1babf3c` fix(graph): poison ignored constituent operation failures (P3-1)
- `4012058` fix(graph): file new semantic occurrences with expected-prior CAS (P3-3)
- `00804b5` feat(graph): persist ordered immutable Working snapshot declarations (P3-4)
- `8ee9899` feat(graph): plan bounded purpose-specific normalized coverage (P3-6/25)
- `a93c687` feat(graph): write one normalized coverage set for derived edges (P3-6/25)
- `4de4268` fix(graph): retain original projection leases in derived coverage (P3-6/30)
- `f14ef05` fix(graph): route derivation writes through normalized provenance (P3-6)
- `53c8a97` fix(graph): authorize delegated source replacement at edge sequence (P3-1/4/11)
- `1f0ca5f` docs(graph): record worker contract hashes and graph integration handoff (P3-41 W5/6)
- `73e4026` feat(graph): resolve lifecycle commands without mutation authorization (P3-1/35 W7-a)
- `436facb` feat(domain): own v3 event envelope blob snapshots (P3-40 W7-b)
- `330a077` feat(domain): register canonical bound outcome event IDs (P3-34 W7-c)
- `917a379` fix(domain): forbid caller request IDs in submitted operation payloads (P3-2/34 W7-d)
- `6594958` feat(graph): centralize immutable creation declaration acceptance (P3-4/25 W7-e)
- `41444e6` fix(graph): use canonical coverage member keys as IDs (P3-6 W5)

## Resume batch (W1 on Claude, 2026-09-26)

Queued contract requests:

- W4-1 `62811e4`: RecordResult accepts OBSERVATION_RUN, OBSERVATION and
  RESOURCE_BINDING.
- W4-2 `e356683`: ReportResourceChangeIntent.PathContents
  `[]ResourcePathContent{Path, ContentHash}` is a canonical set bound into
  the v3 request hash. Each path is canonical and unique, and is covered by
  ChangedPaths unless the report is AllPaths or a resynchronization.
  Duplicate ChangedPaths are now rejected.

Graph regressions after merging p3/persistence: the failing graph tests were
legacy fixtures that predate the explicit-namespace (P3-3), creation
declaration (P3-4), poisoning (P3-1) and normalized coverage (P3-6)
contracts. No production graph code needed a semantic change. Fixtures now
set namespaces explicitly and declare creation through `mustCreate`
(DeclareCreation in the creating transaction). Three obsolete expectations
were replaced by the binding rule:

- An identical goal restated after Resolve is a noncurrent duplicate and
  stays RESOLVED (P3-4/C-1). The former ResolvedCanonicalAbsorbsNoOpenGoal
  rejection is gone.
- The obligation-claim duplicate test varies the declared obligation hash.
  The legacy claim argument is ignored.
- An AGENT filing a non-`agent.`-prefixed ID in its own AGENT_KEY slot is
  legitimate, because display prefixes confer nothing. The test now asserts
  that an AGENT cannot file into the DIRECTIVE namespace.

`derivedRelationshipID` is extracted with byte-identical output.

Remaining graph failures are SQLite-only and caused by W2. The SQLite
`ExactObligation` (P3-12..18 facet) is still `errUnsupported`, so the SQLite
variants of TestReplaceDirective_ObligationFanOut/256 and
TestD13_{ReplacementRetiresBoundObligations,
SnapshotIDReplacementRetiresObligations,
UnauthorizedIndirectRetirementAbortsReplacement} fail. Their memory variants
pass. TestOneLargeEventScalesLinearly moved to W7 (ingest producer path).

The full race run (`go test -race ./...`) also fails in `internal/ingest`.
Ingest sets no explicit Namespace and never calls graph.DeclareCreation, so
keyed writes are rejected with `invalid record` (P3-3/4). The package also
exceeds the default 10-minute timeout under -race, because each SQLite Open
costs about 4.5s. This is the pending W7 producer integration and is not a
graph regression. The SQLite TestOneLargeEventScalesLinearly failure has the
same producer cause and is owned by W7.
