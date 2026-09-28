# 8. Observation identities, obligation matcher/claim versions, applicability fingerprints, mutation grants, and invalidation rules

Status: Proposed (2026-09-26, drafted for Phase 3; reconciled through the
round-4 integration, worktree `p3-int`, head `dc07666` (SPEC-4.4/DUR-4.10/
K1e/SPEC-4.9 correct the prior "reconciled through PR #6 round 4 (head
`4ff6ca1`)" header, itself a correction of a stale "round 2"), with every
round-4 branch merged — K1 (derived-at-read proof validity, replacing
DUR-3.1 (C)'s cap), L1 (derived-at-read subject applicability, `store
.SubjectApplicability`), M1 (GC policy-version-mismatch quarantine), M2
(the settlement pre-check before obligation-version retirement), M3
(every P3-42 required-test bullet written), M4, and the round-4 GC/grant
fixes. `go test -race -count=1 -timeout 45m ./...` passes with no
exceptions; every decision below cites real, `grep`-verified code and
tests, not a proposed contract. **This ADR remains Proposed for one
narrower reason than before:** the P3-42 required-test mapping is now
met after the round-5 SPEC-5.1 remap (every wrong/partial/adjacent
citation moved to a better existing test, read and confirmed before
citing; memory-only coverage labeled in-row), with the round-5 t3 and t2
test passes since closing the open-exchange half of P3-38 and the P3-20
filesystem/network-read clause — the coverage table below now marks no
clause MISSING — though it still records a small number of
adjacent-test/Low-confidence caveats inline (see
"Outstanding required tests" below), and K1 has fully landed, but
the commander has not yet
ruled this ADR Accepted at the Phase 3 gate — that ruling is explicitly
outside this docs pass's authority.)
Date: 2026-09-26

## Context

FR-OBL-001 through FR-OBL-006 require obligations to have a stable identity, a
deterministic matcher/claim-pattern registry, an append-only status history, a
materialization exception distinct from satisfaction, and matcher-declared
invalidation dependencies against the current resource state. FR-AUTH-001/002
require every obligation transition to check access and authority, and let a
source authorize a specific matcher version rather than a bare grant of
"assert" over an obligation's stable ID. FR-REL-001/002/005/007/008 require a
SATISFIES relation to be a typed derived view from transitions/proofs, not a
separately persisted item-to-item edge, and require provenance/access checks
before any resource-derived satisfaction is published at a boundary broader
than its evidence. FR-DOM-005/006 keep evidence, knowledge, and state as
separate categories and require currentness/residency/goal-status/obligation
status to be independent axes. INV-04, INV-05, INV-09, INV-10, and INV-16
require, respectively, that lower authority can never promote itself through
an obligation transition; that resource/observation state never crosses an
access boundary; that identical inputs replay deterministically; that restart
and concurrent mutation preserve valid state; and that a current obligation is
SATISFIED only by proof applicable to the declared current resource state or
an explicit authorized assertion. Event traces T02, T06, and T07 are this
ADR's acceptance cases: T02 (replacement retires exactly the old obligation
version, with no inherited proof/grant), T06 (every lifecycle/assertion path
enforces one authorization matrix), and T07 (proofs expire when their subject
changes, and a newer valid proof replaces a still-valid one through an audited
pair of transitions).

`internal/obligation` (worker W4, final branch `p3/obligation` tip `8b0ad63`)
implements the matcher registry, claim binding, proof/applicability, resource
reporting, and invalidation; `internal/lifecycle` (W3), `internal/tools`/
`internal/graph` membership (W5), `internal/retrieve` (W6), and `internal/ingest`
(W7) consume its transaction-scoped API. All of it is merged into
`phase-3-semantic-state`; the packages, functions, and tests cited below are
real, not proposed. Only the ADR's own Status line — kept Proposed per the
commander's reconciliation ruling — signals that formal acceptance is a
separate, still-pending step.

**Binding source.** This ADR records the disposition reached in the
commander's binding Phase 3 decision record
(`.worktrees/_commander/phase3-decisions.md`, P3-1 through P3-42, all
conflicts C-1 through C-21 ADOPTED by commander ruling FROZEN 2026-09-26),
reconciled against W4's final report (`final-p3-w4.md`, its 29-item decision
list beyond the frozen P3/C text, approved by the commander as Q-1..Q-10 and
the T07 evidence ruling), W5's final report (`final-p3-w5.md`, for the
evidence-support and tool-result rulings this ADR shares with ADR 6/17/19),
and W7's gate evidence (`final-p3-w7.md`). Section references below (`§P3-n`)
are to the decision record; `§W4-n` are to W4's numbered implementation list.

## Decision

### 1. Typed, exact-version grant targets (§P3-5)

`domain.GrantTarget{Kind, SessionID, ItemID | ObligationID+Version,
AuthorizationKey}` is the discriminated target ADR 16 and this ADR both use;
`ITEM_OCCURRENCE` targets name an immutable item occurrence, `OBLIGATION_VERSION`
targets name `(SessionID, ObligationID, Version)` — never the stable ID alone
and never `Revision`. A grant issued against version N authorizes a
transition on version N only. Matcher grants remain restricted to
`ActionAssertObligation` (ADR 16). `domain.Action.Delegable()`/
`ValidForTarget()` (`internal/domain/grant_target.go`) close the action/target-kind
pairing; `internal/graph.AuthorizeAtSequence` (§3 below) resolves the exact
stored target and its indexed `GrantsFor(action, target)` read, replacing a
whole-session `Grants()` scan on every mutation path. Legacy Phase 1/2 grants
against a stable obligation ID stay inert; nothing attaches one to "the
latest version" automatically.

### 2. Deterministic claim/matcher registration and target binding (§P3-12, §W4-1..9)

`obligation.MatchClaim` (`internal/obligation/claim.go`) is the closed claim
pattern: the whole extracted text must be ASCII, with at most one
sentence-final period stripped, matching exactly `All tests must pass` or
`Read <path>` (no whitespace/control bytes in the path) — no trimming, case
folding, or substring search (`FuzzMatchClaim` fuzzes this). `obligation.DefaultRegistry`
holds the two closed matchers, `tests_pass/1` and `file_read/1`; `obligation.BindPinned`
and `obligation.BindDeclared` (`internal/obligation/target.go`) bind a
claim/declaration to its `TargetSpec`, and `obligation.DeclarePinnedTx`
(`internal/obligation/declare.go`) runs only in the same transaction as the
Pinned item's own creation, only when that item is current and not a
duplicate (§W4-8) — it replaced `ingest.declareObligation` per W4's handoff
to W7. An explicit `obligation=<claim>` attribute wins over pattern matching
and creates at most one declaration slot per item (slot 0 for Pinned; HARNESS
declarations use canonical decimal slots 1-1024 via `domain.DerivedObligationID(key, n)`,
§W4-7). Binding decisions ratified beyond the frozen P3/C text (commander-approved
Q-1..Q-4/Q-10):

- **Q-1:** a claim-derived `file_read` binds `CURRENT_CONTENT`; `FIXED_HASH`
  comes only from an explicit typed declaration that states the hash.
- **Q-2:** `obligation=file_read` still takes its path only from text
  matching `file_read`'s own claim pattern — the explicit attribute changes
  which matcher is chosen, never the pattern grammar.
- **Q-3:** a workspace binding applies only if its reporter's authority is
  at least the source's and its boundary covers the source; source bindings
  outrank task bindings; the latest version of each applicable binding is
  used; more than one applicable binding is `BINDING_AMBIGUOUS`, and one that
  exists but doesn't qualify is `BINDING_AUTHORITY`.
- **Q-4:** an invalid path (absolute or escaping its base) still creates an
  `UNBOUND` obligation with reason `PATH_INVALID`, never a silently dropped
  declaration.
- **Q-10:** an unknown claim, an unknown matcher version, or a missing
  target creates an `UNBOUND` obligation. It can never execute and it blocks
  completion (ADR 16 §P3-9 amendment) exactly like a resolvable but
  UNRESOLVED one.
- Opaque specs (environment/suite/coverage) must be closed tokens or
  hashes; a raw value such as `PATH=...` is rejected outright (§W4-9).

`tests_pass` requires a trusted declared suite, complete coverage
specification, resource/worktree, working directory, and environment
specification resolved only from a workspace binding or an explicit typed
target; `file_read` requires a normalized resource-relative path plus
`FIXED_HASH` or `CURRENT_CONTENT` mode. An unresolved target is a
syntactically known, non-executable matcher, never a guess. Replacement (§7)
never retroactively binds a Phase 2 unbound claim.

### 3. Transition intents and the obligation state machine (§P3-13)

`obligation.ApplyTransitionTx(tx, actor, domain.TransitionIntent, seq)`
(`internal/obligation/transition.go`) is the single entry point. The caller's
intent carries target/version/expected-revision/requested-action/evidence
references/applicability intent/request ID only; actor, sequence, `Action`,
`Matcher`, and `GrantID` attribution are service-derived, continuing ADR 16's
`TransitionAction` binding. The legal edge set is unchanged
(UNRESOLVED→SATISFIED; SATISFIED→UNRESOLVED; UNRESOLVED↔BLOCKED; any
non-WAIVED status→WAIVED); WAIVED and retired versions never transition
again. Evidence references must exist by commit, match session and exact
occurrence, be accessible, and satisfy §4's publication rule. A positive
matcher transition additionally requires, in the same transaction: a live
exact-version grant (§1), a bound matcher (§2), and a validated nonempty
applicability proof current as of commit (§4), applied under
`AppendObligationTransition`'s existing CAS (ADR 16). Reason codes are
closed (`domain.ObligationReasonCode`); rationale text is bounded and
access-filtered.

**The raw Phase 2 `store.Tx.AppendObligationTransition` path now refuses
SATISFIED, closing an INV-16 gap this ADR's own transaction never had
(PR #6 round 2, DUR-2.12).** ADR 16's original, still-legal raw method
(unchanged there — it remains Phase 2's own legacy entry point) carries no
proof or assertion record and predates this ADR's Phase 3 backing
requirement entirely; before this fix it could still commit an
undeclared/legacy version straight from UNRESOLVED to SATISFIED with
nothing behind it. Both backends now reject `To == ObligationSatisfied` on
that path outright — satisfaction is exclusively
`AppendSemanticObligationTransition`'s (this section's path), which always
carries a `TransitionDetail`. There is no production caller of the raw
path that could have hit this (`storetest` exercised it directly), so this
closes a latent gap, not an active one.

### 4. Proof visibility and the derived SATISFIES view (§P3-14)

`domain.ApplicabilityProof` names the exact obligation version, `TargetSpec`,
matcher/rule, sorted evidence occurrences, resource version/dependency
references, satisfying transition, and boundary. `obligation.publishableEvidence`
enforces that a satisfaction's boundary is `Within` (ADR 6) every evidence and
applicability source's boundary before the transition commits — actor access
alone is not sufficient, and an overbroad proof is rejected outright.
`obligation.Satisfies(tx, viewer, ref, currentOnly)` (`internal/obligation/read.go`)
is the access-filtered SATISFIES read: `currentOnly` returns only the proof
behind a current SATISFIED version, and the historical form retains later
invalidation/retirement. A bare attestation creates an `AssertionRecord`, never
a fabricated edge (`TestSatisfiesNoEdgeForAttestation`). `graph.Relationship`
keeps only item-to-item `DERIVED_FROM`/`SUPERSEDES`/`DEPENDS_ON`/`REFERENCES`/
`DUPLICATE_OF` edges (Q2, adopted); SATISFIES is never one of them. **Note for
W3/W7 (recorded in W4's contract items):** the store also exposes a
lower-level `Satisfies` read; callers use `obligation.Satisfies`, the
access-filtered derived view, not the store's.

### 5. Assertion modes: ATTESTATION vs RESOURCE_BOUND (§P3-15, C-14)

`domain.AssertionMode` (`ATTESTATION`/`RESOURCE_BOUND`) is chosen explicitly at
assertion time and recorded on `domain.AssertionRecord`; `obligation.checkResourceClaims`
(`internal/obligation/transition.go`) validates RESOURCE_BOUND's target/
dependency/applicability data against the authoritative resource state (§9).
ATTESTATION rests on asserting authority alone; optional citations are audit
provenance only and never a silent resource dependency (`ATTESTATION cannot
carry a resource proof` per the schema manifest). **UNBOUND obligations
(§W4-21):** a RESOURCE_BOUND assertion on an UNBOUND obligation is
`ErrUnknownApplicability` — it can still be attested, matching W2's
proof-target check. **A claim must cover the obligation's own target
(PR #6 round 1, SPEC-1.11/SPEC-2.13).** `checkResourceClaims` originally
accepted any well-formed dependency claim without comparing it to
`o.TargetSpec`, so a RESOURCE_BOUND assertion could cite an unrelated
resource or path and still satisfy the obligation — attestation semantics
under a RESOURCE_BOUND label. It now requires at least one dependency that
actually covers the target: a `DependencyWorkspace` claim must match the
target resource's authoritative revision and fingerprint exactly: a
`DependencyCurrentPath` claim must resolve through the same current-path
read `file_read` observations use (§9/§10) and match the target's locator;
and a `DependencyFixedContent` claim is meaningful only as the required
snapshot of a `FIXED_HASH` file target, matching `TargetSpec.File.RequiredHash`
exactly — anywhere else it is rejected as an unbound claim, never silently
accepted as vacuous coverage.

**Alternative considered and rejected (C-14).** Claude M10 proposed inferring
`Applicability = ASSERTED` for a bare assertion and treating a citation
carrying fingerprints as retroactively invalidatable like a matcher proof.
Codex's cross-check required an explicit mode chosen at assertion time.
**Ruling: ADOPT explicit mode**, as implemented above.

### 6. Rejected proof and proof refresh use legal transitions (§P3-16, §W4-18..20)

`obligation.evaluateOne`/`satisfy` (`internal/obligation/evaluate.go`) and
`invalidateProof` (`internal/obligation/invalidate.go`) implement the
restricted `PROOF_REJECTED` and atomic `PROOF_REFRESH` paths.
Ratified refinements beyond the frozen text:

- **Cross-boundary rejection (§W4-18), REPLACED (PR #6 round 2 systemic
  ruling H1; SEC-2.9; commits `bf69262`/`f5efa47`/`8d14af9`, W4b).** W4's
  original text said a newer complete FAIL "may reject a task-wide matcher
  proof even when the failing run's own evidence is narrower," with no
  boundary check — SEC-2.9 found this let a FAIL private to another agent
  reject a TASK-wide proof and leak the private observation's ID into a
  visible record. **The rule is replaced in full:** a proof is rejected only
  by a subject- and family-matched **COMPLETE FAIL** whose run ordinal is
  greater than the proof's own run ordinal and whose evidence and run
  boundaries both cover the proof's boundary (`failCovers`,
  `internal/obligation/evaluate.go`) — regardless of what fingerprint or
  content the FAIL observed. Rejection depends on **ordinal and boundary
  only**, never on comparing fingerprints: this closes the H1 residual where
  a FAIL judged "inapplicable" by content comparison could be skipped even
  though it was the newest terminal result for the subject. A newer PASS at
  a different fingerprint rejects nothing (only FAIL rejects). Rejection
  still only ever moves a version towards UNRESOLVED, and the rejecting
  observation's ID is never recorded where an unauthorized reader of the
  proof could see it. Tests: `TestH1NewerFailAtOtherFingerprintRejects`,
  `TestH1StalePassAfterRevert`, `TestH1StalePassAfterInapplicableFail`,
  `TestH1PrivateFailDoesNotOutrankTaskPass`,
  `TestPrivateFailNeverRejectsTaskProof_SEC29`.
- **Satisfaction is additionally gated on a per-subject high-water mark
  (H1's watermark half; SEC-2.1/SPEC-2.1/DUR-2.1). Fully landed as of this
  pass (commits `7ab3bfe`, `a444105`, W2; `8d14af9`, W4b).** The newest
  *complete* PASS or FAIL run for the exact subject partition wins, whatever
  the current subject state's own applicability — this closes the round-1
  residual where a workspace revert (W1→W2→W1) made an older run's PASS
  satisfy despite a newer FAIL, because the watermark previously counted
  only `Applicability == CURRENT` subject states and a revert moves the old
  state to STALE. `store.ResourceReader.SubjectHighWater(subjectKey, taskID,
  access)` (`internal/store/semantic_resource.go`) is one keyed read per
  partition, maintained at write time by `InsertObservation` in both
  backends (SQLite: migration `0037_subject_high_water.sql`, a primary-key
  table; memory: an in-memory map keyed the same way) — `subjectWatermark`
  (`internal/obligation/evaluate.go`) takes the max across the run's own
  partition and every partition whose evidence could back the obligation.
  **The same mark independently re-gates at commit time**, not only at
  evaluation: both stores' `checkProofNotStale`/equivalent guard (the
  commit-time half of INV-16, `store.ValidateSatisfactionBacking`'s sibling
  check) rejects a commit whose proof's run ordinal is older than the
  subject's current high-water mark, closing the same window a
  read-then-write race could otherwise open between evaluation and commit.
  Tests: `internal/store/storetest`'s `TestConformance/SemanticSubjectHighWater`.
- **What rejection touches (§W4-19, corrected — SPEC-1.10/SPEC-2.5).**
  W4's original implementation list narrowed this to "a resource-bound
  assertion proof is never rejected by a FAIL," which silently departed from
  the frozen P3-16 text with no commander ruling; PR #6 round 1 (SPEC-1.10)
  withdrew that narrowing as a code fix, and this ADR's text is corrected to
  match. A newer complete, boundary-covering (`failCovers`, §6 above) FAIL
  rejects the subject's **current
  matcher or resource-bound satisfaction alike** — both are proof, and
  `evaluateOne`'s `VerdictFail` branch runs the same restricted
  `invalidateProof` path regardless of which kind the current proof is
  (`internal/obligation/evaluate.go`). An ATTESTATION carries no proof and
  is never touched by resource invalidation at all — that half of the
  original text was always correct and is unchanged.
- **Refresh (§W4-20):** the release (UNRESOLVED) step writes at the
  evaluation's own sequence; the positive (SATISFIED) step is authorized in
  advance at its own later sequence, before either write commits. Without a
  live grant for the new proof, the still-valid old proof is left alone
  rather than half-invalidated.

### 7. Matcher evaluation is runtime-controlled, with trusted reevaluation (§P3-17, C-4)

`obligation.evaluate` sees only validated typed observations and recorded
resource/target state; no public parameter nominates a matcher, outcome, or
text as proof. A positive evaluation requires a live exact-version
`ActionAssertObligation` grant (§1) and commit-time applicability (§4).

**Alternative considered and rejected (C-4).** Claude E4 proposed running the
matcher only on observation ingestion. Codex's cross-check (X11) proposed an
additional trusted runtime reevaluation on an exact obligation version after
a new grant, an authorized unblock/revalidation, or a replacement. **Ruling:
ADOPT trusted exact-version reevaluation.** `obligation.ReevaluateTx(tx, actor,
domain.ReevaluateIntent{RequestID, ObligationID, Version, ExpectedRevision}, seq)`
(`internal/obligation/reevaluate.go`) selects from existing recorded
observations in deterministic run order; a new grant alone, with no
reevaluation call, never by itself satisfies anything.

### 8. HARNESS obligation declarations and materialization exceptions (§P3-18)

`obligation.DeclareObligationTx` and `obligation.SetMaterializationTx`
(`internal/obligation/declare.go`, `materialization.go`) implement FR-OBL-001's
HARNESS path and FR-OBL-003's rendering-only exception: direct equal-or-higher
authority over the source, or an exact-occurrence grant, is required; naming
a SYSTEM source is never sufficient. The exception affects rendering only —
it never satisfies, waives, protects, or permits completion
(`TestUnfinishedTaskObligations`, ADR 16 §P3-9 amendment).

**A source's declared obligations are bounded (PR #6 round 1, DUR-1.5,
G2/SPEC-2.13).** `domain.Phase3Policy.ObligationDeclarationLimit()`
(`internal/domain/semantic.go`) is `min(MaxTargets, MaxObligationsPerSource)`
— the tighter of the policy's own target-set cap and a fixed 256 — and
`createObligation` (`internal/obligation/declare.go:226`) refuses to bind
one more obligation version to a source than this limit *before writing
anything*, returning `ErrResourceLimit`. Without this bound, a source could
accumulate more declared obligation versions than its tightest by-source
consumer read (the lifecycle replacement/retirement path, ADR 16) could ever
page, making the source permanently unreplaceable, undemoteable,
unarchivable, and uncollectible — the same "producer limits never exceed
consumer limits" principle round 1's G2 ruling states generally.

### 9. Resource currentness has an authenticated, ordered source (§P3-19, C-9, §W4-10..16)

`obligation.RegisterResourceTx` and `obligation.ReportResourceChangeTx`
(`internal/obligation/resource.go`) implement `ResourceState`/`ResourceUpdate`
under authenticated SYSTEM/HARNESS-only reporting, CAS on the authoritative
revision. Ratified report-ordering rules (§W4-14): an ordinary report must
extend the authoritative revision by exactly one; a skipped revision, or any
report while state is already UNKNOWN, commits UNKNOWN and invalidates
everything; a report at or behind the current revision is rejected and never
rolls state back; only a resync restores KNOWN; an observation never
establishes resource state by itself (this is C-9's ruling, unchanged).

**Alternative considered and rejected (C-9).** Claude E6 proposed
first-observation initialization. Codex's cross-check required authoritative
reporting only. **Ruling: ADOPT authoritative baseline/resync**, exactly as
implemented. `tests_pass` compares workspace fingerprints (§W4-11: a new
revision with the same fingerprint keeps the proof); the reporter is exactly
the registering SYSTEM/HARNESS principal, and being the reporter grants no
read access to obligations (§W4-12); a resync invalidates every proof whose
recorded fingerprint or path content differs from the resynced state, and a
path the resync doesn't mention is treated as changed (§W4-13).

**Per-path content (§W4-2, §W4-15).** `ReportResourceChangeIntent.PathContents
[]ResourcePathContent{Path, ContentHash}` (a canonical set, bound into the v3
request hash) records content only from KNOWN reports, at the resulting
revision; a report stating unchanged content keeps path-dependent proofs, and
an omitted path asserts nothing. **File identity (§W4-16):** canonical
resource-relative locators (base `"."`) identify both file content and file
subjects, so a base-directory split never separates a binding, a report, and
a read; a file run's path must lie under its bound workspace's base
directory.

### 10. Workspace binding and proof locator identity are explicit (§P3-20, C-5, C-11)

`obligation.BindWorkspaceTx`/`resolveWorkspace` (`internal/obligation/workspace.go`)
and `canonicalLocator` (`internal/obligation/resource.go`) implement
`domain.WorkspaceBinding` and `resource-locator/v1`, a locator identity
separate from ADR 19's legacy `reference-locator/v1`.

**Alternatives considered and rejected (C-5, C-11).** As drafted originally:
Codex's cross-check required a distinct resource-locator identity (C-5,
adopted — legacy References identity is unchanged) and separated resource
*control* from workspace *bindings* (C-11, adopted — an empty `TaskID` is
neither universal read access nor resource-reporting capability by itself).
Both rulings are implemented as originally recorded; no change at
reconciliation.

**Resolution reads only each binding's current version, and only while that
version is in the context (PR #6 round 2, H2, commit `eb0ae67`, W1).**
`resolveWorkspace` originally paged `WorkspaceBindingsByContext` — every
version of every binding in a context — and kept the highest version seen
in memory; enough rebinding history for one context could exceed the work
bound and permanently fail a Pinned directive's creation. It now pages
W1's write-time `CurrentWorkspaceBindingsByContext` (migration 0038's
one-row-per-binding pointer index), so version history never counts against
the bound. This is a behavior change beyond a performance fix: **a binding
counts in a context only while its latest version is still recorded
there** — a rebind that moves a binding to a different context retires it
from the old one, rather than leaving a stale version visible forever.

### 11. Typed observations preserve evidence and reporter provenance (§P3-21, C-6, C-7, §W4-22..23)

`obligation.RegisterRunTx`/`ReportObservationTx`/`evidenceInRun`
(`internal/obligation/observation.go`) implement the typed envelope, bound to
an exact TOOL evidence occurrence; only the run's registering reporter may
report against it (§W4-12 restated), and malformed input is rejected
atomically (C-7).

**Evidence is bound to the run's own execution, not merely to the caller's
say-so (PR #6 round 1, SPEC-1.12).** "Validate same session, execution and
boundary" (P3-21) originally checked only that the intent's `ExecutionID`
matched the run's — the reporter agreeing with itself, not a fact about the
evidence occurrence. `evidenceInRun` (`internal/obligation/observation.go:221`)
now additionally requires the evidence's own `Source.ToolCallID` to equal
`run.ExecutionID`: **the documented harness contract is that a harness
reporting an observation must set `ExecutionID` to the producing tool
call's ID**, so the evidence occurrence a run cites is provably the one that
tool call actually produced, not an unrelated TOOL item reused across runs.
**Resolved (SPEC-2.8, PR #6 round 2, W2).** `store.ProducedBy(ev, execution)`
(`internal/store/semantic_resource.go`) is the shared predicate both
backends' `InsertObservation` now call independently: `ev.Source != nil &&
ev.Source.ToolCallID != "" && ev.Source.ToolCallID == execution`. P3-21's
"validated by service and store" now holds in full — the service-level
check above and this store-level check are two independent enforcement
layers, not one masquerading as two. Tests:
`internal/store/storetest`'s `TestConformance/SemanticObservationEvidenceExecution`.

**T07 evidence ruling (§W4-22, commander-approved beyond the frozen text).**
TOOL evidence may be TURN- or TASK-scoped **if it has exactly the run's own
ownership** — the observation keeps the evidence's boundary, but derived state
(§12) stays at the run's TASK boundary; any ownership mismatch between the
evidence and the run is rejected. This lets a TURN-scoped tool result serve
as valid evidence for a TASK-scoped obligation's proof without silently
widening the evidence's own access boundary. **Residual consequence
(recorded, not a defect):** a proof resting on TURN-scoped evidence stays
valid after that evidence's own turn expires — it is an immutable record of
an applicable run, and only a resource change (§13), not evidence lifetime,
invalidates it.

**obs-state/1 text (§W4-23).** The `obs-state/1` template is fixed and built
only from typed fields (family, outcome, completeness, counts, subject key,
observed identity); it contains no tool output, paths, or environment
values.

**Alternatives considered and rejected (C-6, C-7).** As drafted originally:
Codex's cross-check required TOOL-authority derived state (C-6, adopted —
`graph.FileObservationState` files the OBSERVATION-namespace supersession
branch at TOOL authority) and atomic rejection of a malformed envelope (C-7,
adopted). Both are implemented as originally recorded.

### 12. Subject comparability, run order, and state authority (§P3-22, C-8, §W4-10)

`obligation.SubjectFor`/`deriveState` (`internal/obligation/subject.go`,
`subject_state.go`) and `graph.FileObservationState` implement `SubjectKey/v1`,
a pre-execution run ordinal, and CAS'd watermark gating.

**Run ordinal (§W4-10, Q-5).** A run's ordinal is exactly the sequence
allocated when it is *registered* (`RegisterRunTx`), not when it is reported
or received — this is what makes a delayed report's receipt order incapable
of substituting for actual execution order.

**Alternative considered and rejected (C-8).** As drafted originally: Codex's
cross-check required complete/current/ordered gating with a persisted
pre-execution run ordinal (adopted, and now concretely `RegisterRunTx`'s
allocated sequence). Implemented as originally recorded; no change at
reconciliation.

**G1: one closing observation per run, plus the ordinal/high-water rules
that actually decide "current" (SPEC-2.5, PR #6 round 1/2).** This section's
ordinal is necessary but not sufficient for G1's full comparability
guarantee; §6 above records the rest, cross-referenced here because it is
this section's own subject/run/watermark machinery that enforces it:
migration 0029 makes `(subject, Ordinal)` unique so two runs of one subject
can never share an ordinal (ambiguous order otherwise); migration 0030 plus
`store.ClosesRun` (`internal/store/semantic_resource.go`, backed by
SQLite's `closingObservation` predicate and memory's equivalent check)
enforce at most one terminal (complete PASS/FAIL, or ERROR/TIMEOUT/
CANCELLED) observation per run;
`internal/store`'s commit-time INV-16 guard (`store.ValidateSatisfactionBacking`,
DUR-1.9) independently re-checks that a commit never leaves a SATISFIED
version without applicable backing, as a second layer behind the service's
own check; and §6's rejection/high-water rules (H1) decide which run's
result is "current" for comparison. None of this is optional hardening —
without migration 0030's uniqueness, a partial/duplicate closing observation
could itself make ordinal comparison ambiguous. **Under H1, a newer
complete PASS at a *non-current* fingerprint still outranks an older PASS
at the current fingerprint — the probe result is UNRESOLVED, not a silent
keep of the old proof — and any newer covering FAIL rejects even a
`FIXED_HASH` `file_read` proof** (DUR-3.10): ordinal and boundary decide
rejection, never which fingerprint matched. Tests (missing from this
section until PR #6 round 3, DUR-3.10/SPEC-3.8): `TestH1NewerFailAtOtherFingerprintRejects`,
`TestH1StalePassAfterRevert`, `TestH1StalePassAfterInapplicableFail`,
`TestH1PrivateFailDoesNotOutrankTaskPass`,
`TestPrivateFailNeverRejectsTaskProof_SEC29` (§6); `internal/store/storetest`'s
`TestConformance/SemanticSubjectHighWater` and the INV-16 backing cases in
`SemanticSatisfactionBacking`/`SemanticStaleProof`.

### 13. Invalidation is atomic and narrowly scoped (§P3-23, C-10, §W4-17)

`obligation.settle` (`internal/obligation/effective.go:153`, plus the inline
`invalidateProof` helper, `evaluate.go:98`/`invalidate.go:36`) implements the restricted, paged, work-bounded invalidation
transaction. `domain.CauseResourceInvalidation` and `CauseProofRejected`
(`internal/domain/assertion.go`) are the two causes this restricted path
actually uses, and only these two require a non-nil `OriginAuthorization`
distinct from a live `GrantID` (`domain.TransitionDetail.Validate`,
`domain.ObligationTransition.validateSemanticTransition`) — a runtime
consequence the reporter's own resource authority does not otherwise carry.
`CauseProofRefresh` (§6) is a *different* case: it is the authorized,
grant-backed SATISFIED→UNRESOLVED→SATISFIED pair a live matcher grant
produces, so its release step carries the evaluating actor's own
`GrantID`, never `OriginAuthorization` — refresh is not part of this
section's restricted invalidation path, even though both share the
SATISFIED→UNRESOLVED direction.

**Subject-state applicability is derived at read time, landed
(PR #6 round 3 commander ruling, DUR-3.1; High; amends P3-22/P3-23's
"recorded ... marked in the same transaction" text; landed at head
`4ff6ca1` — SPEC-4.4/DUR-4.10 correct the prior "assigned W4b, not yet
landed" text).** All three parts are implemented:

- **(A) Reports read only what they can affect — historical as of round
  4, superseded by K1 A1 below.** As landed in round 3 this was an index
  design: `lookup_live_proof_path` (migration 0045, frozen step
  `0045/proofs/reconcile-live-proof-paths-v1`) filed each live proof's
  `CURRENT_PATH` dependency under its exact path plus the hex of every
  ancestor directory, and each `WORKSPACE` dependency under `"ws"`, and
  `affectedProofs` (removed with K1) read only the live proofs an
  accepted report touched through that index. K1 replaced the read side
  outright: an accepted report now writes only its O(1) monotone
  pointers and reads **zero** proof pages (K1a), and the 0045 tables
  survive only as an unused write-time metric (A6 below). The
  directory-intersects-files rule itself — a changed directory affects
  every file under it, §13/SPEC-1.18 — lives on inside K1 A1's key rule
  (`store.PathAffectKeys`, `internal/store/semantic_resource.go`: the
  exact path plus every ancestor directory). Tests:
  `TestK1ReportsNeverFanOut` (the store's proof-page counters stay at
  zero across reports), `TestDUR31ReportsReadOnlyAffectedProofs`
  (reworked for K1: every report reads zero proof pages, with
  effective-status semantics asserted per path shape),
  `TestDUR31ReportsIgnoreUntouchedLiveState`.
- **(B) Applicability is derived, not recorded, for planning.**
  `store.SubjectApplicability(r, st)`
  (`internal/store/subject_applicability.go`) is the one shared rule
  (ruling L1, resolving GLM-1: the former
  `obligation.Service.SubjectApplicability` method is deleted): a few
  exact-key reads re-deriving a subject state's applicability from the
  authoritative resource state at read time, failing closed — any read
  error yields `UNKNOWN` plus the error, never `CURRENT` — never a
  static flag a later resource change would otherwise have to walk out
  and update by hand; the stored `SubjectState.Applicability`
  (`subject_state.go`) is written once, at filing, is filing-time-only
  metadata, and is deprecated there with a pointer to this rule. Tests:
  `TestDUR31SubjectApplicabilityIsDerived`,
  `TestSubjectApplicabilityIsExactAndFailsClosed`
  (`internal/store/subject_applicability_test.go`). **L1 landed in round
  4; the gap this section previously recorded as open is closed except
  for one Phase 4 deferral:** retrieval consumes the rule —
  `retrieve.observationCurrent` (`internal/retrieve/read.go`) labels a
  NamespaceObservation item `ItemHistorical` whenever its derived
  applicability is not CURRENT, failing closed on any derivation error,
  a constant label that is no oracle (L1.2); the
  `SubjectStatesByResource` CURRENT filter is gone from both stores
  (L1.5/SEC-4.11/DUR-4.7) — every filed state pages in first-filing
  order whatever its applicability, and both implementations' comments
  say so; reevaluation's `selectEvidence`
  (`internal/obligation/reevaluate.go`) selects only CURRENT-derived
  partitions (`TestSPEC410ReevaluationSelectsOnlyCurrentEvidence`). The
  one remaining deferral, recorded here as an explicit Phase 4 item, not
  a Phase 3 defect: there is still no production
  `policy.EligibilitySnapshot` builder, so nothing populates
  `policy.EligibilitySnapshot.Applicability`
  (`internal/policy/eligibility.go`, whose field comment now carries
  L1.3's contract — any builder MUST fill it from
  `store.SubjectApplicability`). Migration 0032's CURRENT-only partial
  index is stale metadata recorded in ADR 3; removing it is Phase 4
  scope.
- **(C) A per-resource live-dependents cap — historical as of round 4,
  retired outright by the commander's FROZEN K1 ruling, which has landed
  (below).** `domain.Phase3Policy.MaxLiveProofDependents` (default 256,
  `internal/policy/phase3.go`) bounded a resource's live
  non-`FIXED_CONTENT` proof-dependency rows so invalidating all of them
  fits half of `MaxTransactionWork` at 5 work units per row
  (`domain/semantic.go`); at the cap a new proof was refused before any
  write (`dependentRoom`, since removed). Round 4 review found the cap
  did not actually bound what an ALL/UNKNOWN/resync report costs —
  `proofAffected` (also removed) paged *every* dependency of each live
  proof, including rows on other resources and `FIXED_CONTENT` rows the
  cap never counted — so the wedge (C) existed to prevent (every report
  on that resource refused, its SATISFIED proofs stuck stale,
  SEC-4.1/SPEC-4.1, High) was still reachable, and path-stable proofs
  could hold cap room indefinitely and silently starve a later,
  unrelated satisfaction (DUR-4.3). **K1 resolves all of this by
  removing the cap and its refusal path entirely (A6 below), not by
  narrowing it:** reports are O(1) pointer writes that never read proofs
  and are never refused for dependent volume, so neither wedge nor
  starvation is reachable; the field and migration 0046's column stay
  recorded but unvalidated (P3-40 hashes), and the two round-3 cap
  tests (`TestDUR31AssertionRespectsDependentCap`,
  `TestDUR31MatcherRespectsDependentCap`) were removed with the code
  they pinned. Tests that pin the retirement:
  `TestK1MultiResourceProofsNeverWedgeReports_DUR42`,
  `TestK1StableLiveProofsNeverBlockSatisfaction_DUR43`,
  `TestMaxLiveProofDependentsRecordedNotValidated`,
  `TestConformance/IngestionV3BackfilledPolicyReplays`.

This changes §12's "recorded" framing for the file-content case
specifically; the OBSERVATION-derived `task_state` item itself (§11/§12,
TOOL authority, C-6) is unaffected.

**K1 — the FROZEN commander ruling that replaces (C), landed (round 4;
`.worktrees/_commander/K1-final.md`, background `proposal-K1.md`/
`K1-sec.md`/`K1-spec.md`; landed in the round-4 integration, worktree
`p3-int`, head `dc07666`; W4b lead with W2c/W3c/W7b/W1).** In place of a
write-time cap, K1 makes proof validity **derived at read**, exactly as
(B) already does for subject-state applicability, and removes the cap
and its refusal path entirely (K1a, K1d):

- **A1 — monotone, write-time-pointer validity.** `store.ProofDerivedValid`
  (`internal/store/semantic_validity.go`) is the one shared rule: a proof
  is valid iff its resource is KNOWN and, for every recorded
  non-`FIXED_CONTENT` dependency, no accepted update with a later
  revision *affects* it under the shared key rule — `store.PathAffectKeys`
  (`internal/store/semantic_resource.go`): a dependency's exact path plus
  each of its ancestor directories, with the `"all"` key for all-paths and
  UNKNOWN reports (a
  `WORKSPACE` dependency only on a fingerprint change or lost freshness;
  a `CURRENT_PATH` dependency only on a touch to its path, an ancestor
  directory, `ALL`, or `UNKNOWN`, never on a same-content path report —
  and, since round 5's K1-api.3 amendment below, not on a broad raise
  whose report explicitly recorded the path's prior content either;
  `FIXED_CONTENT` never; the directory-intersection half of the old
  `change.affects` predicate also survives as `store.PathAffectKeys`,
  `internal/store/semantic_resource.go:129`; `under` remains a test-only helper, `internal/obligation/invalidate.go:18`). This is decided from two write-time pointers,
  migration 0048's `lookup_workspace_divergence` (per resource, the
  raises' revision order — `LastWorkspaceDivergenceRev`) and
  `lookup_affecting_raise` (per resource/key, `LastAffectingRev`),
  maintained in the report's own O(1) transaction; a dependency is
  invalid iff a relevant pointer exceeds its recorded `ResourceRevision`.
  Both fail closed: an unreadable record, dependency, or pointer, or an
  exceeded read bound, returns invalid with the error, never true.
  Validity is monotone: once a proof is derived invalid it is never valid
  again, so a W1→W2→W1 revert cannot resurrect it. Migration: `0048_k1_pointers.sql`,
  frozen step `0048/k1/reconcile-workspace-divergence-v1`
  (`reconcileK1PointersV1`, `steps_0048.go`) backfills divergence exactly
  from each resource's fingerprint chain, and conservatively raises the
  `ALL` key only for UNKNOWN or all-paths reports plus every recorded
  `ChangedPath` of every stored report (same-content history
  isn't reconstructible, so a backfilled raise can settle a proof a live
  report would have spared — an accepted, one-time-upgrade-only
  overapproximation, not an ongoing behavior). **A KNOWN report after an
  UNKNOWN one raises divergence** through the same general rule as any
  fingerprint change: going UNKNOWN clears the resource's stored
  fingerprint, so the next KNOWN report's fingerprint (never empty)
  always differs from it, with no special-case code. Tests:
  `TestK1ReportsNeverFanOut`, `TestK1ValidityIsMonotone`,
  `TestK1DependencySemantics`, `TestConformance/SemanticProofDerivedValid`
  (storetest); `TestUpgradeK1Pointers_0048`
  (`internal/store/sqlite/upgrade_test.go`, round 4 — closes the upgrade-
  parity gap this ADR previously flagged: against a real pre-0048
  database, the divergence chain and affecting keys backfill exactly
  (the one genuinely content-ambiguous path key carries the documented
  conservative superset, never fewer raises than the runtime rule
  requires), `ProofDerivedValid` matches the runtime rule exactly,
  `LiveProofs`/`SettlementCursor` are consistent, and a fresh database
  living the same history through the runtime path — never the
  backfill — raises identically; no case fails open).
  **K1-api.3 amendment (round 5, XREV-5.1/5.2; integration merge
  `5810578`, migration `0049_path_confirmations.sql`): a broad raise
  whose report explicitly recorded the path's prior content *confirms*
  the path and no longer falls a `CURRENT_PATH` dependency on it.** The
  exact key keeps the unchanged rule above; for each broad key, the
  latest raise counts against the path unless that same raise confirmed
  it, in which case the latest *unconfirmed* raise does
  (`dependencyDerivedValid`, `internal/store/semantic_validity.go`,
  through two new `ResourceReader` pointers, `LastConfirmedRev` and
  `LastUnconfirmedRev`). Confirmations are write-time records in the
  report's own O(1) transaction — 0049's `lookup_path_confirmation`
  (per resource/confirmed path/broad key: the latest confirming raise
  and the latest unconfirmed raise it overtook) and
  `lookup_unconfirmed_gap` (the immutable closed runs of unconfirmed
  raises the settlement cause seeks, A3 below), key encodings exactly
  0048's — and a report's write costs at most confirmed-paths × key
  depth (`broadKeyCovers`): still no fan-out. Monotonicity is
  unchanged: the unconfirmed pointer only grows, so a confirmation can
  never resurrect an earlier invalidation — only a dependency asserted
  at or after the confirming raise's own revision is spared, and
  H1→H2→H1 on the exact key stays a change. **The migration backfills
  nothing** (the same non-reconstructibility 0048's ALL-key backfill
  accepted), so pre-0049 history keeps invalidating exactly as before
  — conservative over-invalidation, never under — and a post-upgrade
  confirming report closes the legacy raise it overtakes as an
  unconfirmed gap without sparing any dependency below it
  (`TestUpgradePathConfirmations_0049`,
  `internal/store/sqlite/upgrade_test.go`, against a real database
  migrated through 0048). **XREV-5.1, same landing: a report's raises
  and confirmations are visible to K1 pointer reads inside the writing
  transaction.** Both backends apply a transaction's pending reports in
  registration order at every K1 pointer read and at the A5 commit
  guard (`advanceK1Reports` — a watermark, not a once-flag, so reports
  registered after an earlier advance also become visible), and a
  matching write that changed content outranks one that recorded the
  prior content, so the outcome never depends on the writes' order
  inside the transaction. Accepted fail-closed residual: a read taken
  between a report's registration and its content writes' recording
  can only over-invalidate — the confirmation is not yet recorded, so
  the raise stands — never spare one. Tests:
  `TestConformance/SemanticK1BroadConfirmations` (the three broad
  shapes — an ALL-paths report, a resync-shaped report with a changed
  fingerprint, an ancestor-directory report naming the path — plus
  changed-content and omitted-path controls, W1→W2→W1 fingerprint and
  broad-key monotonicity, interleaved confirmed and unconfirmed raises,
  and one path's confirmation never suppressing another path's
  raises); `TestConformance/SemanticK1InTxVisibility` (an
  in-transaction read is felled by the same transaction's change and
  stays felled through an in-transaction H1→H2→H1; a confirming broad
  report followed by a same-transaction satisfaction reads valid and
  commits through the A5 guard); `TestXREV5SameContentAllPaths` and
  `TestXREV5PathReadAndWaiverInReportTx` (`internal/obligation`
  service level, `zz_xrev5_ports_test.go`, both backends — the latter:
  a waiver appended after a same-transaction report settles first, so
  the committed history is assertion, then RESOURCE_INVALIDATION, then
  the waiver from UNRESOLVED).
- **A2 — one effective-status helper, everywhere status is selected.**
  `obligation.EffectiveStatus(r, o)` (`internal/obligation/effective.go`)
  is the one helper: a stored SATISFIED version is effectively SATISFIED
  only while `ProofDerivedValid` holds; otherwise it is effectively
  UNRESOLVED and `pending` (a settlement has not been recorded yet).
  Every status-selected query that exists today goes through it or a
  thin wrapper: `CompleteTask`/X8 and GC protection (`gc_snapshot`'s
  `OpenObligationSource`) via `internal/lifecycle/effective_status.go`'s
  `openObligation` (`effectiveStatus = obligation.EffectiveStatus`); the
  `SATISFIES` view and inspection (`ObligationView`) directly in
  `internal/obligation/read.go`; matcher evaluation in
  `internal/obligation/evaluate.go`. **The FR-DOM-007 mandatory set and
  automatic-planning eligibility are Phase 4 planner territory
  (`internal/plan`, SDD §6) that does not exist yet — A2's requirement
  there is necessarily deferred, not landed, and this ADR records it as
  such rather than claiming coverage a nonexistent package can't have.**
  A repo-wide enforcement test,
  `TestEffectiveStatusIsTheOnlyStoredStatusReader_K1A2`
  (`internal/domain/effective_status_boundary_test.go`, rewritten
  type-based in round 5's SPEC-5.9 — round 4's version was a syntactic
  AST walk), parses and type-checks every production package outside
  `internal/domain`/`internal/store` (test files, testdata, and nested
  modules skipped) and fails on any read of the stored
  `ObligationVersion.Status` field outside its allowlist — however the
  read is used: compared, assigned to a local, a map key, converted, a
  method value. Matching the type-checked field itself
  (`types.Selections` against the resolved field object) is what makes
  the check catch indirection, and a package that fails to type-check
  fails the test closed rather than being skipped. The allowlist holds
  exactly: `obligation.EffectiveStatus` (the one helper);
  `obligation.Service.ApplyTransitionTx` (the write path's
  transition-table check, recorded cause and history — a pending
  version is settled first); `graph.settleBeforeRetirement` (M2's
  settlement pre-check, itself settlement machinery); and
  `lifecycle.completionBlockers` (`Status.Valid()` enum-shape
  validation only — its selection goes through `openObligation` →
  `effectiveStatus`). The `statusReadPending` list, for reads
  predating K1 that still needed migrating, is empty — nothing remains
  pending, and an entry that no longer occurs also fails, so the list
  can only ever shrink. Tests: `TestK1ReadsUseEffectiveStatus`,
  `TestCompletionBlockersUseEffectiveStatus_K1A2`,
  `TestGCProtectionUsesEffectiveStatus_K1A2`,
  `TestStatusSelectorsReadStoredSatisfiedVersions_K1A2`,
  `TestArchiveProtectionUsesEffectiveStatus_K1A2`
  (`internal/lifecycle/effective_status_test.go`).
- **A3/A4 — inline settle plus an async audit worker.**
  `obligation.Service.settle` (`internal/obligation/effective.go`) writes
  the restricted `RESOURCE_INVALIDATION` transition before any transition
  on a pending version: cause is `settlementCause`'s earliest update, by
  session `(Seq, ID)`, that actually fired one of the proof's dependency
  pointers (`FirstWorkspaceDivergenceAfter`/`FirstAffectingUpdateAfter`,
  plus round 5's `FirstUnconfirmedAffectingUpdateAfter` for the broad
  keys — K1-api.3 SPEC-2: a broad raise that confirmed the path never
  fired the dependency, so only unconfirmed raises are candidates),
  keyed exactly `(proofID, causeID)` so every path settles a proof at
  most once. `graph.WithPendingSettler` (`internal/graph/settlement.go`)
  injects `obligation.Service.SettleBeforeRetireTx` as graph's
  `PendingSettler`: `settleBeforeRetirement` calls it before a version is
  retired (replacement, supersession), and without an injected settler,
  retiring a derived-invalid SATISFIED version fails closed with
  `graph.ErrPendingSettlement` (M2) rather than ever recording
  SATISFIED→retired over a pending settlement. `obligation.Service.SettlePendingTx`
  is the durable, resumable async worker (the GC-queue-cursor pattern,
  migration 0048's `settlement_cursor` and `lookup_live_proof`): as the
  session's SYSTEM runtime (`runtimeActor`), it audits up to a bounded
  page of live proofs after the cursor, settles each pending one through
  the same exact-keyed path as the inline settle (idempotent with it),
  skips settled/re-satisfied/waived/retired versions, and never blocks or
  charges a report — correctness never depends on it having run. Each
  pass is sized by the remaining work budget (XREV-5.3, round 5): it
  stops before a settlement that would exceed the budget, commits the
  completed prefix, and advances the cursor only past the proofs it
  actually processed — never to the page end — while a proof that alone
  exceeds a whole fresh pass's budget is left pending and skipped past,
  so the cursor never stalls on it (K1-api.3 §4) and inline settlement
  (A3) still settles it at its next transition. Nothing in production calls the worker (its only current callers are
  tests): the embedder schedules the passes (SPEC-5.12, round 5) — alongside draining the GC queue is the intended
  cadence — and a scheduler that never fires costs only the timeliness of the recorded state, never a guarantee,
  since inline settlement settles every pending version at its next transition; the audit is a disclosure/
  completeness concern, never a validity one. **K1-api.md's original XREV-5.3 sentence — that a configuration where
  not even one settlement fits "is rejected by policy validation (MaxTransactionWork must be enough for one
  settlement)" — is superseded by that same document's binding amendment list, items 4 and 5 (SEC-6.2, round 7):
  no minimum-budget check ever validates a recorded policy (`Phase3Policy.Validate` requires only positive limits,
  `internal/domain/semantic.go:131`), and the worker never refuses — it skips the oversized proof past the cursor
  and leaves it pending for inline settlement (`TestXREV5SettlementWorkerSkipsOversizedProofWithoutStalling`).**
  The settlement's actor is always the session SYSTEM runtime; the causing update's own `Reporter` field (unchanged
  from P3-19/ADR 8 §9) is the record of who actually reported the change, kept separate from the settlement's own
  actor. Tests (`internal/obligation/k1_test.go` unless noted): `TestK1InlineSettleBeforeTransition`,
  `TestK1SettlementWorker`, `TestK1SettleBeforeRetire`,
  `TestK1ReplacementSettlesPendingBeforeRetirement`;
  `TestPendingSatisfiedVersionIsUnfinishedAndProtected_K1`
  (`internal/lifecycle/k1_pending_test.go`); round 5 adds
  `TestXREV5SettlementWorkerMakesProgressWithSmallBudget` and
  `TestXREV5SettlementWorkerSkipsOversizedProofWithoutStalling`
  (`internal/obligation/xrev53_worker_test.go`, both stores — the
  latter's control: the cursor advances every silent pass until the
  scan wraps, the oversized proofs stay stored SATISFIED and pending
  with effective UNRESOLVED, and one still settles inline at its next
  transition; `cf388e2` fixed a nested-view deadlock in the file's
  `proofCursorOf` helper — it reads the version before opening its
  view, because the SQLite store serves one view at a time);
  `TestK1Api3SettlementCauseSkipsConfirmations`
  (`internal/obligation/zz_xrev5_ports_test.go`, both backends — the
  recorded cause is the unconfirmed raise, never the confirming one,
  including the boundary where the dependency was asserted at the
  confirming report's own revision) and storetest's
  `TestConformance/SemanticK1SettlementCause` (the gap-seek read
  itself: closed gap runs, the open run past the confirmation pointer,
  a raise at exactly rev not past it, and canonical-argument checks).
- **A5 — INV-16 and the commit guard.** SDD v0.11 (below) states INV-16 as
  "a current obligation's effective SATISFIED status is backed by an
  assertion or proof that is valid at read." Both stores' `checkProofDerivedValid`
  (`internal/store/memory/semantic_k1.go`, `internal/store/sqlite/semantic_k1.go`)
  is the A5 commit guard: it refuses a SATISFIED write whose proof is
  derived invalid at commit, calling `store.ProofDerivedValid` directly.
  Tests: `TestConformance/SemanticA5CommitGuard` (storetest);
  `TestConcurrentINV16`, `TestK1PropertyEffectiveSatisfactionIsValid`
  (`internal/obligation/k1_property_test.go`, both stores via the
  SQLite suite; property: stored SATISFIED ⇒ effective SATISFIED, or
  pending settlement whose causing update is committed — SPEC-5.10,
  round 5, strengthens it on both sides of the guard: each pending
  version's `settlementCause` must be a stored update the read can
  itself see, read back by ID, identical, at or before the read point,
  and a generated same-transaction satisfy-then-fell step — an
  observation satisfies an obligation and a report fells its workspace
  in one transaction — must be refused by the guard, with coverage
  counters keeping both clauses non-vacuous and a mutation check that
  neutering `checkProofDerivedValid` in either store fails the
  property).
- **A6 — the cap is retired, not narrowed.** `Phase3Policy.Validate`
  (`internal/domain/semantic.go`) no longer validates
  `MaxLiveProofDependents` at all (K1 A6, GLM-2, DUR-4.6): the field and
  migration 0046's column stay recorded, since P3-40's request hashes
  already include them and committed migrations are never edited, but a
  historical policy that recorded it now simply replays under its own
  recorded value with no re-validation. `dependentRoom` and its
  `errDependentCap` refusal are gone from `internal/obligation/evaluate.go`
  and `transition.go` entirely, not narrowed. Migration 0045's
  `lookup_live_proof_path`/`lookup_live_dependents` tables are **not**
  reused for A1's pointers (correcting this ADR's own earlier draft of
  this section) — they stay maintained as an unused write-time metric
  (K1d: "the counter may stay as a metric"), while A1's actual pointers
  are migration 0048's own new tables. Tests:
  `TestK1MultiResourceProofsNeverWedgeReports_DUR42`,
  `TestK1StableLiveProofsNeverBlockSatisfaction_DUR43`.
- **A7 — disclosure and a bounded fail-closed rule.** `ObligationView.Pending`
  (`internal/obligation/read.go`) is the fixed K1 A7 code: `VisibleObligations`
  reports it whenever a version's effective status differs from its
  stored one because of a pending settlement, naming no update, path, or
  ID. `EffectiveStatus`/`ProofDerivedValid` fail closed on any unreadable
  pointer or dependency (UNRESOLVED, with the error); dependencies per
  proof stay bounded at creation by the existing claim bound (`MaxTargets`/
  `MaxEvidence`), so derivation always fits the transaction's work budget
  regardless of a resource's live-proof count.

K1e's SDD amendment is recorded below. It was first applied per the
commander's FROZEN ruling ahead of K1's own implementation landing — the
same precedent SDD v0.9/v0.10's freeze-time amendments already set for
this repository (ADR 19) — and K1's implementation has since landed
(round 4, above), so the SDD text and the Decision-section text above
are now both current, real code and no longer a forward-looking
placeholder.

**A changed directory intersects files under it (PR #6 round 1, SPEC-1.18).**
`change.affects` (round 1, then at `internal/obligation/invalidate.go:39,50`;
the predicate is gone under K1, and its rule lives on as the ancestor keys
`store.PathAffectKeys` emits, `internal/store/semantic_resource.go:129`,
used by both stores and `settle`; `under` itself is test-only now)
originally
compared a reported path to a dependency's path by exact string equality
only, so a report naming a changed directory (e.g. `src`) never invalidated
a `CURRENT_CONTENT` dependency on a file under it (`src/a.go`) — the
opposite of P3-23's required conservative intersection. It now matches
`p == q || strings.HasPrefix(p, q+"/")`: a directory report affects every
path under it, and a sibling whose name merely shares a prefix (`doc` vs.
`docs`) stays distinct because the comparison requires the exact separator.

**Q-9 (commander-approved beyond the frozen text, §W4-17).** A matcher only
ever satisfies obligations the reporting principal can access; rejection and
resource invalidation, by contrast, apply **across all boundaries** through
this restricted path — the asymmetry is deliberate: a reporter cannot use its
resource authority to newly satisfy something it cannot see, but an
authoritative resource change must still invalidate a private proof it
never had read access to, because leaving a stale satisfaction standing is
the actual security-relevant failure.

**Alternative considered and rejected (C-10).** As drafted originally:
Codex's cross-check required a narrowly defined runtime consequence with a
separate historical authorization reference (adopted, now
`domain.TransitionDetail.OriginAuthorization`). Implemented as originally
recorded.

## Implementation decisions beyond the frozen text

These are decisions the commander approved during Phase 3 implementation,
beyond P3-1..42 and C-1..C-21, that this ADR is the natural home for because
no single §-decision above covers them. Each cites its real code and test.

- **Runtime discipline — change causes (§W4-24).** `obligation.appendTransition`
  (`internal/obligation/change.go`) requires every `SemanticChange` cause to
  cite a stored record: the transition itself for a caller-requested
  transition, the evaluated observation for a matcher or refresh step, and
  the resource update or rejecting observation for a runtime consequence
  (§13). `TestSemanticChangeRecords` (`internal/obligation/change_test.go`).
- **Runtime discipline — record IDs (§W4-28).** `obligation.RecordIDEncoding
  = "context-runtime/w4/record-id/v1"` (`internal/obligation/ids.go`) derives
  every W4 audit/transition/dependency/observation/run/update record ID as a
  prefix plus the full hash of `(kind, ordered parts)`, so records of
  different kinds never share an ID. It is registered in ADR 4's canonical
  domain registry and pinned in `internal/domain/canonical_domains_test.go`
  — the item W4's final report flagged as open ("W1 has not registered it
  yet") is resolved.
- **Runtime discipline — work bound (§W4-25).** Every correctness-critical
  fan-out (invalidation, retirement) charges one transaction-wide budget,
  `domain.Phase3Policy.MaxTransactionWork`; exceeding it is `ErrResourceLimit`
  and the whole transaction rolls back — results are never truncated.
  `TestResourceInvalidationPagingAndLimit`, `TestFailureInjectionAtomicity`.
- **Runtime discipline — receipts (§W4-26).** Each W4 mutation family has its
  own hash domain, `context-runtime/w4/<family>/v1` (distinct from the
  record-id domain above, since family names contain a dot). Replay is
  checked before any current state is read; policy limits apply only after
  replay, so an admitted retry is never reinterpreted by a later, stricter
  limit. `TestReceiptReplayAndConflict`.
- **Runtime discipline — policy pinning (§W4-27).** `obligation.New` refuses
  a policy unless it names `claim-pattern/v1`, `matcher-registry/v1`, and
  `obs-state/1`, and keeps its own clone (including W1's `GCTriggers` slice)
  so a caller cannot alias and mutate it after construction.
  `TestNewPinsRuleVersions`, `TestServiceRequiresFiniteKnownPolicy`.
- **Runtime discipline — deferred sequence allocation (PR #6 round 2,
  DUR-2.14, commit `1ecbbf8`).** `begin` (`internal/obligation/service.go`)
  now accepts `seq == 0` and defers allocation until after the replay check
  (`allocate`, called post-replay), matching the same "replay before
  `NextSeq`" discipline `graph.operationSeq` and `lifecycle.collect` already
  use elsewhere — an exact retry of a W4 mutation consumes no sequence.
  **Except pinned declarations:** `beginAt` (used only for a declaration
  that must ride another write's own transaction — a Pinned source's
  creation or replacement) still requires a nonzero `seq`, because that
  sequence must be the *other* write's exact sequence, never independently
  deferred or allocated. `TestDeferredSeqAllocatedAfterReplay_DUR214`.
- **Replacement declarations (§W4-29, ties to §P3-4/C-1 below).**
  `obligation.DeclareForReplacementTx(tx, actor, newSourceItemID, seq)`
  (`internal/obligation/declare.go`) reads a replacement's explicit
  `obligation=` claim only from the new occurrence's immutable creation
  declaration (`AcceptedSemantics.AcceptedAttributes`), never from the
  caller directly; a missing or legacy-unknown declaration is
  `ErrUnsupportedSchema`, more than one obligation attribute is
  `ErrInvalidRecord`. The result is the next version, UNRESOLVED, with no
  inherited grant or proof. `TestDeclareForReplacement`,
  `TestDeclareForReplacementFailsClosed`.
- **Only independent tool observations are evidence (ADR 6/17/19 territory;
  W5's ruling, FR-DOM-006).** A semantic tool's own acknowledgment of the
  agent's write (e.g. "Stored X.") is never `tool_result` and can never be
  cited as evidence support — only an external TOOL result qualifies.
  `internal/tools/ack_evidence_test.go:TestToolAcknowledgmentIsNeverEvidenceSupport`,
  `internal/ingest`'s `TestGateT17_ToolResultAsEvidence` (external TOOL
  result qualifies; user transcript and a semantic tool's own acknowledgment
  are both refused). This closes a laundering path: without it, an agent
  could cite its own "Stored X." as if it were independent confirmation of
  X.
- **Tool-outcome EventID format (ADR 4 territory; §W4-28's sibling, cited
  here because it binds a TOOL observation's evidence occurrence).**
  `domain.ToolOutcomeEventID(b, toolCallID) = OutcomeEventID(b) + "/" +
  toolCallID` (`internal/domain/outcome_id.go`) is now formally registered —
  W7's final report's open item ("W1 may register it formally") is resolved.
  See ADR 4's "Canonical domain registry (Phase 3)" section, which this ADR
  does not duplicate.
- **A task-less automatic trigger produces no GC request (PR #6 round 2 systemic ruling H4; SPEC-2.2/DUR-2.5).**
  A directive's explicit replacement (ADR 19's Q1/C-1 amendment) may itself
  be session-scoped (`TaskID == ""`); before this ruling, its SUPERSESSION
  side effect tried to build a `CollectTask` request with an empty
  `TaskID`, which `domain.CollectIntent.Validate` rejects — so the
  *replacement itself* failed even though the replacement's own
  authorization had nothing to do with GC. H4 resolves this at the root:
  `gcqueue.Enqueue(tx, pol, origin, trigger, taskID, triggerID)`
  (`internal/gcqueue`, imported by `internal/ingest`, `internal/tools`,
  `internal/obligation`, **and `internal/lifecycle` itself** —
  `replace.go`'s task-less directive replacement path, SPEC-3.8 — every
  producer below the top-level lifecycle API) persists nothing at all for
  a task-less trigger and returns success; it never falls back to a
  session-scoped `CollectScope`, because H4 disables automatic
  session-scoped collection. The replacement (or keyed write, or
  observation supersession) that raised the trigger always succeeds on its
  own merits — GC producing nothing is never a reason a mutation fails.
  Test: `internal/lifecycle`'s `TestReplaceTasklessDirectiveProducesNoGC`.
  Manual SESSION collection uses durable resumable batches under round-3
  ruling J7 (SEC-3.9); see ADR 16. This does not enable automatic task-less
  producers.
  `domain.GCTaskCompletion` is exempt from this restriction the same way it
  is exempt from the enabled-set check (ADR 16's amendment): it always
  names a real task by construction. **SPEC-2.11 (same commit):** `Enqueue`
  takes the caller's own recorded `Phase3Policy`, not the executor's — the
  policy that decided the source event's classification is the same one
  that decides GC-trigger enablement and is stamped on the request, so an
  executor running a newer or older policy can never silently drop or
  misattribute a trigger the event's own policy enabled. Tests:
  `internal/gcqueue`'s `TestEnqueueUsesRecordedPolicyAndTaskScope`;
  `internal/ingest`'s `TestGCProducers_TasklessSupersession_H4` and
  `TestGCProducers_RecordedPolicyDecides_SPEC211`.

## Alternatives considered

Each numbered decision above already records, inline, the specific rejected
alternative for every conflict the commander ruled on (C-4, C-5, C-6, C-7,
C-8, C-9, C-10, C-11, C-14) as "Claude review" vs. "Codex cross-check"
positions with the trade-off and the FROZEN ruling — that is the "Alternatives
considered" content this ADR format calls for, kept next to the decision it
disputes rather than repeated in a second list. Three further alternatives
were rejected outright by the binding decision record, not merely by a
disputed cross-check, and are recorded once here because no single
numbered decision above is the obvious place for them:

- **A seventh `OBSERVATION` authority, alongside the six `EventKind`
  authorities.** Rejected: observations are typed payloads carried by an
  existing SYSTEM/HARNESS/TOOL-authority event (§11), not a new authority
  level. `domain.EventKind` still has exactly six values.
- **A dedicated `ActionEvaluateMatcher` mutation-grant action.** Rejected:
  matcher grants authorize `ActionAssertObligation` only (ADR 16,
  reaffirmed at §1). No such action exists in `internal/domain/grant_target.go`.
- **A separately stored item-to-obligation `SATISFIES` edge in
  `graph.Relationship`.** Rejected at §4/Q2: `graph.Relationship` has no
  SATISFIES variant; `obligation.Satisfies` is the only read.
- **Reusing `AuthorizeMutation`'s live grant-target check for the
  invalidation path (§13).** Rejected as part of C-10: `settle` (K1) and
  the inline `invalidateProof` helper run as internal, runtime-only
  consequences, never through the public grant-authorized mutation path.

## SDD amendment (applied in v0.10)

These sentences are applied to SDD.md as v0.10, exactly as the commander's
FROZEN "Required normative amendments at freeze" table approved them
(`phase3-decisions.md`); this ADR does not itself edit SDD.md.

- **FR-REL-001; §7.** Replace "Relationship records support DERIVED_FROM,
  SUPERSEDES, DEPENDS_ON, REFERENCES, SATISFIES, and DUPLICATE_OF.
  Relationship records are the only store of edges; items do not duplicate
  them." with: "Relationship records store item-to-item DERIVED_FROM,
  SUPERSEDES, DEPENDS_ON, REFERENCES and DUPLICATE_OF edges. SATISFIES is a
  typed derived relation from authoritative obligation-transition and proof
  records to an obligation version; it is not a separately persisted
  item-to-item edge. Items do not duplicate an item-to-item edge's data." —
  the last sentence preserves the replaced text's own "items do not
  duplicate them" clause, scoped to the item-to-item edges FR-REL-001 still
  covers, and is the actual SDD.md v0.10 text in full (`SDD.md:217`).
- **FR-AUTH-002 / FR-OBL-002.** Add: "Positive matcher transitions require a
  live exact-obligation-version grant. Runtime invalidation of an already
  accepted resource-bound proof is a restricted audited consequence of the
  recorded resource/proof change, permitted after the original grant expires
  or is revoked; it confers no general mutation or disclosure authority."
- **FR-OBL-005.** Add: "The current resource state is established by
  authenticated ordered resource reporting, not by receipt of an
  observation. Assertions explicitly distinguish authority attestation from
  resource-bound proof. Newer applicable rejected proof and proof refresh use
  the defined audited transitions."

## SDD amendment (K1e, applied in v0.11 ahead of K1's own implementation)

These sentences are applied to SDD.md as v0.11, per the commander's FROZEN
K1 ruling (`.worktrees/_commander/K1-final.md`, adopting `proposal-K1.md`
K1a/K1d/K1e as amended by `K1-spec.md` A1-A7) — the same precedent SDD
v0.9/v0.10's freeze-time amendments already set (ADR 19): the normative
text is applied on the ruling's freeze, independent of whether K1's own
code has landed. This ADR does not itself edit SDD.md.

- **FR-OBL-005.** Add: "A resource-bound obligation's SATISFIED status is
  its effective status: derived at read from whether the current proof's
  recorded dependencies still match the authoritative resource state,
  never read from a stored status flag as authoritative. A resource
  change that makes a stored-SATISFIED proof's effective status no
  longer SATISFIED is recorded atomically by the next transition that
  observes it, and independently by a bounded, resumable asynchronous
  audit worker; the effective status is correct whether or not that
  worker has run." This replaces this ADR's DUR-3.1 (C) live-proof-
  dependents cap — a write-time refusal that round 4 review
  (SEC-4.1/SPEC-4.1) found does not actually bound invalidation work —
  with the derive-at-read design the "Subject-state applicability"
  section above already uses for planning; K1 extends the same technique
  to SATISFIED status itself.
- **INV-16.** Replace "A current obligation can be SATISFIED only by proof
  applicable to the declared current resource state or an explicit
  authorized assertion." with: "A current obligation's effective
  SATISFIED status (FR-OBL-005) is backed by an assertion or proof that is
  valid at read, applicable to the declared current resource state; a
  stored SATISFIED status whose proof has since become invalid is never
  presented as current, whether or not its audit transition has yet been
  recorded."

**The K1 amendments were freeze-time text that has since landed, so this
paragraph's round-3 "not yet reflected … pending code" hedge is itself
historical:** the exact monotone validity rule (K1-spec.md
A1), the "one effective-status helper" enforcement test (A2), the inline
settle plus async audit worker (A3/A4), the commit guard and
property-test wording (A5), and A6/A7's disclosure and bounded-dependency
rules are all implemented at the round-4 integration, and the K1
subsection above cites each one's real code and tests. The SDD keeps
only INV-16's normative wording (v0.11, above); the rest of K1 remains
this ADR's Decision-section text, as ADR 19's freeze-time precedent
allows, and that text is now descriptive of landed code, not forward-
looking.

## Consequences / compatibility impact

- `internal/obligation` is the sole owner of P3-12 through P3-23, 20 source
  files, about 7.2k lines including tests; it imports only `domain`, `store`,
  `graph`, and `policy` (root `imports_test.go`).
  `internal/lifecycle` and `internal/tools` call its status/read contracts
  (`obligation.Satisfies`, `VisibleObligations`, `UnfinishedTaskObligations`)
  and never duplicate matcher logic or grant-target resolution.
- `MutationGrant`'s target became the discriminated `GrantTarget` (§1); this
  landed as part of the Phase 3 domain contract seed and is not a pending
  migration — every grant-issuance/revocation call site and fixture already
  uses it.
- SATISFIES is a derived view, not a `Relationship` row (§4); any code
  querying `graph.Relationships` for one finds none and must call
  `obligation.Satisfies` instead.
- Forward migrations 0021-0023 (`internal/store/sqlite/migrations/0021_phase3_declarations.sql`,
  `0022_phase3_resources.sql`, `0023_phase3_proofs.sql`) and 0026
  (`0026_reconcile_legacy_matcher_satisfaction.sql`, the checksum-pinned
  `UPGRADE_RECONCILIATION` step for a legacy current matcher-derived
  SATISFIED row lacking establishable applicability) carry this ADR's schema
  (ADR 3's amendment records the full migration list).
- Every positive obligation transition requires a live exact-version grant
  (§1) and a bound matcher/target (§2) in the same transaction; this is
  enforced code, not a future requirement.
- **Known limitations, carried into this reconciliation from W4's final
  report, not defects to silently work around:**
  - Two reads remain unpaged at one task's or one obligation's scope rather
    than indexed across the whole session: `VisibleObligations` (via the
    legacy `Obligations(taskID)` list) and `originOf` (via the per-obligation
    transition list). Both are bounded by one task/obligation, never the
    whole session; an indexed paged read can replace them later without a
    contract change.
  - The SQLite test suite swaps a package-level backend factory and is not
    `t.Parallel`-safe; no W4 test uses `t.Parallel`.
- Event traces T02, T06, and T07 are this ADR's acceptance gate; see "Gate
  evidence" below for the real tests that close it.

## Gate evidence (W7, `final-p3-w7.md`, integration head `fc87199`; reconfirmed at
PR #6 review round 1 head `c22a53c`)

`go vet ./...` and `gofmt -l` are clean. `go test -race -count=1 ./...` passes
with no exceptions: `TestReplaceDirectiveDeclaresRealW4Obligation`, the one
`internal/lifecycle` fixture W7's original report left pending (attributed to
a stale W3 fixture after the facet merges), now passes too. Per the Phase 3
gate checklist (`phase3-decisions.md`):

| Gate item | This ADR's evidence |
|---|---|
| T02 full semantic state | `internal/ingest`: `TestGateT02_ReplacementRetiresOldRequirement`, `TestGateT02_GrantBindsExactVersion`, `TestP336_ChangedRestatementReplaces`, `TestP336_ReplacementHistoryReconstructible`; `internal/obligation`: `TestTraceT02Obligation`; `internal/lifecycle`: `TestReplaceDirectiveReopensWithIdenticalContentAndRetiresObligations`; `internal/graph`: `TestReplaceDirective_T02` |
| T06 full mutation state | `internal/ingest`: `TestGateT06_AllLifecyclePathsAuthorize`, `TestGateT06_GrantExpiryAtActualSequence`, `TestGateT06_Completion`, `TestCommandsV2_AbortsAtomically`, `TestRequireAtomic_T06Denial`; `internal/obligation`: `TestTraceT06`, `TestMatcherGrantT06`, `TestTransitionAuthorityT06`; `internal/domain`: `TestAuthorizeMutation_T06_UserCannotActOnSystemGoal`, `TestAuthorizeMutation_T06_HarnessCannotAssertWithoutGrant`, `TestAuthorizeMutation_T06_SystemGrantedMatcherLetsHarnessAssert` |
| T07 full semantic state + repeats | `internal/ingest`: `TestGateT07_SetupThroughIngest`, `TestGateT07_ProofsExpireThroughIngest`, `TestGateT07_Repeats` (8 subtests: other repo/env; partial/timeout/cancelled; out-of-order runs and stale reports; missing baseline, revision gap→UNKNOWN, and resync; same-fingerprint FAIL rejects and PASS refreshes; revoked grant; agent-private evidence; fan-out over more than one page and limit rollback); `internal/obligation`: `TestTraceT07`, `TestTraceT07PublicAPI`, `TestResourceInvalidationPagingAndLimit` |
| Generated-history properties (INV-04/09/16 for this ADR's scope) | `internal/ingest`: `TestProperty_GeneratedHistories` (one current obligation version per source, SATISFIED backed by assertion/proof, WAIVED/retired frozen, no authority promotion by transition, retries allocate nothing); `internal/obligation`: `TestConcurrentINV16` |
| Failure injection / race | `internal/obligation`: `TestFailureInjectionAtomicity` (8 multi-record operations roll back at every constituent write, 2-9 writes each, both backends), `TestTransitionIgnoredErrorPoisons`; `internal/ingest`: `TestConcurrency_ReplacementVsTransition`, `TestConcurrency_InvalidationVsObservation` |
| Frozen Phase 2 fixture upgrade/replay | `internal/ingest`: `TestPhase2FixtureReplay`; `internal/store/sqlite`: `TestUpgradeReconcilesLegacyMatcherSatisfaction`, `TestInterruptedReconciliationRollsBack`, `TestUpgradePhase3RowFields`, `TestUpgradeGrantTargetIndex` |
| SQLite suite (this package) | 50/50 subtests, 8/8 failure-injection scenarios pass at `8b0ad63`, about 29s under `-race` (SQLite stores open from W2's migrated `sqlitetest` template) |
| Fuzzing | `FuzzMatchClaim`, `FuzzTestsPassVerdict` (20s each, both pass) |

Serialized U delta/rebase, actual inherited-request removal, and rebase
omission/order are explicitly Phase 5 and are not part of this gate.

## P3-42 required-test bullet coverage table (SPEC-4.9)

The commander asked for every P3-1 through P3-42 required-test bullet
(`.worktrees/_commander/phase3-decisions.md`'s own per-decision
`**Tests.**` sentence, split into its individual clauses) mapped to a
real, `grep`-verified test, or marked MISSING — a finer-grained pass than
the "Outstanding required tests" section below, which only ever tracked
the specific bullets a prior review round had already flagged. Built by
listing every `func Test*` in the packages phase3-decisions.md's own
work-split table assigns each decision to (~1,000 functions across
`internal/{domain,store,graph,lifecycle,obligation,ingest,tools,retrieve,
policy,invocation,gcqueue}`), then matching each clause against that real
list; a clause with no reasonably confident match is MISSING rather than
forced onto an unrelated test. Package/file are given once per test the
first time it appears in a row when not already obvious from context.

| P3 | Clause | Test |
|---|---|---|
| P3-1 | failure injection after each constituent write | `TestFailureInjectionAtomicity` (`internal/obligation/failure_test.go`) — asserts at obligation/failure_test.go:187, both stores via the SQLite suite (sqlite_suite_test.go:146) |
| P3-1 | ignored-error poisoning | `TestTransitionIgnoredErrorPoisons` (`internal/obligation/transition_test.go`) — asserts at obligation/transition_test.go:340/:344, both stores via the SQLite suite (sqlite_suite_test.go:102) |
| P3-1 | inclusive expiry/revocation during a multi-command event | `TestAuthorizationUsesActualAllocatedGrantBoundary` (authorization at the exact allocated sequence, inclusive); `TestAuthorizationIgnoresDeadGrantHistory` (revoked grants never authorize) — both `internal/graph/authorize_seq_test.go`; asserts at graph/authorize_seq_test.go:35/:38/:42 (store-free sequenceFixture) and :77/:79 (eachStore, both stores). The expiry/revocation-during-a-multi-command-event half is MISSING — pending p7-ta/p7-tb |
| P3-1 | retirement at a later sequence | `TestRetirementAuthorizesAndWritesSameExactSequence` (`internal/graph/retirement_sequence_test.go` — the retirement's authorization and its audit write share one exact allocated sequence) — asserts at graph/retirement_sequence_test.go:63 (shared exact sequence) and :66 (refusal past the boundary; store-free stub fixture) |
| P3-1 | Prepare/MarkSent stale after every new semantic record family | `TestP3_1_PrepareMarkSentStaleAfterEverySemanticRecordFamily` (`internal/invocation/p342_p3_1_test.go`) — asserts at invocation/p342_p3_1_test.go:893-894/:899 |
| P3-1 | TargetCall sequence reuse rejected | `TestConformance/SemanticLedgerSeqIsolation` (storetest, registered storetest.go:153, body semantic_ledger.go:15 — asserts at :67-68, and TestConformance runs under both stores); `TestPhase3RowsCarrySemanticSeq` (sqlite — asserts at sqlite/seq_isolation_test.go:20-21) — **Resolved** |
| P3-2 | retry after lifecycle changes and restart | `TestCommandReplayUsesOriginalGrantAndFrozenResult` (`internal/lifecycle`); `TestCompletionReplaysAcrossSQLiteRestart`; `TestP3_2_UnpinReplaySurvivesLaterChangesAndRestart` (`internal/lifecycle/p342b_p3_2b_test.go`, both stores — later lifecycle changes intervene, then the identical retry replays the frozen unpin without allocating a sequence, changed arguments are `ErrEventIDConflict`, and a SQLite close/reopen replays identically) — asserts at lifecycle/outcome_test.go:47; lifecycle/x8_test.go:106/:109/:112; lifecycle/p342b_p3_2b_test.go:84/:88/:94/:103 |
| P3-2 | changed method/principal | `TestReplayUsesRecordedArgumentBoundAndPrincipal` (`internal/lifecycle`) — asserts at lifecycle/receipt_test.go:31/:37/:40 |
| P3-2 | failed attempt then valid retry (Phase 3 service level) | `TestP3_2_FailedAttemptThenValidRetry` (`internal/lifecycle/p342_p3_2_test.go`) — asserts at lifecycle/p342_p3_2_test.go:31/:51 |
| P3-2 | archive→unarchive→old Collect retry | `TestP3_2_ArchiveUnarchiveThenOldCollectRetryReplays` (`internal/lifecycle/p342_p3_2_test.go`) — asserts at lifecycle/p342_p3_2_test.go:120/:125 |
| P3-2 | completed task retry | `TestCompletionReplaysAcrossSQLiteRestart` (`internal/lifecycle/x8_test.go`, SQLite); `TestCompleteTaskResolvesOwnedGoalsAndReplaysFrozenReceipt` (`internal/lifecycle/complete_test.go`, memory half only) — asserts at lifecycle/x8_test.go:106/:112 (SQLite restart); lifecycle/complete_test.go:130/:134 (memory) |
| P3-2 | expired retrieval receipt replay | `TestRetrievalReceiptReplayPrecedesCurrentState` (`internal/retrieve`); `TestP3_2_ExpiredLeaseStillReplaysItsReceipt` (`internal/retrieve/p342b_p3_2_test.go`, both stores — the expiry is proven real by a new request minting a new lease before the replay assertion) — asserts at retrieve/replay_test.go:56; retrieve/p342b_p3_2_test.go:71/:74 |
| P3-2 | concurrent identical requests produce one effect/result | `TestConcurrentIdenticalInvocationsProduceOneEffect` (`internal/tools/outer_test.go`, memory-only — toolFixture builds a memory store; the both-stores port is `TestP3_24_ConcurrentIdenticalRetriesCommitOneEffectOnBothStores`, tools/p342_p3_24_test.go); `TestConcurrentCompletionExecutesOnce` (`internal/lifecycle`) — asserts at tools/outer_test.go:27/:34; lifecycle/concurrency_test.go:55 |
| P3-3 | same textual key in all three namespaces | `TestP3_3_SameTextualKeyInAllThreeNamespaces` (`internal/graph/p342_p3_3_test.go`) — asserts at graph/p342_p3_3_test.go:70/:77 |
| P3-3 | literal lifecycle IDs never resolve agent/observation records | `TestP3_3_SameTextualKeyInAllThreeNamespaces` (`internal/graph/p342_p3_3_test.go`, observation namespace included) — asserts at graph/p342_p3_3_test.go:95 |
| P3-3 | two agents cannot overwrite each other | `TestAgentUpdatesOwnPreUpgradeKey` ("other agent's key" subtest, `internal/graph/legacy_agent_key_test.go` — the second agent's write is its own first version and leaves the first current, both stores) — asserts at graph/legacy_agent_key_test.go:77/:81 |
| P3-3 | authority/boundary mismatch | `TestAuthorizeSupersession_DifferentAccessBoundariesFail` (boundary half); `TestP3_3_SupersessionAuthorityMismatch` (`internal/domain/p342b_p3_3_test.go`, authority half on the agent-key path — key/task/agent/boundary held exactly equal, only authority varies, with a matched control) — asserts at domain/authz_test.go:976; domain/p342b_p3_3_test.go:32/:42/:51/:58 |
| P3-3 | namespace round-trip and old-key migration | `TestAgentUpdatesOwnPreUpgradeKey` (`internal/graph`); `TestUpgradeAgentOwnOldKey_G5` (`internal/ingest`); `TestUpgradeCurrentNamespace`; `TestUpgradeCurrentVersionNamespaces` (`internal/store/sqlite/upgrade_test.go`, the real migrations rather than hand-inserted `Namespace=""` rows) — **Resolved** — asserts at graph/legacy_agent_key_test.go:77/:81; ingest/upgrade_restatement_test.go:251/:259; sqlite/upgrade_test.go:358-360/:703-705 |
| P3-3 | duplicate occurrence never becomes current | `TestD10_DuplicateDirectiveNeverCurrent`, `TestD10_MappedDuplicateNeverCurrent` (`internal/graph/current_test.go`) — asserts at graph/current_test.go:107/:326 |
| P3-4 | identical restatement after Resolve/Unpin/Archive | `TestLinkDuplicate_RestatementAfterResolveStaysResolved` (`internal/graph/duplicate_test.go`); `TestP336_UnpinnedRestatementStaysUnpinned`; `TestP336_ResolvedRestatementStaysResolved` (`internal/ingest`); `TestP3_4_RestatementAfterArchiveStaysArchived` (`internal/ingest/p342b_p3_4_test.go`, the Archive half — the archived goal stays current and untouched) — asserts at graph/duplicate_test.go:164; ingest/semantic_change_test.go:41/:60; ingest/p342b_p3_4_test.go:43 |
| P3-4 | changed accepted versus ignored attributes | `TestDeclareCreationUsesStoredDefaultsAndCopiesAcceptedInputs` (`internal/graph`, memory-only — creation_test.go builds one memory store); `TestP3_4_AcceptedVersusIgnoredAttributeRestatements` (`internal/ingest/p342b_p3_4_test.go`, both stores — an explicit-default kind and a diagnosed unknown attribute stay duplicates; `obligation=` is an accepted change and replaces) — asserts at graph/creation_test.go:40; ingest/p342b_p3_4_test.go:66/:92 |
| P3-4 | changed claim | `TestLinkDuplicate_ComparesObligationClaim` (`internal/graph/duplicate_test.go`) — asserts at graph/duplicate_test.go:371 |
| P3-4 | policy bump | `TestDeclarationDedupSurvivesLifecycleAndPolicyChanges` (`internal/graph/declaration_duplicate_test.go`) — asserts at graph/declaration_duplicate_test.go:40/:56 |
| P3-4 | new turn/TTL origin | `TestLinkDuplicate_RejectsNonDuplicates` (`internal/graph/duplicate_test.go` — a changed TURN/TTL eligibility origin is a new version, never a duplicate; TURN/TTL cases :87-97); `TestLinkDuplicate_RestatedAcrossTurns` (same-origin restatement does dedup) — asserts at graph/duplicate_test.go:126/:335-336 |
| P3-4 | unchanged Working snapshot after lifecycle updates | `TestWorkingSnapshotDeclarationPreservesWholeOrderedIdentity` (`internal/graph/snapshot_declaration_test.go`) — asserts at graph/snapshot_declaration_test.go:19/:24 |
| P3-4 | duplicate raw text not an active requirement | `TestNonDirectiveDuplicates_D10` (detection only); `TestP3_4_DuplicateRawTextIsNotAnActiveRequirement` (`internal/ingest/p342b_p3_4_test.go`, both stores — the duplicate holds and creates no obligation, the canonical keeps exactly its one current obligation; changed text stays an active requirement) — asserts at ingest/p342b_p3_4_test.go:133/:136/:144/:151/:164 |
| P3-4 | legacy unknown declaration fails closed | `TestIdenticalRestatementOfUnknownIdentityFailsClosed` (`internal/graph`); `TestUnknownIdentityRestatementIsALineDiagnostic` (`internal/ingest`) — **Resolved** — asserts at graph/legacy_identity_test.go:57/:63; ingest/unknown_identity_test.go:43/:59/:63 |
| P3-4 | same-content explicit replacement (C-1) | `TestReplaceDirectiveReopensWithIdenticalContentAndRetiresObligations` (`internal/lifecycle`, memory-only — replace_test.go builds one memory store) — asserts at lifecycle/replace_test.go:84/:93 |
| P3-5 | v1 grant cannot authorize v2 | `TestTypedGrantNeverFollowsLatestVersion` (`internal/domain/grant_target_test.go`) — asserts at domain/grant_target_test.go:40 |
| P3-5 | revision changes do not invalidate a still-live version grant | `TestP3_5_RevisionChangeDoesNotInvalidateLiveVersionGrant` (`internal/graph/p342_p3_5_test.go`) — asserts at graph/p342_p3_5_test.go:79/:82 |
| P3-5 | item/key-string collision | `TestGrantTargetCanonicalIsolation` (`internal/domain/grant_target_test.go`) — asserts at domain/grant_target_test.go:14/:20/:24 |
| P3-5 | exact target-set validation | `TestAuthorizeGrantIssuance` (`internal/domain/authz_test.go`) — asserts at domain/authz_test.go:673/:688/:699 |
| P3-5 | legacy grant inertness | `TestTypedGrantNeverFollowsLatestVersion` (legacy half: a `TargetIDs`-only grant never authorizes a typed obligation target, `internal/domain/grant_target_test.go`) — asserts at domain/grant_target_test.go:46 |
| P3-5 | revocation after retirement | `TestP3_5_RevocationAfterRetirement` (`internal/lifecycle/p342_p3_5_test.go`) — asserts at lifecycle/p342_p3_5_test.go:86/:95 |
| P3-5 | expiry at each indirect write | `TestRetirementAuthorizesAndWritesSameExactSequence` (expired-grant half — an obligation retirement is refused when the grant's sequence boundary has passed, `internal/graph/retirement_sequence_test.go`) — asserts at graph/retirement_sequence_test.go:66; expiry at each *other* indirect write is MISSING — pending p7-ta/p7-tb |
| P3-6 | N sources produce O(N) members/edges | `TestDerivedCoverageWritesOneSetAndLinearEdges` (`internal/graph/coverage_link_test.go`) — asserts at graph/coverage_link_test.go:25 |
| P3-6 | no union copied N times | `TestDerivedCoverageWritesOneSetAndLinearEdges` (`internal/graph/coverage_link_test.go`) — asserts at graph/coverage_link_test.go:29 (one set, one shared CoverageID) |
| P3-6 | complete reconstruction after restart | `TestP3_6_CoverageReconstructionAfterRestart` (`internal/graph/p342_p3_6_test.go`) — asserts at graph/p342_p3_6_test.go:137 |
| P3-6 | missing source or nested expired lease fails admission | `TestApplyRejectsProjectionSourceWithoutInheritedCoverage`; `TestProjectionRequiresOriginalLiveLease` (`internal/retrieve`) — asserts at retrieve/apply_test.go:216; retrieve/dependencies_test.go:62/:68 |
| P3-6 | semantic support excludes transcript-only provenance | `TestToolAcknowledgmentIsNeverEvidenceSupport` (`internal/tools/ack_evidence_test.go`, memory-only — toolFixture builds a memory store) — asserts at tools/ack_evidence_test.go:27-29/:38-39 |
| P3-6 | bounded limits reject without partial coverage | `TestProjectionRejectsCyclicAndOverBudgetCoverage` (`internal/retrieve`); `TestCoveragePlanIsCompleteBoundedAndPurposeSpecific` (`internal/graph/coverage_plan_test.go`, the plan as a pure function); `TestP3_6_OverBudgetCoverageRejectsWithoutPartialWrites` (`internal/graph/p342b_p3_6_test.go`, both stores — the store after rejection holds no coverage record, members, per-source index rows, or `DERIVED_FROM` edges; the at-limit control commits the complete set) — asserts at retrieve/dependencies_test.go:137/:141; graph/coverage_plan_test.go:31; graph/p342b_p3_6_test.go:86 |
| P3-7 | B's task-visible transcript is not A's membership | `TestP3_7_TaskVisibleTranscriptIsNotMembership` (`internal/graph/p342_p3_7_test.go`) — asserts at graph/p342_p3_7_test.go:100/:105 |
| P3-7 | X1/X3 without X2 cannot form a prefix | `TestMembershipPrefixRejectsGapsAndFalseClosure` (`internal/graph/membership_prefix_test.go`) — asserts at graph/membership_prefix_test.go:73 |
| P3-7 | all calls/results stay grouped | `TestMembershipToolCallsRequireExactOutputAndResultsStayGrouped` (`internal/graph/membership_association_test.go`) — asserts at graph/membership_association_test.go:35/:46 |
| P3-7 | checkpoint issuing round remains uncovered | `TestMembershipPrefixExcludesIssuingRound` (`internal/graph/membership_prefix_test.go`) — asserts at graph/membership_prefix_test.go:31 |
| P3-7 | closure needs successful recorded acknowledgment | `TestMembershipAcknowledgmentRequiresCompleteRoundAndLaterInference` (`internal/graph/membership_acknowledge_test.go`, memory-only — membershipTestStore builds a memory store) — asserts at graph/membership_acknowledge_test.go:136 via membershipFails (membership_round_test.go:17) |
| P3-7 | restart and explicit legacy reconstruction | `TestP3_7_RestartAndExplicitLegacyReconstruction` (`internal/graph/p342_p3_7_test.go`) — asserts at graph/p342_p3_7_test.go:157/:250 |
| P3-8 | Resolve→archive→retrieve stays RESOLVED | `TestGateT05_ResolvedGoalStaysResolved` (`internal/ingest`) — asserts at ingest/gate_traces_test.go:265 |
| P3-8 | Unpin preserves unresolved/blocked obligations and SYSTEM instruction status | `TestPromotedUnkeyedPinCanBeUnpinned` (`internal/lifecycle`); `TestP3_33_UnpinWithUnresolvedObligation` (`internal/lifecycle/p342_p3_33_test.go`, obligation half); `TestP3_8_SystemInstructionStaysProtectedAfterUnpin` (`internal/lifecycle/p342b_p3_8_test.go`, both stores, SYSTEM half — a USER cannot unpin SYSTEM's instruction, SYSTEM's unpin preserves the authority while moving PINNED→DURABLE, and the unpinned SYSTEM instruction still discloses `ExplicitProtectedRemoval` on archive where a twin USER one does not) — **Resolved** — asserts at lifecycle/generation_test.go:136; lifecycle/p342_p3_33_test.go:55/:68/:83 (UNRESOLVED only, no BLOCKED case); lifecycle/p342b_p3_8_test.go:55/:62/:76; the BLOCKED half is MISSING — pending p7-ta/p7-tb |
| P3-8 | lower-authority denial | `TestD1_AuthorizeLifecycleCommand_SourceActor` (`internal/graph/lifecycle_test.go`) — asserts at graph/lifecycle_test.go:62 |
| P3-8 | explicit grant success | `TestD1_AuthorizeLifecycleCommand_Grant` (`internal/graph/lifecycle_test.go`) — asserts at graph/lifecycle_test.go:110 |
| P3-8 | repeated request versus distinct wrong-state request | `TestLifecycle_ExecutesInOrder_P335` (`internal/ingest/working_test.go`); `TestP3_8_DistinctResolveAfterResolvedIsMismatch` (`internal/ingest/p342b_p3_8_test.go`, both stores — a distinct event resolving an already-RESOLVED goal is MISMATCH/NOT_EXECUTED, diagnosed, and changes nothing, while the original event's retry replays) — asserts at ingest/working_test.go:103/:120; ingest/p342b_p3_8_test.go:44/:56 |
| P3-8 | all-or-nothing command sequence | `TestLifecycle_SourceActor_R7` (`internal/ingest/working_test.go`); `TestP3_8_UnauthorizedLaterCommandRollsBackEarlierOnes` (`internal/ingest/p342b_p3_8_test.go`, both stores — the first command executes, the later unauthorized one voids the whole event including the earlier effect; the split-event control shows each alone would execute) — asserts at ingest/working_test.go:133/:136; ingest/p342b_p3_8_test.go:88/:91/:100 |
| P3-9 | hidden OPEN goal | `TestCompleteTaskFailsClosedWithoutPartialEffects` ("hidden goal" case, `internal/lifecycle/complete_test.go`; memory-only) — asserts at lifecycle/complete_test.go:172/:178 |
| P3-9 | broad-scope source with originating T | `TestP3_9_BroadScopeSourceWithOriginatingTaskIsNotTaskOwned` (`internal/lifecycle/p342_p3_9_test.go`) — asserts at lifecycle/p342_p3_9_test.go:51/:64 |
| P3-9 | unresolved obligation after Unpin/materialization disable | `TestCompletionRejectsAllOwnerBlockersBeforeLedger` (`internal/lifecycle/completion_blockers_test.go`, stubbed reader); `TestP3_9_UnresolvedBlockersSurviveRealUnpinAndMaterializationDisable` (`internal/lifecycle/p342b_p3_9b_test.go`, both stores — a real store, a real Unpin, plain and materialization-disabled blockers; the waiver control proves it was the obligation, not the pin, that blocked) — asserts at lifecycle/completion_blockers_test.go:39/:52/:59; lifecycle/p342b_p3_9b_test.go:60/:68/:93 |
| P3-9 | empty goal set | `TestCompleteTaskWithoutGoalsStillRecordsReceiptAndGC` (`internal/lifecycle`, memory-only — newTestStore builds one memory store) — asserts at lifecycle/complete_test.go:199 |
| P3-9 | wrong workflow | `TestCompleteTaskRejectsForeignWorkflowBeforeOwnerQueries` (`internal/lifecycle`, memory-only — newTestStore builds one memory store) — asserts at lifecycle/complete_test.go:28/:33 |
| P3-9 | in-flight operation/open round | `TestCompletionX8RejectsEveryReservationAndOpenExchange` (`internal/lifecycle/completion_blockers_test.go`, stubbed reader); `TestCompletionRejectsInFlightWorkOnRealStores` (`internal/lifecycle/x8_test.go`) — asserts at lifecycle/completion_blockers_test.go:52/:59; lifecycle/x8_test.go:64/:69 |
| P3-9 | grant expires between goals | `TestCompletionGrantExpiringBetweenGoalsFailsWhole` (memory only); `TestP3_9_GrantExpiringBetweenGoalsFailsWholeBothStores` (`internal/lifecycle/p342b_p3_9_test.go`, both stores — the second grant lapses one sequence before its goal's own; the no-expiry control completes both) — asserts at lifecycle/atomicity_test.go:300/:305/:311; lifecycle/p342b_p3_9_test.go:75/:87-88 |
| P3-9 | crash at each goal/task/GC write | `TestEveryConstituentWriteIsAtomic` ("complete task" case, `internal/lifecycle/atomicity_test.go`; memory-only — failure injected at every constituent write, not only the GC write) — asserts at lifecycle/atomicity_test.go:170/:175/:178 via walkWrites |
| P3-9 | completed retry versus new request | `TestCompletionReplaysAcrossSQLiteRestart` (`internal/lifecycle/x8_test.go`, SQLite); `TestCompleteTaskResolvesOwnedGoalsAndReplaysFrozenReceipt` (memory half) — asserts at lifecycle/x8_test.go:106/:112 (SQLite restart); lifecycle/complete_test.go:130/:134 (memory) |
| P3-10 | each allowed/forbidden pair | `TestGenerationChangeClosedPairs` (`internal/policy/generation_test.go`, the closed pair table itself) — asserts at policy/generation_test.go:20 (closed-pair table :13-18) |
| P3-10 | authority/grant/access | `TestGenerationExcludesObligationSourceAndNeedsAuthority` (`internal/lifecycle/generation_test.go`, authority and grant halves, memory-only — newTestStore builds one memory store); `TestP3_10_PromoteDemoteRefuseInaccessibleTarget` (`internal/lifecycle/p342b_p3_10_test.go`, both stores, access half — a full-authority SYSTEM actor gets `ErrNotFound` on a private and a missing target alike, for Promote and Demote; the accessible control carries the boundary across unchanged) — asserts at lifecycle/generation_test.go:92/:95/:98/:106-107; lifecycle/p342b_p3_10_test.go:42/:65 and :48-49/:56-57 and :79-80 |
| P3-10 | retention derivation | `TestGenerationPairsFollowClosedPolicy` (same test, retention assertions, `internal/lifecycle`, memory-only) — asserts at lifecycle/generation_test.go:54-55; policy/generation_test.go:27-28 (pure policy check) |
| P3-10 | excluded roles/source types | `TestGenerationChangeRequirementAndKnowledgeGuards` (role/currentness/obligation-source exclusions, `internal/policy/generation_test.go`); `TestGenerationExcludesObligationSourceAndNeedsAuthority` (authority half) — asserts at policy/generation_test.go:43/:46/:52/:59/:65; lifecycle/generation_test.go:92/:95/:98 |
| P3-10 | expired origin unchanged | `TestP3_10_ExpiredOriginUnchangedByPromotion` (`internal/lifecycle/p342_p3_10_test.go`) — asserts at lifecycle/p342_p3_10_test.go:50/:59-60/:68/:70-74 |
| P3-10 | replay and CAS conflict | `TestP3_10_PromoteReplayAndCASConflict` (`internal/lifecycle/p342_p3_10_test.go`) — asserts at lifecycle/p342_p3_10_test.go:96/:108/:112-113/:122-131 |
| P3-11 | forged SYSTEM issuer rejected | `TestIssueGrantFailsClosed` (`internal/lifecycle/grants_test.go`, memory-only — newTestStore builds one memory store) — asserts at lifecycle/grants_test.go:89-90/:96-97 |
| P3-11 | new source then version grant succeeds | `TestIssueGrantDerivesIssuerAndAuthorizesGrantee` (source seeded in an earlier transaction, `internal/lifecycle/grants_test.go`, memory-only); `TestP3_11_GrantBeforeNonexistentSourceFails` (same-transaction positive control: the grant binds and executes once when the source is created earlier in the same ordered stream, `internal/ingest/p342_p3_11_test.go`) — asserts at lifecycle/grants_test.go:37/:42/:49/:54/:57; ingest/p342_p3_11_test.go:44-50 |
| P3-11 | grant before nonexistent source fails | `TestP3_11_GrantBeforeNonexistentSourceFails` (`internal/ingest/p342_p3_11_test.go`) — asserts at ingest/p342_p3_11_test.go:33-38 (requireAtomic; the atomicity gate helper asserts at ingest/gate_fixtures_test.go:51/:54) |
| P3-11 | revoked/expired grant cannot satisfy | `TestAuthorizeMutation_GrantExpiry` (pure function); `TestInvalidationCannotReuseHistoricalGrantToSatisfy` (invalidation path); `TestP3_11_RevokedOrExpiredGrantCannotSatisfy` (`internal/lifecycle/p342b_p3_11_test.go`, both stores — an authenticated issue/revoke drives the real satisfaction path; the live-grant control satisfies and attributes its transition to the grant, dead grants leave version, history, and assertion cache untouched) — asserts at domain/authz_test.go:370-371/:379-380; domain/transition_v2_test.go:14-15; lifecycle/p342b_p3_11_test.go:88-89/:108-119 |
| P3-11 | lower-authority grant/revoke denial | `TestRevokeGrantNeedsDirectAuthorityAndEndsAuthorization` (revoke half); `TestIssueGrantFailsClosed` (issue half) — both `internal/lifecycle` — asserts at lifecycle/grants_test.go:119-136, memory-only (newTestStore); the SQLite half is MISSING — pending p7-ta/p7-tb |
| P3-11 | no implicit grant from text | `TestP3_11_NoImplicitGrantFromText` (`internal/ingest/p342_p3_11_test.go`) — asserts at ingest/p342_p3_11_test.go:74-75/:97-99 |
| P3-11 | changed grant payload conflicts on retry | `TestP3_11_ChangedGrantPayloadConflictsOnRetry` (`internal/lifecycle/p342_p3_11_test.go`) — asserts at lifecycle/p342_p3_11_test.go:35-36/:47-48/:54-60 |
| P3-12 | SDD punctuation examples | `TestMatchClaim` (`internal/obligation/claim_test.go`, the SDD's own punctuation cases; `TestDeclarePinnedCases` carries none) — asserts at obligation/claim_test.go:54-55 (SDD cases :18-24) |
| P3-12 | ASCII anchoring and fuzz | `FuzzMatchClaim` (`internal/obligation/claim_test.go`) — asserts at obligation/claim_test.go:80-81/:87/:91/:95-96 |
| P3-12 | explicit precedence | `TestBindPinned` (`internal/obligation/target_test.go`, explicit-attribute precedence) — asserts at obligation/target_test.go:45-47 |
| P3-12 | unknown/missing workspace | `TestBindPinnedUnbound` (`internal/obligation/target_test.go`) — asserts at obligation/target_test.go:94-99 |
| P3-12 | wrong suite/repository/environment/subset | `TestSubjectIdentity` (`internal/obligation/subject_test.go`, every identity field varied) — asserts at obligation/subject_test.go:20-25 |
| P3-12 | fixed/current file mode | `TestSPEC111FixedHashTarget`; `TestSPEC111ClaimsMustCoverTarget` (`internal/obligation/round1_test.go`) — asserts at obligation/round1_test.go:222/:228/:232/:236 and :260-264, both stores via the SQLite suite (:121/:122) |
| P3-12 | duplicates do not create obligations | `TestD13_DuplicateLeavesObligations` (`internal/graph`, the canonical's obligation survives); `TestP3_12_DuplicateCreatesNoObligations` (`internal/graph/p342b_p3_12_test.go`, both stores — nothing bound to the duplicate, no extra version anywhere in the task, the canonical's obligation still bound to the canonical alone) — asserts at graph/obligation_test.go:144-145; graph/p342b_p3_12_test.go:36-37/:44-45/:53-58 |
| P3-12 | legacy claims remain unbound | `TestP3_12_LegacyClaimsRemainUnbound` (`internal/obligation/p342_p3_12_test.go`) — asserts at obligation/p342_p3_12_test.go:151-152/:160-164 (memory :92-97 + sqlite :98-107) |
| P3-13 | full status matrix | `TestValidObligationTransitionMatrix` (`internal/domain/obligation_test.go`) — asserts at domain/obligation_test.go:43-44 |
| P3-13 | forged persisted-row fields | `TestP3_13_ForgedPersistedRowFields` (`internal/obligation/p342_p3_13_test.go`) — asserts at obligation/p342_p3_13_test.go:94-95/:102-103 |
| P3-13 | retired/WAIVED versions | `TestValidObligationTransition_WaivedIsTerminal` (WAIVED); `TestTransitionRetiredVersion` (retired, `internal/obligation/transition_test.go`) — asserts at domain/obligation_test.go:51-54; obligation/transition_test.go:278-279, both stores via the SQLite suite (:100) |
| P3-13 | ABA revision race | `TestConformance/ObligationTransitions` (storetest, registered storetest.go:188, body ledger.go:190) — asserts at storetest/ledger.go:212 (stale-revision ABA) and :277 (rejects table); TestConformance runs under both stores |
| P3-13 | absent/future/private evidence | `TestTransitionEvidence` (`internal/obligation/transition_test.go`, both stores via the SQLite suite :98 — a missing and a hidden evidence ID are one uniform `ErrNotFound` (so "future" collapses into absent, per ruling), private evidence is `ErrInvalidAuthorityPromotion`, and nothing changes state); `TestObservationIntentEvidenceReferenceIsExclusive` (`internal/domain/resource_intent_test.go`, shape only); `TestP3_13_ObservationEvidenceAbsentForeignOrPrivateRefused` (`internal/obligation/p342b_p3_13_test.go`, both stores — a nonempty ID naming nothing, a TOOL occurrence from another execution, and one from the run's own execution but owned by another agent are each refused `ErrInvalidRecord` with no observation written; the valid control closes its run) — **Resolved** — asserts at obligation/transition_test.go:208-209/:212-216; domain/resource_intent_test.go:12-13; obligation/p342b_p3_13_test.go:45-46/:57-61/:90-99 |
| P3-13 | matcher name without grant | `TestAuthorizeMutation_T06_HarnessCannotAssertWithoutGrant` (no matcher named at all); `TestP3_13_MatcherNameWithoutGrantRefused` (`internal/domain/p342b_p3_13_test.go` — a named matcher with no grant, a plain grant, or a grant bound to another name, version, or target is refused; only the exactly matching grant authorizes and attributes to its grant ID) — asserts at domain/authz_test.go:275-276; domain/p342b_p3_13_test.go:52-53/:68-73 |
| P3-13 | nonempty bogus fingerprint ID | `TestObservationCannotFabricateEvidenceOrPass` (empty evidence / PASS-with-failures); `TestP3_13_BogusFingerprintCannotSatisfy` (`internal/obligation/p342b_p3_13_test.go`, both stores — a well-formed fingerprint that is not the workspace's current one is refused whole with `ErrUnknownApplicability`; the real fingerprint satisfies and installs the resource-bound proof) — asserts at domain/observation_test.go:11-22; obligation/p342b_p3_13_test.go:127-135/:145-146 |
| P3-13 | status/history/cache rollback parity | `TestTransitionCASAndReplay` (error and replay); `TestP3_13_FailedCASLeavesStatusHistoryAndCacheInParity` (`internal/obligation/p342b_p3_13_test.go`, both stores — after a stale-`ExpectedRevision` refusal the stored status, revision, assertion and proof caches, and history are exactly the successful transition's; the current-revision control revalidates) — asserts at obligation/transition_test.go:249-267 (both stores via the SQLite suite :99); obligation/p342b_p3_13_test.go:173-183/:191-192 |
| P3-14 | task-wide obligation with agent-private PASS rejected | `TestPrivateFailNeverRejectsTaskProof_SEC29` (rejection direction); `TestP3_14_PrivatePassNeverSatisfiesTaskObligation` (satisfaction direction, `internal/obligation/p342_p3_14_test.go`) — asserts at obligation/round2_test.go:118-129 (both stores via the SQLite suite :156); obligation/p342_p3_14_test.go:60-72/:86-108 |
| P3-14 | private evidence never leaks via receipt/cache/view | `TestRetrievalDeniedAuditCannotPublishSource` (adjacent); `TestP3_14_PrivateEvidenceNeverLeaks` (`internal/obligation/p342_p3_14_test.go`) — asserts at obligation/p342_p3_14_test.go:202-207/:220-223/:254-265/:275-277/:288-290 |
| P3-14 | historical/current views after invalidate/waive/retire | `TestObservationStateChain` (`internal/obligation`, an observation-state chain that never touches these views); `TestP3_14_SatisfiesViewsAfterInvalidateWaiveAndRetire` (`internal/obligation/p342b_p3_14_test.go`, both stores — after each ending the current view empties while history keeps the proof-backed relation unflagged, and an out-of-task viewer gets `ErrNotFound` from both) — asserts at obligation/subject_state_test.go:107-128 (both stores via the SQLite suite :131); obligation/p342b_p3_14_test.go:76-97 |
| P3-14 | bare attestation creates no edge | `TestSatisfiesNoEdgeForAttestation` (`internal/obligation/read_test.go`) — asserts at obligation/read_test.go:130-131, both stores via the SQLite suite (:143) |
| P3-14 | dangling proof rejected by both stores | `TestConformance/SemanticProofReferences` (storetest, registered storetest.go:139, body semantic_proof.go:239 — dangling cases :245-268 assert at :276-277, the no-transition case at :280-284, and TestConformance runs under both stores); `TestProofRequiresBackedDependencyIdentity` (`internal/domain/proof_test.go:5`, pure Validate/clone checks at :7/:14/:19 — no store, adjacent only) |
| P3-15 | bare attestation | `TestAssertionModeIsExplicit` (`internal/domain/assertion_test.go`; package corrected here — the row previously said `internal/obligation`); `TestP3_15_BareAttestationCommitsAssertionOnly` (`internal/obligation/p342b_p3_15_test.go`, both stores — commits the assertion with no proof, evidence, or SATISFIES relation; an attestation carrying resource claims is refused by validation before any write) — asserts at domain/assertion_test.go:7-17; obligation/p342b_p3_15_test.go:34-44/:57-66/:74-78 |
| P3-15 | attestation with citations | `TestAssertionModeIsExplicit` (same test, citation subtests); `TestP3_15_CitedAttestationRecordsCitationsAndRefusesBogusOnes` (`internal/obligation/p342b_p3_15_test.go`, both stores — real citations recorded on the transition and cached on the version, still no fabricated SATISFIES edge; a citation naming no stored occurrence is refused `ErrNotFound` whole, and the same shape with the real citation then commits) — asserts at domain/assertion_test.go:12-13; obligation/p342b_p3_15_test.go:99-106/:116-129 |
| P3-15 | resource-bound assertion invalidated | `TestK1InlineSettleBeforeTransition`; `TestK1DependencySemantics` (`internal/obligation/k1_test.go` — invalidation via the derived-at-read dependency state, not a stored status flag) — asserts at obligation/k1_test.go:163-169/:179-180 and :125-139, both stores via the SQLite suite (:177/:176) |
| P3-15 | unauthorized assertion | `TestAuthorizeMutation_T06_HarnessCannotAssertWithoutGrant` (`internal/domain/authz_test.go`) — asserts at domain/authz_test.go:275-276 |
| P3-15 | mode-conflicting retry | `TestP3_15_ModeConflictingRetry` (`internal/obligation/p342_p3_15_test.go`) — asserts at obligation/p342_p3_15_test.go:78-111 |
| P3-15 | explicit revalidation | `TestReevaluateAfterGrant` (`internal/obligation/reevaluate_test.go`) — asserts at obligation/reevaluate_test.go:31-33/:40-48, both stores via the SQLite suite (:139) |
| P3-15 | migration without invented exemption/proof | `TestP3_15_MigrationInventsNoExemptionOrProof` (`internal/obligation/p342_p3_15_test.go`) — asserts at obligation/p342_p3_15_test.go:161-165/:181-186/:200-220 |
| P3-16 | PASS→FAIL on same fingerprint | `TestProofRejection` (`internal/obligation/evaluate_test.go`) — asserts at obligation/evaluate_test.go:166-187, both stores via the SQLite suite (:135) |
| P3-16 | reversed run arrival | `TestH1StalePassAfterRevert` (`internal/obligation/round2_test.go`) — asserts at obligation/round2_test.go:145-146, both stores via the SQLite suite (:157) |
| P3-16 | partial failure does not invalidate unrelated valid proof | `TestProofRejection` (partial-failure case, `internal/obligation/evaluate_test.go`) — asserts at obligation/evaluate_test.go:172-173, both stores via the SQLite suite (:135) |
| P3-16 | refresh with expired grant | `TestProofRefresh`; `TestP3_16_RefreshWithExpiredGrantRetainsOldProof` (`internal/obligation/p342b_p3_16_test.go`, both stores — a second PASS past the grant's expiry retains the old proof and revision with no refresh pair; the live-grant control refreshes with the audited pair) — asserts at obligation/evaluate_test.go:198-199; obligation/p342b_p3_16_test.go:66-70/:80-87 |
| P3-16 | crash between pair writes leaves neither partial effect | `TestFailureInjectionAtomicity` (`internal/obligation/failure_test.go`) — asserts at obligation/failure_test.go:187, both stores via the SQLite suite (:146) |
| P3-16 | blocked/waived behavior | `TestProofRejection`; `TestP3_16_BlockedWaitsForUnblockAndWaivedIsTerminal` (`internal/obligation/p342b_p3_16_test.go`, both stores — BLOCKED records a PASS as evidence but it cannot satisfy, USER cannot unblock SYSTEM's block, an authorized unblock then satisfies; WAIVED is terminal — later PASS and direct satisfy are refused with history frozen) — asserts at obligation/evaluate_test.go:166-187; obligation/p342b_p3_16_test.go:113-128/:137-149 |
| P3-17 | forged USER proof path | `TestObservationCannotFabricateEvidenceOrPass`; `TestP3_17_ForgedUserProofPathIsInert` (`internal/obligation/p342b_p3_17_test.go`, both stores — USER cannot register, report, or reevaluate (`ErrInvalidAuthorityPromotion`, nothing persisted), and text claiming PASS is inert through both reevaluation and attestation citation; domain validation plus run-reporter equality are the two surviving layers) — asserts at obligation/p342b_p3_17_test.go:32-56/:73-77/:87-88/:93-96/:104-105 |
| P3-17 | observation before grant | `TestSEC19ReevaluateIgnoresHiddenObservation` (adjacent — the hidden observation never shadows the accessible PASS, obligation/round1_test.go:320-326, both stores via the SQLite suite :124); nothing asserts the obligation being UNSATISFIED *before* the grant arrives — MISSING — pending p7-ta/p7-tb (partial support: obligation/reevaluate_test.go:40-41, a grant alone never satisfies) |
| P3-17 | unblock then reevaluate | `TestReevaluateStaleEvidenceAndUnblock` (`internal/obligation/reevaluate_test.go`) — asserts at obligation/reevaluate_test.go:82-83/:93-94/:101-102/:111-112, both stores via the SQLite suite (:140) |
| P3-17 | replacement revalidation without inherited grant | `TestDeclareForReplacement` (`internal/obligation/replacement_test.go`); `TestGateT02_ReplacementRetiresOldRequirement`'s `t02Reevaluation` (`internal/ingest/gate_traces_test.go`, the satisfied-obligation repeat through ingest) — asserts at obligation/replacement_test.go:64-68 (both stores via the SQLite suite :93); ingest/gate_traces_test.go:265 |
| P3-17 | unavailable matcher version stays unresolved | `TestP3_17_UnavailableMatcherVersionStaysUnresolved` (`internal/obligation/p342_p3_17_test.go`) — asserts at obligation/p342_p3_17_test.go:51-52/:85-93 |
| P3-17 | deterministic selection and retry | `TestCanonicalRunSubject`; `TestP3_17_ReevaluationSelectsDeterministicallyAndReplays` (`internal/obligation/p342b_p3_17_test.go`, both stores — run order beats deliberately opposite arrival order in accepted state, receipt, and proof; the identical retry replays its receipt and a changed request at a stale revision is `ErrVersionConflict`) — asserts at obligation/subject_test.go:53-58; obligation/p342b_p3_17_test.go:131-132/:151-152/:164-165/:175-180 |
| P3-18 | HARNESS declaration | `TestDeclareHarness` (`internal/obligation`) — asserts at obligation/declare_test.go:213-221/:245-251, both stores via the SQLite suite (:92) |
| P3-18 | SYSTEM source without authority/grant denied | `TestDeclareHarness` (same test, denial subtests) — asserts at obligation/declare_test.go:234-235/:268-270 |
| P3-18 | stable slots and source replacement | `TestDeclarePinnedReplacementVersions`; `TestP3_18_HarnessSlotStableAcrossSourceReplacement` (`internal/obligation/p342b_p3_18_test.go`, both stores — one authorized replacement retires every source-bound slot version atomically and the slot reopens as v2 under the same obligation ID with a fresh UNRESOLVED and its own declaration companion; the retired v1 stays immutable) — asserts at obligation/declare_test.go:306-312 (both stores via the SQLite suite :91); obligation/p342b_p3_18_test.go:40-42/:62-84 |
| P3-18 | exception with unresolved/blocked obligation still prevents completion | `TestUnfinishedTaskObligations` (`internal/obligation/read_test.go`) — asserts at obligation/read_test.go:23-58, both stores via the SQLite suite (:141) |
| P3-18 | unauthorized exception | `TestSetMaterialization` (`internal/obligation/materialization_test.go`) — asserts at obligation/materialization_test.go:70-76, both stores via the SQLite suite (:95) |
| P3-18 | audit/CAS/rollback | `TestFailureInjectionAtomicity`; `TestP3_18_MaterializationAuditCASAndRollback` (`internal/obligation/p342b_p3_18_test.go`, both stores — the concurrent re-declare is `ErrVersionConflict`, the audit event names transition/actor/receipt and is append-only across re-enable, and a failure injected at each constituent write commits nothing — no flip, revision, event, or sequence) — asserts at obligation/failure_test.go:187; obligation/p342b_p3_18_test.go:131-136/:146-158/:190-191 |
| P3-18 | no tool-created obligation | `TestToolAcknowledgmentIsNeverEvidenceSupport` (adjacent, memory-only); `TestP3_18_NoToolCreatedObligation` (`internal/tools/p342b_p3_18_test.go`, both stores — remember/update_state/completion-claim/checkpoint handlers all run for real and create no obligation version, audit event, declaration, or grant, while positive controls prove the counters see the trusted HARNESS path) — asserts at tools/ack_evidence_test.go:27-29/:38-39 (memory-only: toolFixture); tools/p342b_p3_18_test.go:58-84/:133-152 (both stores via p24Stores) |
| P3-19 | initial delayed W1 PASS cannot establish baseline | `TestP3_19_InitialDelayedW1PassCannotEstablishBaseline` (`internal/obligation/p342_p319_test.go`) — asserts at obligation/p342_p319_test.go:66-73/:91-99 |
| P3-19 | W2 update then delayed W1 update | `TestResourceBaselineAndOrdering` (`internal/obligation/resource_test.go`) — asserts at obligation/resource_test.go:122-140, both stores via the SQLite suite (:104) |
| P3-19 | gap→UNKNOWN | `TestGateT07_Repeats` (revision-gap subtest, `internal/ingest/gate_t07_test.go`) — asserts at ingest/gate_t07_test.go:492-502 |
| P3-19 | authoritative resync | `TestGateT07_Repeats` (resync subtest) — asserts at ingest/gate_t07_test.go:504-507 |
| P3-19 | wrong reporter/resource | `TestResourceBaselineAndOrdering` (`internal/obligation/resource_test.go`) — asserts at obligation/resource_test.go:144-152 |
| P3-19 | same-content state at a new revision | `TestResourceInvalidationT07` (`internal/obligation/resource_test.go`); `TestP3_19_CurrentContentClaimsMatchAuthoritativeState` (`internal/obligation/p342b_p3_19_test.go`, both stores — CURRENT_CONTENT is satisfied only by this file's current bytes at the authoritative revision: other bytes, current bytes at a stale revision, another file's content, and a fixed-content claim on a CURRENT_CONTENT target are each `ErrUnknownApplicability`; a path edit then new content re-proves while the FIXED_HASH snapshot claim survives the same edit — the fixed-content gate whose solo removal survives is the second, layered check) — asserts at obligation/resource_test.go:198-241 (both stores via the SQLite suite :106); obligation/p342b_p3_19_test.go:49-96 |
| P3-19 | current/fixed file mode | `TestP3_19_CurrentContentClaimsMatchAuthoritativeState` (`internal/obligation/p342b_p3_19_test.go`, both stores — current and fixed modes side by side: the CURRENT_CONTENT half at :49-96, the FIXED_HASH half at :122-127); `TestSPEC111FixedHashTarget` (`internal/obligation/round1_test.go`, fixed half — asserts at :260-264, both stores via the SQLite suite :122) |
| P3-19 | restart and request replay | `TestRunAndObservationReceipts`; `TestP3_19_RestartReplaysRequestsFromDurableState` (`internal/obligation/p342b_p3_19_test.go`, SQLite close/reopen — identical run-registration and observation requests replay their receipts with no sequence allocated, a changed typed field is `ErrEventIDConflict`, records survive verbatim, and the reopened service accepts a new request; the memory backend has no restart to assert) — asserts at obligation/observation_test.go:193-223 (both stores via the SQLite suite :151); obligation/p342b_p3_19_test.go:220-251/:275-276 |
| P3-20 | `./a.go`, `a.go`, `src/../a.go` equivalence within one base/resource | `TestFrozenLocatorKeyMatchesLiveRuleV1` (`internal/store/sqlite/steps_test.go`); `TestResourceLocatorIsScopedAndLexical` (`internal/domain/resource_locator_test.go`) — asserts at sqlite/steps_test.go:88-89; domain/resource_locator_test.go:10-13 |
| P3-20 | different bases/worktrees stay distinct | `TestResourceLocatorIsScopedAndLexical` (same test) — asserts at domain/resource_locator_test.go:17-19/:24-25/:30-31 |
| P3-20 | uncertain aliases invalidate conservatively | `TestP3_20_UncertainAliasesInvalidateConservatively` (`internal/obligation/p342_p320_test.go`) — asserts at obligation/p342_p320_test.go:39-41/:56-65/:69-71/:76-82 |
| P3-20 | T1 binding unaffected by T2 declaration | `TestH2BindingVersionsDoNotWedgeDeclaration`; `TestP3_20_TaskOneBindingUnaffectedByTaskTwoDeclaration` (`internal/obligation/p342b_p3_20_test.go`, both stores — two tasks sharing one resource with identical specs: T2's binding is never a candidate for T1's sources, T2's v2 bump changes neither T1's current binding nor the ws reference T1's first obligation recorded, and `CurrentWorkspaceBindingsByContext` partitions exactly per task) — asserts at obligation/round2_test.go:306/:314-315 (both stores via the SQLite suite :165); obligation/p342b_p3_20_test.go:59-128 |
| P3-20 | completed-task resource report | `TestP3_20_CompletedTaskDoesNotSuppressInvalidation` (`internal/obligation/p342_p320_test.go`) — asserts at obligation/p342_p320_test.go:99-101/:120-128 |
| P3-20 | no filesystem/network reads during replay | `TestP3_20_ReplayPackagesImportNoFilesystemOrNetwork` (`internal/ingest/p342b_p3_20_test.go` — static: parses every production file of `internal/ingest`/`obligation`/`graph` and fails on stdlib imports in the reading families beyond the `net/url` locator-parsing allowlist, with vacuity guards that the probed functions are actually found); `TestP3_20_ReplayUsesStoredBytesNotTheLocator` (same file, both stores — behavioural: locators poisoned with a path under a nonexistent directory and an RFC 6761 `.invalid` URL still ingest over both stores, the locator is stored verbatim beside the ingested bytes' hash, and replay is byte-identical writing nothing) — **Resolved** (SPEC-5.1's MISSING closed by the round-5 t2 test pass; the `imports_test.go` package-boundary check remains as a weaker adjacent layer, no longer needed for coverage) — asserts at ingest/p342b_p3_20_test.go:95/:99/:106/:111 (static) and :161/:166/:179-184 (behavioural) |
| P3-21 | forged PASS in TOOL/USER/AGENT/retrieved text inert | `TestObservationCannotFabricateEvidenceOrPass`; `TestP3_21_ForgedPassTextIsInert` (`internal/obligation/p342b_p3_21_test.go`, both stores — six single-axis forgeries of the run's evidence occurrence (USER/AGENT/RETRIEVED_CONTENT authority, another execution's TOOL, a TOOL call that produced nothing, a TOOL escaped to session scope) are each refused with nothing persisted and no reevaluation selection, and the genuine report satisfies; service and store are the two layers) |
| P3-21 | wrong/missing span | `TestObservationIntentEvidenceReferenceIsExclusive`; `TestP3_21_WrongOrMissingSpanReferencesRefused` (`internal/obligation/p342b_p3_21_test.go`, both stores — a bare in-range index, an out-of-range index, a span alongside the genuine item, no reference, and a nonexistent item are one uniform `ErrInvalidRecord` that never closes the run, and the genuine reference closes it) |
| P3-21 | changed typed field conflicts on retry | `TestRunAndObservationReceipts` |
| P3-21 | raw environment values rejected | `TestP3_21_RawEnvironmentValuesRejected` (`internal/obligation/p342_p3_21_test.go`) |
| P3-21 | immutable envelope restart | `TestRunAndObservationReceipts`; `TestP3_21_EnvelopeImmutableAcrossRestart` (`internal/obligation/p342b_p3_21_test.go`, SQLite close/reopen — records replay byte-identical, a contradictory second terminal is `ErrInvalidTransition`, a changed payload is `ErrEventIDConflict`, and a genuinely new run's observation still lands; a single dropped service gate survives only because 0030's partial index is the second, layered check) |
| P3-21 | malformed counts/completeness | `TestResourceObservationRecordResults`; `TestP3_21_MalformedCountsAndCompletenessRejectedAtomically` (`internal/obligation/p342b_p3_21_test.go`, both stores — unknown completeness, passed>total, passed+failed>total, skipped mismatch, PASS with failures, a terminal tests result without the workspace fingerprint, and a file-read without a content hash are uniformly `ErrInvalidRecord` and atomic; a well-formed PARTIAL stores with the run open and a complete PASS closes and satisfies) |
| P3-21 | evidence boundary/session/execution mismatch | `TestConformance/SemanticObservationEvidenceExecution` (storetest); `TestP3_21_EvidenceBoundaryAndSessionMismatchRefused` (`internal/obligation/p342b_p3_21_test.go`, both stores — with the execution axis held constant, agent-partition and other-agent-TURN evidence is refused at insert and other-task/other-workflow evidence at the run, while the genuine report passes; the memory-backend guard alone survives because the owners-equality layer is second) |
| P3-22 | 29→7→1→PASS across fingerprints forms one chain | `TestP3_22_WorkedExampleChainAcrossFingerprints` (`internal/obligation/p342_p322_test.go`) |
| P3-22 | distinct repository/directory/environment/coverage subjects never replace | `TestSubjectIdentity`; `TestFileSubjectIgnoresMode` |
| P3-22 | PARTIAL+PASS rejected for replacement | `TestObservationStateGating`; `TestP3_22_PartialOrStaleNeverReplacesEstablishedState` (`internal/obligation/p342b_p3_22_test.go`, both stores — the cited test probes PARTIAL only before any state exists; here an accepted state survives a newer run's PARTIAL PASS, TIMEOUT, and complete PASS of a stale fingerprint unchanged with no supersession filed, while the next complete PASS of the current fingerprint does replace it — currency, not blanket refusal) |
| P3-22 | run2 before run1, including identical fingerprints | `TestGateT07_Repeats` (out-of-order subtest) |
| P3-22 | matcher upgrade does not split identity | `TestP3_22_MatcherUpgradeDoesNotSplitIdentity` (`internal/obligation/p342_p322_test.go`) |
| P3-22 | state authority/boundary checks | `TestObservationSupersessionRequiresTrustedRuleActor`; `TestP3_22_StateSupersessionChecksBoundary` (`internal/obligation/p342b_p3_22_test.go`, both stores — authorization is access-first (another task or session gets `ErrNotFound` before any authority question) and refuses unequal endpoint boundaries even to the trusted insider; on the live path a second task's PASS of the same subject files its own partition with its own watermark and no cross-partition SUPERSEDES edge) |
| P3-22 | stale current-state applicability after edit | `TestDUR31SubjectApplicabilityIsDerived` |
| P3-23 | cross-task/private-proof fan-out | `TestConcurrency_InvalidationVsObservation`; `TestP3_23_InvalidationFansOutAcrossTasksAndPrivateProofs` (`internal/obligation/p342b_p3_23_test.go`, both stores — the cited test runs one task, one obligation, no private proof; here one edit invalidates task one's public proof, task two's own-partition proof, and a proof resting on TURN-private evidence in the same transaction, and task one re-satisfies at the new authoritative content) |
| P3-23 | revoked grant still invalidates without forging live authorization | `TestInvalidationCannotReuseHistoricalGrantToSatisfy` |
| P3-23 | path aliases/unknown paths | `TestSEC17PathCurrencyIgnoresEarlierHistory`; `TestH2PathCurrencyIgnoresLaterUnrelatedEdits`; `TestP3_23_ReportAliasesRefusedAndDirectoriesHitContainedProofs` (`internal/obligation/p342b_p3_23_test.go`, both stores — every alias spelling of docs/a.md (interior dot, doubled slash, leading dot, absolute, parent hop, trailing slash, empty, duplicate) is one uniform `ErrInvalidRecord` consuming no revision, and a changed directory naming no file conservatively invalidates the contained file's proof; the intent gate alone survives because the record gate is the second layer) |
| P3-23 | >one page of proofs | `TestResourceInvalidationPagingAndLimit` |
| P3-23 | limit/crash at final page rolls back state and every status | `TestFailureInjectionAtomicity`; `TestP3_23_SettlementLimitAndFinalPageCrashRollBackEverything` (`internal/obligation/p342b_p3_23_test.go`, both stores — a settlement pass limited to one settles at most one with every obligation still reading UNRESOLVED (derivation is a pure read), and a crash after the final page persists nothing — settlements, cursor, and stored statuses alike — with the next clean pass settling all five from the rolled-back cursor) |
| P3-23 | unrelated resource unaffected | `TestSEC18DeadSubjectStatesDoNotWedgeReports`; `TestP3_23_UnrelatedResourceEditsLeaveOtherResourceProofsUntouched` (`internal/obligation/p342b_p3_23_test.go`, both stores — two resources carrying the same canonical path, each with its own binding and live CURRENT_PATH proof: an edit of one invalidates only its own proof and leaves the other's obligation byte-identical (status, proof, revision, history), in both directions, with the untouched resource's next report still landing) |
| P3-23 | reporter receives no hidden IDs/counts | `TestXREV11StalePathClaim` (adjacent); `TestP3_23_ReportersReceiptCarriesNoHiddenIDsOrCounts` (`internal/obligation/p342b_p3_23_test.go`, both stores — four invalidated proofs yield exactly one RESOURCE_UPDATE reference naming the reporter's own update: no other result arm, no proof/obligation/declaration/settlement ID, no count; the stored receipt's result is byte-identical with clean arguments, the exact retry replays writing nothing, and the invalidation is visible only downstream in derivation and settlement) |
| P3-23 | UNKNOWN cannot retain resource-derived satisfaction | `TestResourceGapBecomesUnknown` (`internal/obligation/resource_test.go`); `TestGateT07_Repeats` (gap subtest, `internal/ingest`) |
| P3-24 | same `call_1` spelling in different outputs | `TestToolCallSpellingIsScopedToOutput` (`internal/tools`) |
| P3-24 | same invocation with different tool/principal/args conflicts | `TestP3_24_SameInvocationDifferentToolPrincipalOrArgsConflicts` (`internal/tools/p342_p3_24_test.go`) |
| P3-24 | no execution from partial assistant output | `TestInvocationRequiresCompletedOutputAndCurrentExactOwner` ("partial" case, `internal/tools/invocation_records_test.go`) |
| P3-24 | concurrent retry | `TestConcurrentIdenticalInvocationsProduceOneEffect`; `TestP3_24_ConcurrentIdenticalRetriesCommitOneEffectOnBothStores` (`internal/tools/p342_p3_24_test.go` — the cited test runs the memory backend only; the port runs both stores: eight goroutines submit the identical request at once, every attempt returns the same receipt, exactly one result member joins the exchange, the replays write no state of their own, and a request on a call the transcript does not name still fails closed) |
| P3-24 | ignored partial error rollback | `TestExecutorFailureReturnsNoReceiptAndPoisonsEvent`; `TestP3_24_PartialFailureInsideToolExecutionRollsBackEverything` (`internal/tools/p342_p3_24_test.go`, both stores — a keyed write whose work all lands and whose handler then dies, and a raw store write followed by the same death, each surface the error and leave sequence, receipt, exchange members, and items untouched, and the identical request afterwards executes freshly with no conflict or phantom receipt) |
| P3-24 | byte-identical missing/private citation error | `TestKeyedWriteCitationFailuresAreUniformAndAtomic` |
| P3-25 | A/B same key isolation | `TestKeyedWritesDeduplicateReplaceAndStayPerAgent` |
| P3-25 | narrower accessible citation does not change key boundary | `TestP3_25_NarrowerCitationDoesNotChangeKeyBoundary` (`internal/tools/p342_p3_25_test.go`, both stores — since round 5's SPEC-5.3 fix the cited evidence's access differs from the frozen boundary in both directions, one TURN-scoped occurrence inside the conjunction and one task-wide occurrence without the agent constraint, with the difference asserted as a precondition, so a filing that takes its boundary from the citation fails either way) |
| P3-25 | cross-session evidence atomic rejection | `TestKeyedWriteCitationFailuresAreUniformAndAtomic`; `TestP3_25_CrossSessionEvidenceIsRefusedUniformlyAndAtomically` (`internal/tools/p342b_p3_25_test.go`, both stores — the cited test probes missing/other-agent items on the memory backend only; here another session's genuine evidence, alone or mixed with valid evidence, gets the same closed not-found as a missing item with nothing committed, the store refuses to even hold a cross-session item inside this session's transaction, and the in-session control lands; single-layer removals survive because session-scoped reads, the accessible set, `loadAccessible`, and authority ordering are four independent layers) |
| P3-25 | exact duplicate versus support change | `TestKeyedWritesDeduplicateReplaceAndStayPerAgent` (`internal/tools/keyed_test.go` — an exact repeat duplicates, a support change replaces) |
| P3-25 | unsupported despite request-transcript edge | `TestCreationDeclarationSupportQualifiesOnlyToolResultTranscripts`; `TestP3_25_RequestTranscriptEdgeLeavesSupportUnsupported` (`internal/graph/p342b_p3_25_test.go`, both stores — the DERIVED_FROM provenance edge a real keyed write files to the request transcript is accepted and readable, yet that transcript as EVIDENCE_SUPPORT and as declared support are both refused atomically and the edge-carrying item reads back unsupported, while a plain semantic EVIDENCE item of the same boundary qualifies both ways; the domain special case alone survives because the kind-category gate is the second layer) |
| P3-25 | allowlist/key fuzz | `TestP3_25_AllowlistAndKeyFuzz` (`internal/tools/p342_p3_25_test.go`) |
| P3-25 | fresh immutable IDs and one current version | `TestAgentKeyAuthorityUsesNamespaceAndExactOwner`; `TestP3_25_FreshImmutableIDsAndOneCurrentVersion` (`internal/tools/p342b_p3_25_test.go`, both stores — across a first filing, its exact retry, a restatement duplicate, a replacement, and a duplicate of the new current, every write is a fresh ID, the key holds exactly one current version counted both through `IsCurrent` and the `CurrentVersions` listing, and every earlier record reads back byte-identical) |
| P3-26 | OPEN/resolved/historical goals | `TestCompletionClaimReportsActualStatusAndMutatesNothing` (`internal/tools`) |
| P3-26 | no-goal error | `TestCompletionClaimReportsActualStatusAndMutatesNothing` (same test, error subtest) |
| P3-26 | private evidence error | `TestKeyedWriteCitationFailuresAreUniformAndAtomic` (adjacent path) |
| P3-26 | all semantic target fields unchanged | `TestCompletionClaimReferenceDoesNotChangeGoal` |
| P3-26 | retry writes one claim | `TestP3_26_RetryWritesOneClaim` (`internal/tools/p342_p3_26_test.go`) |
| P3-26 | REFERENCES never supplies mandatory/completion status | `TestCompletionClaimReferenceDoesNotChangeGoal` (stored goal status/version unchanged); `TestP3_26_RetryWritesOneClaim` (the claim still reports the goal as required) |
| P3-27 | T16 X1–X12 with F1/F2 | `TestT16CheckpointCoversTwelveClosedExchanges` (`internal/tools/scenario_test.go`; `TestGateT16_CheckpointFrontier` skipped itself via `pending()` and is no longer cited) |
| P3-27 | plain summary never checkpoint | `TestGCCheckpointWithoutCompanionRecordsItemSkip` (adjacent) |
| P3-27 | open-round exclusion | `TestClosedPrefixCoverageNamesEveryClosedExchangeOnly` |
| P3-27 | missing middle group rejected | `TestCheckpointRejectsOpenPrefixOversizeAndForeignManifests`; `TestP3_27_MidPrefixGapRejectedBothStores` (`internal/tools/p342b_p3_27_test.go`, both stores — an unacknowledged middle round rejects with `ToolErrorUnavailable` and `LastSeq` unchanged while the control checkpoint still covers the earlier frontier) |
| P3-27 | unseen accessible transcript rejected | `TestCheckpointLookupIgnoresPrivateMembershipsBeforeLimits`; `TestP3_27_UnseenAccessibleTranscriptRejected` (`internal/tools/p342b_p3_27_test.go`, both stores — a RECEIVED-only admission is not a valid source even when every transcript is accessible; a GENERATION_INPUT control on the same shape passes) |
| P3-27 | requirement provenance retained but not retired | `TestP3_27_RequirementProvenanceRetainedButNotRetired` (`internal/tools/p342_p3_27_test.go`) |
| P3-27 | cross-turn semantic summary versus raw leased copy | `TestP3_27_CrossTurnSemanticSummaryVersusRawLeasedCopy` (`internal/tools/p342_p3_27_test.go`) |
| P3-27 | HARNESS form | `TestHarnessCheckpointKeepsHarnessAuthorityAndReplays` |
| P3-27 | oversize/content/coverage limits | `TestCheckpointRejectsOpenPrefixOversizeAndForeignManifests` (same test, size subtests); `TestP3_27_NonTextPartAndCoverageLimits` (`internal/tools/p342b_p3_27_test.go`, both stores — a non-text part is `ToolErrorInvalidArgument`, the closed-prefix and generation-input member caps `ToolErrorTooLarge`, with full-policy controls; the member-limit twin guards survive single removal — defense-in-depth, not a gap) |
| P3-27 | checkpoint chain restart | `TestCheckpointChainCarriesOnlyTheValidatedPrior`; `TestToolReceiptsAndCheckpointSurviveSQLiteReopen`; `TestP3_27_CheckpointChainRestartsOnSQLite` (`internal/tools/p342b_p3_27_test.go`, SQLite — the prior-checkpoint chain survives close/reopen, the head replays without a new effect, and the superseded-link chain is still refused) |
| P3-28 | historical resolved/expired/archived reads unchanged | `TestGetHistoricalGoalIsReadOnly` (`internal/retrieve`); `TestP3_28_ExpiredSourceReadsUnchanged` (`internal/retrieve/p342b_p3_28_test.go`, both stores — a dead-TTL source reads byte-identically and the reads mutate nothing: version, residency, and snapshot sequence stable) |
| P3-28 | wrong session/agent exact ErrNotFound | `TestGetPrivateAndMissingAreIndistinguishable`; `TestP3_28_WrongSessionAndAgentGetExactNotFound` (`internal/retrieve/p342b_p3_28_test.go`, both stores — wrong session, session+agent, agent alone, and missing each return the bare `ErrNotFound` by identity and message, the item untouched) |
| P3-28 | completed requester denied | `TestGetCompletedOriginRemainsHistoricalRead`; `TestP3_28_CompletedRequesterIsDeniedAdmission` (`internal/retrieve/p342b_p3_28_test.go`, both stores — the active task rehydrates while the completed one gets the bare `ErrNotFound`, one denial event, no receipt, and an unchanged lease set) — **Resolved** |
| P3-28 | completed origin accessible only where boundary allows | `TestGetCompletedOriginRemainsHistoricalRead`; `TestP3_28_CompletedOriginBoundaryStillEnforced` (`internal/retrieve/p342b_p3_28_test.go`, both stores — the in-boundary completed-task item still reads as EXPIRED while foreign task, wrong session, and missing get the exact `ErrNotFound` and the item never moves) |
| P3-28 | context_get cannot bypass lease creation | `TestGateT05_ModelPathGet`; `TestP3_28_ContextGetCannotBypassLeaseCreation` (`internal/tools/p342b_p3_28_test.go`, both stores — cited in tools, not retrieve, because the model route lives there and retrieve cannot import it; every successful `context_get` mints exactly one holder-bound lease with a stored receipt, a read-only harness Get mints nothing, and a denied one closes NOT_FOUND with only a denial audit) |
| P3-29 | exact boundary and authority | `TestLeaseRequiresExactHolderAndFiniteAllowance`; `TestP3_29_LeaseRequiresExactHolderIncludingAuthority` (`internal/retrieve/p342b_p3_29_test.go` — the pure liveness predicate: authority promoted or demoted, holder reassigned or moved across tasks, conversation rewritten, or dispatch turn moved each kills the lease) |
| P3-29 | end-of-turn and exact call limit | `TestLeaseLiveCompletedInferenceBoundary` |
| P3-29 | failures/compaction do not consume | `TestLeaseLiveRejectsMissingOrMismatchedState`; `TestP3_29_FailuresAndCompactionDoNotConsumeAllowance` (`internal/retrieve/p342b_p3_29_test.go`, both stores — compaction coalesces, a denied admission consumes no allowance, and a fault mid-write leaves no lease with the retry succeeding) |
| P3-29 | source usage update does not expire | `TestP3_29_SourceUsageUpdateDoesNotExpire` (`internal/retrieve/p342_p3_29_test.go`) |
| P3-29 | supersession does not transfer lease | `TestFindActiveLeaseNeverTransfersAcrossOccurrenceOrAuthority` |
| P3-29 | retry expired result versus new request | `TestApplyPersistsLeaseAndReplaysWithoutRenewal`; `TestP3_29_RetryExpiredResultVersusNewRequest` (`internal/retrieve/p342b_p3_29_test.go`, both stores with a SQLite reopen — the identical request replays the frozen result under the dead lease while a new request mints a fresh lease pinned to the current inference index) |
| P3-29 | concurrent coalescing | `TestConcurrentRetrievalCoalescesOneLease` |
| P3-29 | restart with identical call indexes | `TestSQLiteLeaseSurvivesRestartWithIdenticalCallIndexes` |
| P3-30 | A-private result never TASK-wide | `TestRetrievalDeniedAuditCannotPublishSource`; `TestP3_30_APrivateResultNeverWidensToTask` (`internal/retrieve/p342b_p3_30_test.go`, both stores — a rehydrated agent-private source's projection, coverage, and result each keep the agent constraint within the source boundary, and a different agent in the same task is refused while the holder reads) |
| P3-30 | nested old-lease dependency stays expired despite new lease | `TestNestedOldLeaseCannotBeRenewedByNewRootLease` |
| P3-30 | copied provider representation cannot drop dependency | `TestDerivedRepresentationRetainsProjectionLeaseAndNestedCoverage`; `TestP3_30_CopiedRepresentationCannotDropLeaseOrNestedMember` (`internal/retrieve/p342b_p3_30_test.go` — pure checker: dropping the lease record, the source+lease member, or the nested-coverage member refuses `ErrIncompleteCoverage`, and the missing nested record surfaces as the bare `domain.ErrNotFound` — still a closed refusal) |
| P3-30 | audit denial redaction | `TestRetrievalDeniedAuditCannotPublishSource` (same test) |
| P3-30 | oversize with/without registered projection | `TestP3_30_OversizeWithAndWithoutRegisteredProjection` (`internal/retrieve/p342_p3_30_test.go`) |
| P3-30 | event/result/lease/receipt rollback as one unit | `TestBuildRetrievalRecordsPreservesSourceAndExactLease`; `TestP3_30_RetrievalRecordsRollBackAsOneUnit` (`internal/retrieve/p342b_p3_30_test.go`, both stores — storetest fault injection at each of the seven constituent writes leaves no lease, receipt, result, event, projection item, or coverage and an unchanged sequence counter; an error-swallowing mutant is separately neutralized by the poison guard — defense-in-depth) |
| P3-31 | matrix across every scope/currentness/residency/status/lease combination | `TestEligibilitySeparatesAccessLifetimeAndSelection`; `TestP3_31_FullEligibilityMatrixAcrossScopeCurrentnessResidencyStatusAndLease` (`internal/policy/p342b_p3_31_test.go` — every scope × currentness × residency × origin-task status × lease cell asserts the full four-flag, four-reason `EligibilityResult`, plus an access-denial control that hides every snapshot-dependent reason) |
| P3-31 | leased current OPEN goal still independently a requirement | `TestCurrentGoalAndLeaseHaveIndependentEligibility` |
| P3-31 | expired historical goal admitted only as evidence | `TestObservationApplicabilityCannotBecomeCurrentThroughLease`; `TestP3_31_ExpiredHistoricalGoalAdmittedOnlyAsEvidence` (`internal/policy/p342b_p3_31_test.go` — a TTL-expired HISTORICAL goal is never ordinarily selected, yet a live lease admits it as evidence with `NewSelection` false and cannot promote its selection reason; a live-CURRENT control stays selectable) |
| P3-31 | missing snapshot data | `TestEligibilityMissingSnapshotFailsClosed` |
| P3-31 | counter/wall-clock independence | `TestP3_31_CounterAndWallClockIndependence` (`internal/policy/p342_p3_31_test.go`) |
| P3-31 | stable reasons | `TestOrdinaryLifetimeTTLEdges`; `TestP3_31_StableReasonsForEveryLifetimeOutcome` (`internal/policy/p342b_p3_31_test.go` — the exact closed-registry reason pinned per outcome: live, TTL boundary, expiry, overflow, zero/rewound turn, terminal task, stale dispatch, expired TURN, inactive owner, unknown owner) |
| P3-32 | WORKFLOW OPEN goal survives T1 completion before T2 creation | `TestRegisteredBroadOwnersOutliveTheirTask` |
| P3-32 | same for AGENT pin | `TestRegisteredBroadOwnersOutliveTheirTask` (same test, AGENT subtest) |
| P3-32 | unrelated owner cannot read | `TestP3_32_UnrelatedOwnerCannotRead` (`internal/lifecycle/p342_p3_32_test.go`, the archive/unarchive half); `TestP3_32_UnrelatedOwnerCannotReadViaGet` (`internal/lifecycle/p342b_p3_32_test.go`, both stores — the read half: the uniform `ErrNotFound` for an unrelated agent and a wrong-session reader, an in-boundary positive control, the item untouched) |
| P3-32 | unknown legacy owner | `TestRegisteredBroadOwnersOutliveTheirTask` (register=false → GCIneligible, never archived or selected, `internal/lifecycle/owner_test.go`) |
| P3-32 | restart reconstruction | `TestOwnerRegistrationSurvivesSQLiteRestart` (`internal/lifecycle/owner_test.go`) |
| P3-32 | ending task does not archive broad-scope protected source | `TestP3_32_EndingTaskDoesNotArchiveBroadScopeProtectedSource` (`internal/lifecycle/p342_p3_32_test.go`) |
| P3-33 | TTL boundary/overflow/zero origin/cross-task/completed origin | `TestOrdinaryLifetimeTTLEdges`; `TestTurnOwnershipAndTTL` (boundary/overflow/zero-origin); `TestOrdinaryLifetimeNeverBorrowsAnotherTurnSource` (cross-task turn counter and terminal/completed origin, `internal/policy/temporal_test.go`) |
| P3-33 | new TURN with TTL cannot extend old turn | `TestOrdinaryLifetimeNeverBorrowsAnotherTurnSource` |
| P3-33 | semantic knowledge versus raw projection | `TestDerivedRepresentationRetainsProjectionLeaseAndNestedCoverage`; `TestP3_33_AuthoredKnowledgeOutlivesSourceExpiryButRawProjectionDoesNot` (`internal/policy/p342b_p3_33_test.go` — the eligibility half: the authored fact keeps its own lifetime past its cited evidence's expiry while the raw projection of the same expired evidence is representation-ineligible on every output, and a live lease cannot bridge representation expiry) |
| P3-33 | Unpin with unresolved obligation | `TestP3_33_UnpinWithUnresolvedObligation` (`internal/lifecycle/p342_p3_33_test.go`) |
| P3-33 | archive does not alter status/proof | `TestUnarchiveRestoresResidencyOnlyAndReplays`; `TestP3_33_ArchiveUnarchivePreserveStatusAndProof` (`internal/lifecycle/p342b_p3_33_test.go`, both stores — the obligation side: current, SATISFIED, revision, and proof pointer plus the stored proof itself untouched across archive and unarchive, the unarchive replaying frozen) |
| P3-34 | create→grant→observe versus invalid forward reference | `TestP3_11_GrantBeforeNonexistentSourceFails` (`internal/ingest/p342_p3_11_test.go`); `TestOperationReferenceCannotSkipResolvedValidation` (`internal/domain/semantic_operation_test.go`) |
| P3-34 | source command sees preceding in-transaction changes | `TestCommandsV2_ExecuteInSourceOrder` (`internal/ingest/lifecycle_exec_test.go` — the handler reads the goal an earlier command in the same transaction wrote) |
| P3-34 | grant/revoke order | `TestOps_OrderSequenceAndAliases` (`internal/ingest/ops_test.go`) |
| P3-34 | malformed operation rollback includes turns | `TestP3_34_MalformedOperationRollbackIncludesTurns` (`internal/ingest/p342_p3_34_test.go`) |
| P3-34 | resource-only control cannot open task/turn | `TestOps_ControlEventOpensNothing` |
| P3-34 | mixed-authority deputy attack | `TestOps_SourceSpanActor` |
| P3-34 | old-turn response never relabeled | `TestOutcome_KeepsOriginatingTurn` |
| P3-34 | no completed-task reactivation | `TestCompletedTaskNeverReactivated` |
| P3-34 | semantic writes invalidate unsent previews | `TestP3_34_SemanticWritesInvalidateUnsentPreviews` (`internal/ingest/p342_p3_34_test.go`) |
| P3-35 | new execution order | `TestLifecycle_ExecutesInOrder_P335` |
| P3-35 | old receipt replay no seq/turn/status change | `TestPhase2FixtureReplay` |
| P3-35 | hidden successful versus nonexistent command indistinguishable to another source viewer | `TestRecordsNeverRevealHiddenVersions_SEC22` |
| P3-35 | mismatch/ambiguity cause boundaries | `TestP3_35_MismatchAndAmbiguityCauseBoundaries` (`internal/ingest/p342_p3_35_test.go`) |
| P3-35 | source actor grant at actual sequence | `TestAuthorizationUsesActualAllocatedGrantBoundary` (`internal/graph/authorize_seq_test.go`, both stores); `TestGateT06_GrantExpiryAtActualSequence` (`internal/ingest`) |
| P3-35 | unauthorized command rolls back earlier event writes | `TestLifecycle_SourceActor_R7` |
| P3-36 | Resolve→identical raw Goal restatement remains resolved | `TestP336_ResolvedRestatementStaysResolved` |
| P3-36 | Unpin restatement stays unpinned | `TestP336_UnpinnedRestatementStaysUnpinned` |
| P3-36 | replacement/invalidation old/new authority reconstructible after further mutations/restart | `TestP336_ReplacementHistoryReconstructible` (`internal/ingest`, in-transaction history); `TestP336_HistoryReconstructibleAfterFurtherMutationsAndRestart` (`internal/ingest/p342_p336_test.go`, adds the restart) |
| P3-36 | checkpoint never retires a requirement by source coverage | `TestP336_CheckpointNeverRetiresRequirementBySourceCoverage` (`internal/ingest/p342_p336_test.go`) |
| P3-37 | HARNESS cannot archive SYSTEM target without authority/grant | `TestArchiveRequiresTargetAuthorityOrExactGrant` |
| P3-37 | explicit protected archival audit | `TestArchiveDisclosesExplicitProtectedRemoval`; `TestP3_37_ProtectedRemovalAuditIsStoredAndReadable` (`internal/lifecycle/p342b_p3_37_test.go`, both stores — the durable side: the stored receipt discloses the same flag and names its audit, the audit event carries action/from/to/actor, and a refused archive stores neither receipt nor audit) |
| P3-37 | unarchive leaves RESOLVED/superseded/expired status | `TestUnarchiveRestoresResidencyOnlyAndReplays`; `TestP3_37_UnarchiveLeavesSupersededAndExpiredStatus` (`internal/lifecycle/p342b_p3_37_test.go`, both stores — the superseded directive stays historical and the current one current after unarchive, and the expired TTL still re-archives the item on the next collection) |
| P3-37 | stale revision | `TestUnarchiveRestoresResidencyOnlyAndReplays` (stale `ExpectedVersion` fails `ErrVersionConflict` and a later-archive replay stays idempotent, `internal/lifecycle/archive_test.go`; memory-only) |
| P3-37 | replay | `TestLifecycleEndToEndOnSQLite` ("grant archive revoke" subtest, `internal/lifecycle/sqlite_test.go`, SQLite); `TestUnarchiveRestoresResidencyOnlyAndReplays` (memory half) |
| P3-37 | no content deletion | `TestP3_37_NoContentDeletionOnArchive` (`internal/lifecycle/p342_p3_37_test.go`) |
| P3-38 | mid-turn pending result survives | `TestCollectDecisionMatrix` ("superseded mid-turn kept", `internal/policy/gc_test.go`); `TestP3_39_StableResultsIndependentOfClockAndCounter` (current-turn ephemeral stays PROTECTED, both stores) |
| P3-38 | open exchange and leased historical content survive | lease half: `TestLeaseTakenAfterFirstBatchProtects_SEC42` (`internal/lifecycle/gc_round4_test.go` — a leased item taken into the first Collect batch protects the rest, SEC-4.2's fix); open-exchange half: `TestP3_38_OpenExchangeMemberSurvivesCollectUntilClosed` (`internal/lifecycle/p342b_p3_38_test.go`, both stores — an ended-turn ephemeral that belongs to an OPEN logical exchange gets an explicit PROTECTED decision and stays RESIDENT while its unprotected control archives in the same collection, and once the exchange closes and is acknowledged the next collection archives it) — **Resolved** (SPEC-5.1's MISSING closed) |
| P3-38 | newest eligible checkpoint preserved | `TestCheckpointLookupsFindNewestAndCoveringCheckpoints` (`internal/tools/checkpoint_lookup_test.go` — the real newest/covering lookups, both stores); `TestGCProtectsOnlyTheNewestRelevantCheckpoint` (decision half; memory-only, stubs `checkpointOfItem`) |
| P3-38 | expired lease releases only lease protection | `TestP3_38_ExpiredLeaseReleasesOnlyLeaseProtection` (`internal/lifecycle/p342_p3_38_test.go`) |
| P3-38 | superseded SYSTEM instruction collectible with proper actor | `TestP3_38_SupersededSystemInstructionCollectibleWithProperActor` (`internal/lifecycle/p342_p3_38_test.go`) |
| P3-38 | scope completion | `TestCollectDecisionMatrix` |
| P3-38 | archive→unarchive→old request replay | `TestP3_38_ArchiveUnarchiveThenOldRequestReplays` (`internal/lifecycle/p342_p3_38_test.go`) |
| P3-38 | inaccessible candidate not exposed | `TestCollectArchivesOnlyAuthorizedUnprotectedCandidates`; `TestP3_38_InaccessibleCandidateNotExposedSQLite` (`internal/lifecycle/p342b_p3_38_test.go`, SQLite — the memory half's storemate: an agent-private item appears in no decision list, frozen candidate set, stored receipt, or audit events, while every readable candidate is decided) |
| P3-38 | no LastUsedCall=0 heuristic | `TestP3_38_NoLastUsedCallZeroHeuristic` (`internal/lifecycle/p342_p3_38_test.go`) |
| P3-39 | completion committed then crash before Collect | `TestCompletionReplaysAcrossSQLiteRestart` (`internal/lifecycle/x8_test.go` — completion survives the crash/restart and the queued request still executes; `TestCompletionGCFailureRollsBackGoalsAndTask` asserts the complementary rollback when the GC write itself fails) |
| P3-39 | retry request executes once | `TestCompletionGCRequestExecutesOnceAfterProducerCommit`; `TestP3_39_GCRequestExecutesOnceAcrossSQLiteRestart` (`internal/lifecycle/p342b_p3_39_test.go`, SQLite with the real crash window — the durable request survives the close/reopen and drains exactly once, a second drain executes nothing, and a direct retry replays the frozen receipt; the redundant production backstop is defense-in-depth) |
| P3-39 | failure leaves request pending | `TestFailingGCRequestsAreQuarantined` (adjacent — J5's non-quarantine refinement is `TestJ5ConfigurationErrorsLeaveRequestsPending`, ADR 16) |
| P3-39 | page boundary required target not missed | `TestJ6QueuePrefixCannotHideRunnableTail` |
| P3-39 | limit rollback | `TestJ3CompleteReceiptFitsAndBatchAdapts`; `TestP3_39_OverLimitCollectionWritesNothing` (`internal/lifecycle/p342b_p3_39_test.go`, both stores — an over-`MaxReceiptBytes` batch that can still shrink stores exactly the single-decision SKIP disclosure and archives nothing; a bound nothing fits under fails closed with `ErrResourceLimit` and no receipt) |
| P3-39 | SQLite EXPLAIN searches full filter/order keys | `TestSemanticReadsUseIndex` (`internal/store/sqlite/semantic_plan_test.go` — EXPLAIN constrains every key column of each facet read) |
| P3-39 | memory work instrumentation excludes whole-session scans | `TestSemanticReadsNeverScan` (`internal/store/memory/semantic_bounded_test.go`); `TestKeyedReadsDoNotScan` (the older keyed reads) |
| P3-39 | stable results independent of wall clock/counter | `TestP3_39_StableResultsIndependentOfClockAndCounter` (`internal/lifecycle/p342_p3_39_test.go`) |
| P3-40 | old-binary database with parsed Resolve and unbound claim→upgrade→close/reopen→retry identical | `TestPhase2FixtureUpgrade`; `TestPhase2FixtureReplay` |
| P3-40 | zero extra turn/seq/transition | `TestPhase2FixtureReplay` (same test) |
| P3-40 | added/changed v3 field under old ID conflicts | `TestV3HashOrdersOperationsAndConflictsWithV2Identity` |
| P3-40 | old envelope integrity | `TestUpgradePhase3RowFields` (`internal/store/sqlite/upgrade_test.go` — legacy envelopes verify unchanged and a bare event is marked unknown so it can never authorize replay); `TestPhase2FixtureUpgrade`'s `checkReplay` (`internal/store/sqlite/phase2_upgrade_test.go`, re-hash under `ingest-payload/v2`) |
| P3-40 | tighter new limits exact retry | `TestRetryAfterLimitsChange_F3` |
| P3-40 | principal/shape oversize attack stays bounded | `TestKnownEventIDCannotSmuggleOversizePayload_SEC31` |
| P3-40 | invalid UTF-8/nil/empty round-trip | `TestUpgradeLosslessStringLists` |
| P3-41 | old-schema fixtures plus new-schema restart on both stores | `TestUpgradeCommandDetailAndLookupItemIndexes` (representative; each migration's own `TestUpgrade*` covers the rest); `TestP3_41_LegacyUpgradeSurvivesRestartsAndAcceptsPhase3Writes` (`internal/store/sqlite/p342b_p3_41_test.go` — SQLite-only by nature, the memory store having no persisted schema to upgrade: a legacy file upgraded and reopened twice keeps its rows, applies its migrations once, and still accepts a fresh Phase 3 semantic write that reads back) |
| P3-41 | SQLite interrupted migration/checksum/Go-step pinning | `TestInterruptedMigrationReplays`; `TestCommittedMigrationsUnchanged`; `TestMigrationChecksumCoversStep` |
| P3-41 | schema↔Go field parity and guard coverage | `TestMigratedSchemaMatchesTypes`; `TestP3_41_UpgradePathSchemaParityAndPoisonGuard` (`internal/store/sqlite/p342b_p3_41_test.go` — column parity re-verified over a database that reached the current schema through the upgrade path, plus the poison guard's durability half across a reopen; the both-stores poison matrix is storetest/poison.go's) |
| P3-41 | dangling/wrong-session/version/proof/coverage refs | `TestConformance/SemanticProofReferences` (storetest, both stores — a proof bundle with any dangling reference is rejected as a whole) |
| P3-41 | no default-generated grants/leases/membership | `TestP3_41_NoDefaultGeneratedGrantsLeasesMembership` (`internal/store/sqlite/p342_p3_41_test.go`) |
| P3-41 | restored status cache agrees with history | `TestUpgradeSubjectHighWater` |
| P3-41 | upgrade reconciliation preserves old evidence and cannot claim a new positive proof | `TestUpgradeReconcilesLegacyMatcherSatisfaction`; `TestInterruptedReconciliationRollsBack` |
| P3-42 | every P3 decision maps to named tests / gate evidence | this table |
| P3-42 | amendments name actual final implemented semantics | ADR 8's own "SDD amendment" sections |
| P3-42 | trace assertions distinguish state from transmitted requests | `TestGateT02_ReplacementRetiresOldRequirement` et al. (Gate evidence table, T02/T06/T07 rows) |
| P3-42 | no unresolved C or missing schema field is labeled Accepted | this ADR's own Status line |

**Method note:** rows marked with an adjacent-test caveat ("adjacent," "partial," "not independently confirmed") point at a real test that covers most, but not quite all, of the clause's exact scenario; a fresh reviewer should not assume the adjacent test proves the narrower claim word-for-word. **Update (commander ruling M3, round 4):** this table originally found 52 genuinely MISSING clauses beyond the 21 the "Outstanding required tests" section below had already tracked (that section's 21 were always a subset, scoped to bullets a prior round had already flagged, not every clause in phase3-decisions.md). M3 ruled none of the 52 deferred; two GLM test writers wrote all of them as `p342_p3<N>_test.go` files, and every MISSING row above has been flipped to that real citation. **(SPEC-5.1, round 5) a full remap pass then re-examined all 51 rows whose citation was a wrong, partial, or adjacent test and moved each to a better existing test, read and confirmed to assert its clause before citing; memory-only coverage is labeled in-row where it applies. Two clauses are MISSING as of this pass, both recorded in-row: P3-20 "no filesystem/network reads during replay" (the package-boundary guarantee is weaker than a direct assertion and does not count) and the open-exchange half of P3-38 "open exchange and leased historical content survive" (the lease half is covered by `TestLeaseTakenAfterFirstBatchProtects_SEC42`).** **(Round-5 t3 pass, same series) the open-exchange half of P3-38 is now covered — `TestP3_38_OpenExchangeMemberSurvivesCollectUntilClosed` (`internal/lifecycle/p342b_p3_38_test.go`, both stores) — leaving P3-20's filesystem/network-read clause the table's only MISSING row.** **(Round-5 t2 pass, same series) that last clause is now covered too — `TestP3_20_ReplayPackagesImportNoFilesystemOrNetwork` and `TestP3_20_ReplayUsesStoredBytesNotTheLocator` (`internal/ingest/p342b_p3_20_test.go`) — and the same pass adds direct assertions for the P3-16 through P3-25 rows above (29 tests across `internal/{obligation,tools,ingest,graph}`, every body read and grep-verified before citing; single-layer mutation survivors are recorded in-row as layered defense-in-depth, each naming its second layer). The table now has no MISSING row.**

**SPEC-5.11 (round 5, P3-42 test hygiene of the `p342_*` files cited
above) — fixed in six commits on the t3 test pass, merged in the round-5
integration (head `3eb4aca`).** `80a3a6e` turns the unpublished-facet
gates of the P3-12/14/15/21 tests from `t.Skip` into `t.Fatal` and gives
P3-36's ingest fixtures a local `requireObligations` failure helper in
place of the shared skip — the obligation facet is published on both
backends, so a missing record is a regression, not pending work
(mutation-checked against a stubbed `ExactObligation`). `77f3c5e` adds
nil guards to P3-24/P3-26's replay checks so a mismatched outcome fails
the test instead of panicking past the SQLite subtests. `68ec6f2` has
P3-20 assert a CURRENT_PATH claim is refused while resource freshness is
UNKNOWN — even one naming the cached path state's own recorded revision.
`a038d96` derives P3-10's TTL check from the item's own
`CreatedTurn`/`TTLTurns` instead of fixture constants. `d4718ca` makes
P3-33 assert an explicit PROTECTED decision exists for the unpinned
source, not merely that no non-protected one appears. `fe63651` replaces
P3-31/P3-39's millisecond sleeps with a statically pinned instant — the
decision path consults no wall clock, and time-valued fields were
already swept by the ambient table.

**SPEC-5.3 (round 5, Medium — a P3-42 citation was not actually narrower)
— fixed in `fab04d6` on the t2 test pass, merged in the round-5
integration (head `3eb4aca`).** `TestP3_25_NarrowerCitationDoesNotChangeKeyBoundary`
(`internal/tools/p342_p3_25_test.go`) cited an occurrence whose access was
exactly the keyed item's frozen boundary (`conversationBoundary(i.Principal)`),
so the citation was not narrower and a keyed write that took its boundary
from the citation (`internal/tools/keyed.go:62`) survived the test's
mutation check. The fix seeds cited evidence whose access differs from the
frozen boundary in both directions — one TURN-scoped occurrence inside the
conjunction, one task-wide occurrence without the agent constraint —
asserts that difference as a precondition (the narrow one is a distinct
boundary `Within` the frozen one; the broad one permits a teammate the
frozen conjunction excludes), and keeps asserting the filing lands on the
frozen boundary with its closed identity fields and the citation recorded
as qualifying support; taking the boundary from either citation now fails
the test on both stores.

## Outstanding required tests (SPEC-1.23, SPEC-2.14, SPEC-3.9, SPEC-4.9) — closed, PR #6 round 4

**All bullets this section tracked are now resolved, including the last
one: the P3-20 "no filesystem/network reads during replay" clause, MISSING
since the round-5 SPEC-5.1 remap, is now covered by the round-5 t2 test
pass (`internal/ingest/p342b_p3_20_test.go` — see the P3-42 table's P3-20
row above).** PR #6 review round 1
(`r6-spec1.md`, SPEC-1.23) searched every package and found no real
counterpart for 24 required-test bullets across P3-1 through P3-38 (21
after three gained a test as a side effect of round-1 code fixes, per
SPEC-2.14). Rounds 2-4 (`r6-spec2.md`, `r6-spec3.md`, `r6-spec4.md`)
reconfirmed the 21 unchanged each time. The SPEC-4.9 pass above expanded
this to a full P3-1..P3-42 clause-level table and found 52 genuinely
missing clauses total (superseding the narrower 21, which were always a
subset). **Commander ruling M3 (round 4): none of the 52 is deferred; two
GLM test writers wrote all of them in PR #6, test-only, as
`p342_p3<N>_test.go` files.** This ADR's own P3-42 table above now cites
every one of them by name; the specific 21 this section originally
enumerated are covered as follows:

- **P3-1** "Prepare/MarkSent stale after every new semantic record family"
  — `TestP3_1_PrepareMarkSentStaleAfterEverySemanticRecordFamily`
  (`internal/invocation/p342_p3_1_test.go`).
- **P3-2** "failed attempt then valid retry" — `TestP3_2_FailedAttemptThenValidRetry`
  (`internal/lifecycle/p342_p3_2_test.go`).
- **P3-3** "same textual key in all three namespaces" — `TestP3_3_SameTextualKeyInAllThreeNamespaces`
  (`internal/graph/p342_p3_3_test.go`).
- **P3-7** "B's task-visible transcript is not A's membership" — `TestP3_7_TaskVisibleTranscriptIsNotMembership`
  (`internal/graph/p342_p3_7_test.go`).
- **P3-15** "mode-conflicting retry" and "migration without invented
  exemption/proof" — `TestP3_15_ModeConflictingRetry`,
  `TestP3_15_MigrationInventsNoExemptionOrProof` (`internal/obligation/p342_p3_15_test.go`).
- **P3-20** "uncertain aliases invalidate conservatively" — `TestP3_20_UncertainAliasesInvalidateConservatively`
  (`internal/obligation/p342_p320_test.go`); "no filesystem/network reads
  during replay" — MISSING since the SPEC-5.1 remap — is now covered by
  `TestP3_20_ReplayPackagesImportNoFilesystemOrNetwork` and
  `TestP3_20_ReplayUsesStoredBytesNotTheLocator`
  (`internal/ingest/p342b_p3_20_test.go`, both stores — see the P3-42
  table's P3-20 row above).
- **P3-24** "same invocation with a different method/principal conflicts"
  — `TestP3_24_SameInvocationDifferentToolPrincipalOrArgsConflicts`
  (`internal/tools/p342_p3_24_test.go`).
- **P3-27** "cross-turn semantic summary versus raw leased copy" —
  `TestP3_27_CrossTurnSemanticSummaryVersusRawLeasedCopy` (`internal/tools/p342_p3_27_test.go`).
- **P3-29** "source usage update does not expire [a lease]" — `TestP3_29_SourceUsageUpdateDoesNotExpire`
  (`internal/retrieve/p342_p3_29_test.go`).
- **P3-34** "malformed operation rollback includes turns" and "semantic
  writes invalidate unsent previews" — `TestP3_34_MalformedOperationRollbackIncludesTurns`,
  `TestP3_34_SemanticWritesInvalidateUnsentPreviews` (`internal/ingest/p342_p3_34_test.go`).
- **P3-35** "mismatch/ambiguity cause boundaries" — `TestP3_35_MismatchAndAmbiguityCauseBoundaries`
  (`internal/ingest/p342_p3_35_test.go`).
- **P3-36** "after further mutations/restart" and "checkpoint never
  retires a requirement" — `TestP336_HistoryReconstructibleAfterFurtherMutationsAndRestart`,
  `TestP336_CheckpointNeverRetiresRequirementBySourceCoverage` (`internal/ingest/p342_p336_test.go`).
- **P3-38** "expired lease releases only lease protection," "superseded
  SYSTEM instruction collectible with proper actor," and "no
  `LastUsedCall==0` heuristic" — `TestP3_38_ExpiredLeaseReleasesOnlyLeaseProtection`,
  `TestP3_38_SupersededSystemInstructionCollectibleWithProperActor`,
  `TestP3_38_NoLastUsedCallZeroHeuristic` (`internal/lifecycle/p342_p3_38_test.go`).
- **P3-10** Promote/Demote "expired origin unchanged" and a CAS conflict —
  `TestP3_10_ExpiredOriginUnchangedByPromotion`, `TestP3_10_PromoteReplayAndCASConflict`
  (`internal/lifecycle/p342_p3_10_test.go`).

The three originally "Resolved" bullets (P3-1 TargetCall sequence reuse,
P3-3 old-key migration, P3-4 legacy unknown declaration fails closed) are
unaffected by this pass and keep their existing citations, still real:
`TestConformance/SemanticLedgerSeqIsolation`/`TestPhase3RowsCarrySemanticSeq`;
`TestAgentUpdatesOwnPreUpgradeKey`/`TestUpgradeAgentOwnOldKey_G5`;
`TestIdenticalRestatementOfUnknownIdentityFailsClosed`/`TestUnknownIdentityRestatementIsALineDiagnostic`.

**P3-42's own gate requirement — "every P3 decision maps to named
required tests" — is met as of this pass: the table marks no clause
MISSING (the open-exchange half of P3-38 is covered by
`TestP3_38_OpenExchangeMemberSurvivesCollectUntilClosed`; P3-20's
filesystem/network-read guarantee by the round-5 t2 pass's two
`internal/ingest` tests)**, plus the
small number of memory-only and
adjacent-test caveats the P3-42 table above records inline. Moving
this ADR's Status line from Proposed to Accepted is the commander's call,
not this docs pass's; flagged for that ruling separately.

## Residual risks and limits

- **Accepted harness-trust limit.** An authorized but compromised resource
  reporter may forge a run outcome or a content hash; the runtime checks
  bindings, grants, applicability, and order, not truth. Correctness of an
  unreported external change remains excluded by FR-OBL-005 as already
  written. A resource update this runtime has itself rejected (§9) must
  never be treated by a caller as successfully reported; the harness is
  responsible for retrying a rejected report rather than dispatching against
  a known-unreported change.
- **Expired TURN evidence (recorded at reconciliation, §11's T07 ruling).** A
  proof resting on TURN-scoped evidence stays valid after that evidence's own
  turn expires: the proof is an immutable record of an applicable run, and
  resource changes (§13) invalidate it, not evidence lifetime. This is an
  accepted consequence of separating evidence boundary from derived-state
  boundary (§11), not an oversight.
- This ADR adds no live provider/request correctness claim and asserts no
  new evidence about resource-reporting reliability beyond what FR-OBL-005
  already accepted; §9 through §13 make that acceptance's boundaries
  precise and auditable, they do not narrow the underlying trust assumption.

## Open questions

### Resolved by this ADR

- ADR 16 left open "whether `granteeMatches`'s exact-ID matching is
  expressive enough once Phase 3 needs grants scoped to 'any task the
  grantee currently owns,'" deferring it to this ADR.
  **Decision:** V1 keeps exact-ID/exact-version grantee and target matching
  (§1); no task-scoped or wildcard grant form is introduced. `domain.GrantIntent`
  requires an exact `[]GrantTarget` set with no wildcard shape.

### Deferred

- Whether the TOOL-authority derived-state rule (§11/C-6) should ever admit a
  distinct HARNESS-authority attestation path with its own provenance
  semantics, versus always requiring a separate explicit assertion (§5).
  Unchanged at reconciliation: V1 requires the separate explicit assertion,
  and no dedicated HARNESS-attestation type exists in the landed code.
- Whether the restricted cause-based invalidation path (§13) should ever be
  reused for a non-resource cause (e.g., an authority downgrade or a
  session-level policy change). Unchanged: `domain.TransitionCause`'s
  registry has no such cause, and any future one requires its own ADR
  amendment.
