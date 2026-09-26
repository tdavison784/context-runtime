# 8. Observation identities, obligation matcher/claim versions, applicability fingerprints, mutation grants, and invalidation rules

Status: Proposed (2026-09-26, drafted for Phase 3; SDD §11 item 3 gates Phase 3 exit on this
ADR reaching Accepted with green gate evidence)
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

None of this is implemented yet: `internal/obligation` (matcher registry,
claim binding, proof/applicability, resource reporting) does not exist as of
this ADR's drafting, nor do `internal/lifecycle`'s completion/GC pieces that
depend on it. This ADR is Phase 3's binding implementation contract for those
packages, not a description of landed code — every "Schema/API" and "Tests"
item below is a required addition, and every code citation is a package/file
this ADR requires to exist, not one that exists today.

**Binding source.** This ADR records the disposition already reached in the
commander's binding Phase 3 decision record
(`.worktrees/_commander/phase3-decisions.md`, P3-1 through P3-42, all
conflicts C-1 through C-21 ADOPTED by commander ruling FROZEN 2026-09-26).
This ADR restates, for the obligation/observation/grant subsystem specifically
(P3-5, P3-12 through P3-23), the decision, its rejected alternative, and the
compatibility/test consequences, in this repository's ADR format; it does not
reopen anything the freeze already settled. Section references below (`§P3-n`)
are to that record.

## Decision

### 1. Typed, exact-version grant targets (§P3-5)

