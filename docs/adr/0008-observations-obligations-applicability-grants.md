# 8. Observation identities, obligation matcher/claim versions, applicability fingerprints, mutation grants, and invalidation rules

Status: Proposed (2026-09-26, drafted for Phase 3; reconciled through PR #6 review
round 2, head `914afef`. `go test -race -count=1 ./...` passes with no exceptions at
every reconciled head so far; every decision below cites real, `grep`-verified code
and tests, not a proposed contract. **P3-42's required-test mapping is still
incomplete** (see "Outstanding required tests" below — SPEC-1.23/SPEC-2.14) **and
G1's applicability rule is still under active fix as of this pass** (SPEC-2.1/DUR-2.1,
§12 below): this ADR remains Proposed for both reasons, not merely pending a
formality)
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

- **Cross-boundary rejection (§W4-18):** a newer complete FAIL may reject a
  task-wide matcher proof even when the failing run's own evidence is
  narrower — rejection only ever moves a version towards UNRESOLVED, and its
  details are redacted.
- **What rejection touches (§W4-19, corrected — SPEC-1.10/SPEC-2.5).**
  W4's original implementation list narrowed this to "a resource-bound
  assertion proof is never rejected by a FAIL," which silently departed from
  the frozen P3-16 text with no commander ruling; PR #6 round 1 (SPEC-1.10)
  withdrew that narrowing as a code fix, and this ADR's text is corrected to
  match. A newer complete applicable FAIL rejects the subject's **current
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
**Residual (SPEC-2.8, assigned W2, still open):** this check exists only at
the service layer; `InsertObservation` in both store backends does not
independently verify `ev.Source.ToolCallID == run.ExecutionID`, so P3-21's
"validated by service and store" is only half true as of this pass.

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

### 13. Invalidation is atomic and narrowly scoped (§P3-23, C-10, §W4-17)

`obligation.invalidateResource`/`invalidateProof` (`internal/obligation/invalidate.go`,
`evaluate.go`) implement the restricted, paged, work-bounded invalidation
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

**A changed directory intersects files under it (PR #6 round 1, SPEC-1.18).**
`change.affects` (`internal/obligation/invalidate.go:39,50`) originally
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
- **A task-less trigger produces no GC request; Phase 3 has no
  session-scoped GC (PR #6 round 2 systemic ruling H4; SPEC-2.2/DUR-2.5).**
  A directive's explicit replacement (ADR 19's Q1/C-1 amendment) may itself
  be session-scoped (`TaskID == ""`); before this ruling, its SUPERSESSION
  side effect tried to build a `CollectTask` request with an empty
  `TaskID`, which `domain.CollectIntent.Validate` rejects — so the
  *replacement itself* failed even though the replacement's own
  authorization had nothing to do with GC. H4 resolves this at the root:
  `gcqueue.Enqueue(tx, pol, origin, trigger, taskID, triggerID)`
  (`internal/gcqueue`, imported by `internal/ingest`, `internal/tools`, and
  `internal/obligation` — everything below `internal/lifecycle` that
  produces a trigger) persists nothing at all for a task-less trigger and
  returns success; it never falls back to a session-scoped `CollectScope`,
  because Phase 3 defines none. The replacement (or keyed write, or
  observation supersession) that raised the trigger always succeeds on its
  own merits — GC producing nothing is never a reason a mutation fails.
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
  invalidation path (§13).** Rejected as part of C-10: `invalidateResource`/
  `invalidateProof` run as an internal, runtime-only consequence, never
  through the public grant-authorized mutation path.

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

## Outstanding required tests (SPEC-1.23, SPEC-2.14)

P3-42 requires every decision above to map to its named required tests.
PR #6 review round 1 (`r6-spec1.md`, SPEC-1.23) searched every package and
found no real counterpart for the required-test bullets below. Round 2
(`r6-spec2.md`, SPEC-2.14) reconfirmed the gap is still open, noting that
exactly three bullets have since gained a real test as a side effect of
their round-1 code fix landing:

- **Resolved.** **P3-1** "TargetCall sequence reuse rejected" for Phase 3
  record families — `internal/store/storetest`'s
  `TestConformance/SemanticLedgerSeqIsolation` and
  `internal/store/sqlite`'s `TestPhase3RowsCarrySemanticSeq` (SPEC-1.4:
  the three SQLite row types now implement `SemanticSeq()`).
