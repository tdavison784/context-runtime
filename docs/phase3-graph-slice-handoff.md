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