A `MutationGrant`'s target is a discriminated `GrantTarget{Kind,
SessionID, ItemID | ObligationID+Version, AuthorizationKey}`, not a bare
string ID. `ITEM_OCCURRENCE` targets name an immutable item occurrence
exactly as ADR 16 already does; `OBLIGATION_VERSION` targets name
`(SessionID, ObligationID, Version)` — never the obligation's stable ID alone
and never its mutable `Revision` (ADR 16's CAS field). A grant issued against
obligation version N authorizes a transition on version N only; it confers
nothing on version N+1 created by a later replacement (§7 below), even though
both share the same stable obligation ID. Matcher grants continue to be
restricted to `ActionAssertObligation` only (ADR 16, unchanged, now enforced
against the versioned target). Retired target versions remain resolvable for
revocation and audit lookups but never for a new status transition. Legacy
Phase 1/2 grants recorded against a stable obligation ID (none exist in
practice, since Phase 2 sets no matcher version and grants nothing per ADR
19 §9/D13) are treated as inert on upgrade — a stable-ID grant never attaches
itself to "the latest version" automatically; it must be reissued against an
exact version to authorize anything.

### 2. Deterministic claim/matcher registration and target binding (§P3-12)

`tests_pass/1` and `file_read/1` are the only two registered matchers,
compiled into policy/code — no dynamic plugin loading. They match only
against a new, nonduplicate Pinned item's whole extracted ASCII text
(stripping at most one trailing sentence period): `All tests must pass`, or
`Read <path>` with a nonempty, non-whitespace path. An explicit
`obligation=<claim>` attribute always wins over claim-pattern matching and
creates at most one Pinned declaration slot per item. Each `ObligationVersion`
gains an immutable `TargetSpec`, `ClaimPatternVersion`, `DeclarationSlot`, a
`WorkspaceBindingRef` (§10 below), and a binding state
(`bound`/`unresolved`) with a fixed diagnostic reason — never a guessed
repository, suite, or working directory. `tests_pass` requires a trusted
declared suite, complete coverage specification, resource/worktree,
working directory, and environment specification, resolved only from a
trusted recorded workspace default or an explicit typed target; `file_read`
requires a normalized resource-relative path plus `FIXED_HASH` (a specific
immutable content snapshot) or `CURRENT_CONTENT` (the authoritative current
path content, going stale on any content change) mode. An obligation whose
target cannot be resolved is a syntactically known but non-executable
matcher — evaluation is skipped, not guessed. Replacement (§7 below) never
retroactively binds a Phase 2 unbound claim; it starts a fresh version with
no inherited grant or proof.

### 3. Transition intents and the obligation state machine (§P3-13)

A public `TransitionIntent{TargetObligationID, TargetVersion,
ExpectedRevision, RequestedAction, EvidenceRefs, ApplicabilityIntent,
RequestID}` is distinct from the stored `ObligationTransition` record (ADR
16). Actor, sequence, `Action`, and `Matcher`/`GrantID` attribution remain
service-derived, never caller-supplied, continuing ADR 16's
`TransitionAction` binding. The legal edge set is unchanged from ADR 16
(UNRESOLVED→SATISFIED; SATISFIED→UNRESOLVED; UNRESOLVED↔BLOCKED; any
non-WAIVED status→WAIVED); WAIVED and retired versions never transition
again, and AGENT/TOOL/RETRIEVED_CONTENT never drive one (FR-OBL-002,
unchanged). Evidence references must exist by commit time, match session and
exact occurrence, be accessible to the actor, and satisfy §4's publication
rule. A positive matcher transition additionally requires, in the same
transaction: a live exact-version grant (§1), a bound matcher (§2), and a
validated nonempty applicability proof current as of commit (§4). History is
appended and the current status/proof cache updated under the same CAS
(`AppendObligationTransition(tx, t, expectedRevision)`, ADR 16) atomically.
Reason codes are closed; optional rationale text is bounded, access-filtered
audit content, never disclosed to a principal without access to the target.

### 4. Proof visibility and the derived SATISFIES view (§P3-14)

An `ApplicabilityProof` is an immutable record naming the exact obligation
version, `TargetSpec`, matcher/rule, evidence occurrence(s), resource
version/dependency references, the satisfying transition, and an explicit
access boundary. Publishing resource-derived satisfaction at an obligation's
boundary requires that boundary be `Within` (ADR 6) *every* evidence and
applicability source's boundary — actor access to create the transition is
not sufficient by itself; an overbroad proof (task-wide obligation resting on
agent-private evidence) is rejected outright. A separately authorized
assertion can never relabel private evidence as having public provenance.
FR-REL-001 is amended (§below) so that SATISFIES is a **typed derived view**
— `SatisfiesRelation{EvidenceOccurrenceID, ObligationID, Version,
TransitionID, ProofID}` computed from transition/proof records — never a
separately persisted item-to-item `Relationship` row; `graph.Relationship`
keeps only item-to-item `DERIVED_FROM`/`SUPERSEDES`/`DEPENDS_ON`/
`REFERENCES`/`DUPLICATE_OF` edges (Q2, adopted; C-1's own text is separate,
§7). The **current** SATISFIES view includes only the proof behind a current
SATISFIED version; the **historical** view retains later
invalidation/retirement for audit. A bare attestation assertion (§5) creates
an assertion record, never a fabricated evidence edge. Every read redacts
inaccessible proof IDs, resource hashes, and paths, and truncates
inaccessible provenance without revealing counts — repeating ADR 16's
graph-layer non-disclosure discipline at this layer.

### 5. Assertion modes: ATTESTATION vs RESOURCE_BOUND (§P3-15, C-14)

Every authorized satisfaction explicitly declares `AssertionMode` = `ATTESTATION`
or `RESOURCE_BOUND` at the moment of assertion; there is no mode inferred
from whether the request happens to carry a citation. `ATTESTATION` rests on
the asserting authority alone: optional accessible citations are supporting
audit provenance only, never a silently created resource dependency, and
remain subject to authorized revalidation/waiver/retirement but never to
automatic resource invalidation (§6). `RESOURCE_BOUND` declares validated
target/dependency/applicability data and is invalidated under §13's rule.
Missing evidence is never treated as an implicit third mode, and attaching a
citation with fingerprints after the fact cannot silently convert an
ATTESTATION into a RESOURCE_BOUND assertion. The request hash (ADR 4/19's
`CanonicalEncoder`) includes the mode. A legacy assertion (none exist before
Phase 3, since Phase 2 grants nothing) is recorded explicitly `LEGACY`/
unknown unless its original recorded intent proves a mode — the runtime never
manufactures freshness or strips a known dependency to make old data fit a
new field.

**Alternative considered and rejected (C-14).** Claude M10 proposed inferring
`Applicability = ASSERTED` for a bare assertion and treating a citation
carrying fingerprints as retroactively invalidatable like a matcher proof.
Codex's cross-check required an explicit mode chosen at assertion time.
**Trade-off:** inference avoids one field, but means adding or removing an
incidental citation silently changes whether a satisfaction survives a later
edit — the authorizing intent becomes unrecoverable from the stored record.
**Commander ruling (FROZEN 2026-09-26): ADOPT explicit mode** exactly as
Codex's cross-check proposed, with conservative reconciliation (not invented
exemption) for any unknown legacy intent.

### 6. Rejected proof and proof refresh use legal transitions (§P3-16)

A newer complete, applicable FAIL for the same declared subject invalidates
that subject's current matcher/resource-bound satisfaction even at an
*unchanged* workspace fingerprint (a flaky-suite regression, not just a
content change) — rejected-proof invalidation is one instance of §13's
restricted cause-based path, recording the rejecting observation as
`PROOF_REJECTED` cause; an expiring grant can never preserve an invalid
proof. Stale, partial, timeout, or unrelated results never invalidate merely
by later receipt (§12's run-order/completeness gate). A newer valid proof
replacing a still-valid SATISFIED proof is an atomic, audited
SATISFIED→UNRESOLVED→SATISFIED pair with cause `PROOF_REFRESH` and CAS at
each step — never a direct SATISFIED→SATISFIED write, which FR-OBL-002's
transition table does not permit and this ADR does not add an exception for.
The positive half of the pair is preauthorized at its actual mutation
sequence (ADR 8 §3/§1); if the new proof cannot itself be authorized, the
still-valid old proof is retained rather than left in a half-invalidated
state; if the old proof turns out invalid, the version is left UNRESOLVED.
Identical proof replay is a no-op. BLOCKED evidence still waits for an
authorized unblock; WAIVED remains terminal regardless of any later proof.

### 7. Matcher evaluation is runtime-controlled, with trusted reevaluation (§P3-17, C-4)

Only validated typed observations and recorded resource/target state ever
enter registered matcher code; no public transition or tool parameter may
nominate a matcher, a forged outcome, or arbitrary text as executable proof.
A positive evaluation always requires a live exact-version
`ActionAssertObligation` grant (§1) and commit-time applicability (§4).
Unknown historical matcher versions are never silently replaced by "latest."

**Alternative considered and rejected (C-4).** Claude E4 proposed the runtime
"runs [the matcher] when it ingests an observation envelope, and nowhere
else." Codex's cross-check (X11) proposed an additional trusted runtime
reevaluation operation on an exact obligation version, triggered by a new
matcher grant, an authorized unblock/revalidation, or a replacement — with no
caller-selected matcher, outcome, or evidence text, run in deterministic
observation order. **Trade-off:** observation-only execution has fewer entry
points, but strands valid evidence that arrived before a grant existed, or
before an unblock, forcing a duplicate observation merely to revalidate an
unchanged version. Trusted reevaluation adds a controlled retry surface —
never a model-selected proof executor. **Commander ruling (FROZEN
2026-09-26): ADOPT trusted exact-version reevaluation.** An optional
`ReevaluateIntent{RequestID, ObligationID, Version, ExpectedRevision}` runs
through a trusted control API only, selecting from existing recorded
observations in deterministic run order (§12); a new grant alone, with no
reevaluation call, never by itself satisfies anything. Reusing old proof for
a replacement version still requires a new version grant and an explicit
revalidation call — a replacement never inherits proof automatically (§6 of
ADR 19's dedup/replacement text, and §P3-4/C-1 below).

### 8. HARNESS obligation declarations and materialization exceptions (§P3-18)

FR-OBL-001's HARNESS declaration path (an obligation named without going
through a Pinned directive) is a trusted `DeclareObligationIntent` naming an
accessible existing or earlier-created-in-transaction source, a stable
declaration slot, description, target, and matcher request; it requires
direct equal-or-higher authority over the source or an explicit
source-action grant — merely naming a SYSTEM source is never sufficient.
`SetObligationMaterializationIntent` (the exception FR-OBL-003 already
requires) needs source/equal-or-higher policy authorization, expected
revision, and audit; it affects rendering only — never satisfaction, waiver,
protection, or completion checks (§P3-9/ADR 16 amendment below). No AGENT
tool exposes either mutation. `declare_obligation` and
`set_obligation_materialization` are new closed action values for trusted
principals only, never matcher grants.

### 9. Resource currentness has an authenticated, ordered source (§P3-19, C-9)

`ResourceState` is keyed `(session, resource/worktree identity)` with
immutable owner/reporter binding, an authoritative resource revision, a
workspace fingerprint, freshness `KNOWN`/`UNKNOWN`, and CAS/audit history.
Only an authorized SYSTEM/HARNESS reporter may initialize or update it, under
a request identity with an expected prior revision; duplicates replay and a
stale update can never roll the pointer back. A detected revision gap
commits `UNKNOWN` plus every necessary invalidation in the same transaction,
or rejects the whole update if that cannot complete atomically; only an
authoritative resync restores `KNOWN`.

**Alternative considered and rejected (C-9).** Claude E6 proposed that "when
no `ResourceState` exists yet for the namespace, the observation's
fingerprint establishes it" — first-observation initialization. Codex's
cross-check required that "only an authorized resource reporter may
initialize or update it. An observation alone never establishes currentness."
**Trade-off:** first-observation bootstrap needs no separate reporting
handshake, but lets a delayed run make an earlier workspace (W1) appear
current after an unrecorded later change (W2) — exactly the staleness bug
this obligation subsystem exists to prevent. **Commander ruling (FROZEN
2026-09-26): ADOPT authoritative baseline/resync.** `tests_pass` requires
complete target equality and a current workspace fingerprint (including
relevant uncommitted changes/configuration/dependencies); `file_read` follows
its declared fixed/current-content mode; a combined
initialize-then-evaluate event uses two explicit ordered authenticated
operations, never an implicit observation side effect.

### 10. Workspace binding and proof locator identity are explicit (§P3-20, C-5, C-11)

`resource-locator/v1` is a **new, separate** identity from ADR 19's legacy
`reference-locator/v1`: an authenticated opaque resource/worktree ID plus an
explicit repository-relative base directory and a normalized relative path,
lexically normalized (`/` separators, `.`/safe `..` removed, absolute paths
and control bytes/backslashes rejected), never dereferencing the filesystem
or a URL. An unknown symlink/alias mapping is conservative uncertainty, never
assumed equality. `WorkspaceBinding{ID, Version, ResourceID, SourceContext,
BaseDir, EnvironmentSpec, SuiteSpec, Access, Reporter, Seq}` is an immutable
version attached to source/task/conversation context — a session-wide
mutable "current workspace" is forbidden. Resource control (§9) runs
independently of task creation, turn advancement, or completion, so a
completed task can never suppress cross-task invalidation; the trusted
control actor still needs resource-reporting authority regardless of its
`TaskID`.

**Alternatives considered and rejected (C-5, C-11).** *C-5:* Claude E4
proposed reusing "the versioned ADR 19 §16 locator-identity rule." Codex's
cross-check (E4/M8) required a distinct rule: "a harness-authenticated opaque
resource/worktree ID plus canonical repository-relative path and explicit
base directory." **Trade-off:** reusing References identity looks smaller,
but ADR 19 §16/23 (R19/SPEC-2.2) explicitly records that the accepted
locator rule has *no* repository or base-directory component — silently
changing it would reinterpret existing references and frozen migration
steps. **Ruling: ADOPT the separate `resource-locator/v1`;** legacy
References identity is unchanged. *C-11:* Claude M8 proposed restricting
`Workspace` declarations, like resource-change reports, to session-level
(empty-`TaskID`) SYSTEM/HARNESS events. Codex's cross-check distinguished
resource *control* (session-level, per C-9) from workspace *bindings*
(immutable, task/conversation-scoped, may appear on a task-bound trusted
event). **Trade-off:** one restriction is simpler but produces either a
single ambiguous mutable session workspace or forbids binding a task's
resource/suite/environment context at all. **Ruling: ADOPT separating
resource control from workspace bindings**, exactly as above; an empty
`TaskID` is neither universal read access nor resource-reporting capability
by itself.

### 11. Typed observations preserve evidence and reporter provenance (§P3-21, C-6, C-7)

The six authority-typed `EventKind`s are unchanged; only SYSTEM/HARNESS
events may carry a bounded typed observation operation, and lower authority
can never create one. The typed envelope — family/schema, declared subject,
resource/workspace binding, working directory, opaque environment
fingerprint/spec, observed fingerprint, declared coverage plus completeness,
outcome/counts, reporting matcher version, authenticated reporter, and
immutable execution/run identity — is persisted independently of any
rendered text, and is bound to an exact TOOL evidence occurrence created from
its referenced span or an earlier authenticated operation, validated for
same session/execution/boundary. Environment values and raw tool-output
bytes never become trusted template content.

**Alternatives considered and rejected (C-6, C-7).** *C-6:* Claude E5's
rationale called the derived state item "HARNESS or SYSTEM authority,"
elsewhere "the event's span authority capped at HARNESS," leaving which span
supplies authority ambiguous. Codex's cross-check required "render a
TOOL-authority `task_state` from validated typed fields only," with any
stronger status requiring a separate explicit harness assertion.
**Trade-off:** HARNESS-authority state reads as a trusted attestation, but
conflates reporting trust with evidence authority and affects what
supersedes what. **Ruling: ADOPT TOOL-authority derived state** (also
governs §12's `obs-state/1` rule); a stronger claim must be a separate,
explicitly authorized assertion (§5), never implicit in observation
ingestion. *C-7:* Claude E5 proposed that "envelopes whose TOOL span is
missing[ create] evidence only: no state and no supersession" — best-effort
acceptance. Codex's cross-check required rejecting a missing or mismatched
evidence reference atomically as a malformed payload. **Trade-off:**
best-effort acceptance avoids losing an unrelated span but leaves an
observation without the immutable evidence occurrence this contract
requires, silently weakening provenance. **Ruling: ADOPT atomic rejection of
malformed envelopes;** a validly bound `ERROR`/`TIMEOUT`/`CANCELLED`/partial
result remains legitimate evidence-only history, a distinct case from a
missing/mismatched reference.

### 12. Subject comparability, run order, and state authority (§P3-22, C-8)

`SubjectKey/v1` hashes semantic observation family, declared suite/path,
coverage specification, resource/worktree, normalized working directory, and
environment specification — excluding outcome, observed fingerprint, matcher
implementation version, receipt order, and audit time, so a matcher upgrade
never splits an otherwise-identical subject's identity. State is indexed by
subject plus task and exact boundary. A trusted monotonic `ObservationRun`
ordinal is assigned **before execution**, and an accepted per-subject
watermark is held under CAS.

**Alternative considered and rejected (C-8).** Claude E5 proposed that "for a
PASS or FAIL outcome only, the registered rule `obs-state/1` derives a
`task_state` item," with identity including coverage
(`COMPLETE`/`PARTIAL`). Codex's cross-check required "only a terminal
complete observation of the current applicable resource state, newer than
the accepted subject watermark, may derive a replacement current-state
item." **Trade-off:** outcome-only gating permits a `PARTIAL` PASS/FAIL, or a
run that arrives late, to replace newer truth merely by matching outcome.
Ordinal- and coverage-gated replacement adds run bookkeeping the runtime must
persist, but is the only rule under which "the current answer" cannot regress
from a delayed or partial result. **Ruling: ADOPT complete/current/ordered
gating with a persisted pre-execution run ordinal** — receipt order at
ingestion time is never an acceptable substitute for that ordinal, since a
delayed report's receipt order says nothing about when the run actually
executed. Partial output, timeouts, errors, cancellations, and out-of-order
runs (including a run with an identical resulting fingerprint to one already
accepted) remain immutable evidence only, never a replacement of current
state. A deliberately declared subset suite is its own complete subject; a
`PARTIAL` run of the full suite does not retroactively become that subset's
complete result. Higher-authority assertions (§5) remain a separate,
explicitly authorized operation, distinct from this rule's TOOL-authority
`obs-state/1` supersession (§11/C-6).

### 13. Invalidation is atomic and narrowly scoped (§P3-23, C-10)

Every accepted resource update invalidates all intersecting current
resource-bound SATISFIED proofs across the session in the same transaction,
before any later snapshot can observe new resource state paired with old
satisfaction. An unknown path, unknown change coverage, or an `ALL` report
invalidates every potentially affected dependency; historical proof is
preserved, never deleted. This runtime invalidation is a **restricted
consequence of an already-accepted proof**, not a fresh exercise of a
revoked/expired grant and not a general privilege for a session-level
reporter: it can only move the one matching current SATISFIED version to
UNRESOLVED, never touch an unrelated target, waive/block/satisfy anything, or
disclose an unrelated target's details. All affected records are processed
through stable indexed pages; exceeding a configured work limit aborts the
entire update/invalidation transaction atomically — there is no successful
partial invalidation.

**Alternative considered and rejected (C-10).** Claude E6 proposed recording
the invalidating transition "as runtime policy acting for the matcher, with
`GrantID` set to the grant on the satisfying transition" — reusing the
live-action `GrantID` field. Codex's cross-check required "a narrowly defined
runtime consequence of the previously accepted resource-bound proof, not a
generic caller privilege and not a fresh exercise of an expired grant,"
naming "actual reporter and originating authorization reference." **Trade-
off:** both proposals agree a stale proof must invalidate despite grant
expiry or revocation; reusing the live `GrantID` field is a smaller schema
change but misstates the authorization actually in force at invalidation
time, and cannot express a taskless resource reporter's lack of private-
target access in the first place. **Ruling: ADOPT the restricted cause-based
path with a separate historical `OriginAuthorizationRef`,** distinct from a
live `GrantID`; the internal invalidation method is runtime-only (no public
API surfaces it directly) and is tested to prove it can perform no other
transition and disclose no proof detail beyond the invalidated status
itself. FR-AUTH-002 and FR-OBL-002 are amended (§below) to name this path
explicitly.

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
  level; adding one would require re-deriving FR-ING-002's total order for no
  behavior this contract needs.
- **A dedicated `ActionEvaluateMatcher` mutation-grant action.** Rejected:
  matcher grants already authorize `ActionAssertObligation` only (ADR 16,
  reaffirmed at §1); a separate action would let a grant scoped to
  "evaluate" bypass the same-action restriction the ADR 16 matcher-grant
  validation depends on, with no requirement this contract needs it for.
- **A separately stored item-to-obligation `SATISFIES` edge in
  `graph.Relationship`.** Rejected at §4/Q2: it would let a caller construct
  a fabricated satisfaction edge the way M7 (ADR 19 §18) already forbids for
  every other relationship type, and it duplicates state that the transition/
  proof records already hold as the single source of truth.
- **Reusing `AuthorizeMutation`'s live grant-target check for the
  invalidation path (§13).** Rejected as part of C-10: invalidation only
  ever narrows one already-SATISFIED target to UNRESOLVED as a documented
  side effect of an accepted resource report, never a fresh grant-authorized
  mutation; running it through the same live-grant check would let a
  reporter's session-level authority be mistaken for target-specific mutation
  authority it does not have.