- **Resolved.** **P3-3** "old-key migration" (an agent updates its own
  pre-upgrade key) — `internal/graph`'s `TestAgentUpdatesOwnPreUpgradeKey`
  and `internal/ingest`'s `TestUpgradeAgentOwnOldKey_G5` (SPEC-1.5).
- **Resolved.** **P3-4** "legacy unknown declaration fails closed" on the
  ingest path — `internal/graph`'s
  `TestIdenticalRestatementOfUnknownIdentityFailsClosed` and
  `internal/ingest`'s `TestUnknownIdentityRestatementIsALineDiagnostic`
  (SPEC-1.3; migration 0034 additionally reconciles a *known* declaration
  where identity is establishable, per ADR 3's amendment above — the
  "fails closed" case these tests cover is now the narrower unknown-only
  case, not every legacy item).

The remaining bullets are still open, not owned by this ADR's own package
(`internal/obligation`) unless marked, and P3-42's mapping therefore remains
unmet — this ADR stays Proposed for this reason among others (Status header
above).

- **P3-1** "Prepare/MarkSent stale after every new semantic record family" —
  still only the Phase 2 `TestObligationChangeStalesPreview`; no Phase
  3-record-family case exists (`internal/invocation`).
- **P3-2** "failed attempt then valid retry" at the Phase 3 service level —
  no test found in `internal/obligation`, `internal/tools`, or
  `internal/lifecycle`.
- **P3-3** "same textual key in all three namespaces" — OBSERVATION is
  missing from the existing DIRECTIVE/AGENT_KEY case (`internal/domain`).
- **P3-7** "B's task-visible transcript is not A's membership" — no test
  found in `internal/graph`'s membership package.
- **P3-15** "mode-conflicting retry" and "migration without invented
  exemption/proof" — no test found in `internal/obligation`.
- **P3-20** "no filesystem/network reads during replay" (only
  `imports_test.go`'s package-import restriction exists, which is a weaker
  guarantee) and "uncertain aliases invalidate conservatively" — no test
  found in `internal/obligation`.
- **P3-24** "same invocation with a different method/principal conflicts"
  — no test found in `internal/tools`.
- **P3-27** "cross-turn semantic summary versus raw leased copy" (the
  `LeaseID` skip at `internal/tools/checkpoint.go`) — untested.
- **P3-29** "source usage update does not expire [a lease]" — no test
  found in `internal/retrieve` or `internal/policy`.
- **P3-34** "malformed operation rollback includes turns" (existing
  `TestOps_MissingHandlerFailsClosed` uses a SYSTEM event, which opens no
  turn) and "semantic writes invalidate unsent previews" — no test found
  in `internal/ingest`.
- **P3-35** "mismatch/ambiguity cause boundaries" — no `DetailAccess`
  assertion on a MISMATCH/AMBIGUOUS outcome specifically.
- **P3-36** "after further mutations/restart" (`TestP336_ReplacementHistoryReconstructible`
  never reopens the store) and "checkpoint never retires a requirement"
  (`gate_t16_test.go` checks facts only, not a requirement) — both in
  `internal/ingest`.
- **P3-38** "expired lease releases only lease protection," "superseded
  SYSTEM instruction collectible with proper actor" (pure matrix only, no
  service-level case), and "no `LastUsedCall==0` heuristic" — no test found
  in `internal/policy` or `internal/lifecycle` beyond the pure decision
  matrix (`TestCollectDecisionMatrix`).
- **P3-10** Promote/Demote "expired origin unchanged" and a CAS conflict —
  no test found in `internal/lifecycle`.

This list is not this ADR's package's obligation to close by itself; it is
recorded so the Phase 3 gate's own claim of completeness is accurate. SPEC-2.14
counts 21 required-test bullets still open across the list above (the three
resolved bullets moved out of the count). Adding each test, or recording an
explicit ruling that a bullet is satisfied another way, closes this section.

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
