# 19. Directive parsing and ingestion

Status: Accepted (2026-09-26, Phase 2 exit; PR #5 merged as b869cb1 after SEC 4, SPEC 5, DUR 5, TEST 2 review rounds reported NO FURTHER WORK NEEDED)
Date: 2026-09-26

## Context

Phase 2 (SDD §11 item 2) implements FR-ING-001..007, FR-AUTH-001,
FR-DIR-001..007, section 9 (security), and event traces T01/T02 (ingestion
half), T06 (parse/authorization half), T18 (full), against the Phase 1
foundation (`internal/domain`, `internal/store`, `internal/graph`,
`internal/invocation`) accepted in ADRs 1, 3, 4, 6, 13, 16, 17. The
commander's Phase 2 brief proposed decisions D1-D19 (D20 was added from
ADR 4's acceptance). An adversarial decision review (Codex gpt-6-astra
xhigh) found D2, D4, D9 correct as written and amended the other sixteen,
reproducing three concrete counterexamples against the merged Phase 1 code
(lossy SQLite text storage, a duplicate directive resolving as current by
literal ID, and `SupersedeSnapshot` rejecting a duplicate Working member
instead of retiring the item it should have replaced) and adding eight
missing decisions, M1-M8. The commander accepted every amendment and
missing decision as written, with rulings R1-R8 that further narrow or
override eight of them. This ADR records the resulting decisions — the
brief's choice, the review's amendment, and the commander's ruling where one
applies — grouped by subsystem, for Phase 2's five worker branches
(`p2-contract`, `p2-parser`, `p2-store`, `p2-graph`, `p2-ingest`).

Sources: `phase2-brief.md` (D1-D20), `phase2-decision-review.md`
(amendments and M1-M8), `phase2-amendments.md` (R1-R8, binding).

## Amended in Phase 3 (ADR 8, 2026-09-26; reconciled against integration head `fc87199`)

Phase 3's binding decision record (`.worktrees/_commander/phase3-decisions.md`,
P3-4, P3-34, P3-35, P3-36, P3-40, with commander ruling C-1 FROZEN
2026-09-26, reconciled against W7's landed `internal/ingest`) restates and
extends this ADR's §7 (D10's deduplication/replacement rule) and adds the
typed-operation/lifecycle-v2/v3-identity decisions this ADR's ingestion
pipeline already owned, now that Phase 3 implements them.

**Q1, restated (already accepted before this Phase 3 record, unchanged
here):** "Reopening requires a changed version (different content or
attributes) under FR-DIR-002['s] authorized replacement." Ordinary identical
restatement — the same content and the same accepted attributes as the
current version's immutable creation declaration (§7's `SameDirectiveSemantics`
comparison, now formalized as a persisted `CreationDeclaration` per ADR 8's
domain-work; see that ADR's §P3-4 in the binding record) — never reopens,
re-pins, unarchives, or rebinds an obligation merely by being reingested. A
new TURN/TTL eligibility origin remains, as it always has been under §7's
D10, a meaningful identity change, not a refresh of the old occurrence in
place.

**C-1's addition (new in Phase 3): an explicit authenticated typed
replacement intent may reuse identical content.** `ActionReplaceDirective`
is a new mutation naming an expected current occurrence and version (CAS),
carrying its own idempotent request identity (§10/D14's idempotency
discipline, unchanged), authorized exactly as any other lifecycle mutation
under this ADR's §11/D15 source-actor rule and ADR 16's `AuthorizeMutation`.
Unlike ordinary D10 restatement, it may legally recreate a version whose
content and attributes are byte-identical to the version it replaces — this
is not automatic reopening through re-ingestion, because it requires an
explicit, authenticated, CAS'd operation naming exactly which occurrence/
version it replaces, the same discipline this ADR's §7 already requires for
every other authorized replacement. It is audited together with all
indirect consequences: every obligation version bound to the retired source
is authorized and retired in the same operation (§9/D13, unchanged), and the
new version starts fresh (a new OPEN goal, or a new UNRESOLVED obligation
with no inherited proof or grant), exactly as any other D10 replacement
already does. This is not a new textual `Reopen` directive keyword — §1/M4's
closed unsupported-lifecycle vocabulary (`Archive, Unarchive, Promote,
Demote, Block, Unblock, Waive, CompleteTask, Reopen`) is unchanged, and
`Reopen` remains an `ErrUnsupportedDirective` diagnostic when written as a
directive heading; `ActionReplaceDirective` is a typed mutation-service
operation, not directive-grammar surface.

**Alternative considered and rejected.** Claude M9 had originally
recommended "keep[ing] the spec-literal behavior (it is an authorized
replacement)" — treating an identical-content restatement itself as an
authorized replacement, which Q1 already overrode before this Phase 3 record
existed. The Phase 3 cross-check's proposal — "[an authenticated intent] may
reuse identical content, and is checked/audited as `ActionReplaceDirective`,
including all indirect obligation retirements" — is what C-1 adopts, as an
*additional* explicit escape hatch alongside Q1's changed-content
requirement, not a reversal of Q1. **Trade-off:** Q1's changed-content rule
alone is simple and prevents accidental reopening, but requires an artificial
edit merely to recommission identical work or rebind a legacy claim; adding
the explicit typed intent preserves Q1's default while giving an authorized
principal a deliberate, audited way to do exactly that when it is actually
needed. **Commander ruling (FROZEN 2026-09-26): ADOPT the recommendation and
amend Q1 to add this explicit escape hatch, without weakening Q1's default.**
Real code confirms this exactly: `domain.ActionReplaceDirective`
(`internal/domain/authz.go`), `domain.ReplaceDirectiveIntent`
(`internal/domain/lifecycle_intent.go`), and
`lifecycle.Service.ReplaceDirective` (`internal/lifecycle/replace.go`, W3),
which itself calls `graph.ReplaceDirective` (`internal/graph/graph.go:298`,
this ADR's §7 original decision, unchanged), implement it; obligation retirement
runs through ADR 8 §2's `obligation.DeclareForReplacementTx`. Tests:
`internal/lifecycle`: `TestReplaceDirectiveReopensWithIdenticalContentAndRetiresObligations`,
`TestReplaceDirectiveNeedsAuthorityOrExactGrant`,
`TestReplaceDirectiveFailsClosedOnUnknownIdentity`; `internal/ingest`:
`TestGateT02_ReplacementRetiresOldRequirement`,
`TestP336_ChangedRestatementReplaces`,
`TestP336_ResolvedRestatementStaysResolved`,
`TestP336_UnpinnedRestatementStaysUnpinned`; `internal/graph`:
`TestReplaceDirective_T02`.

**Typed operations, v3 identity, and lifecycle-command v2 execution (P3-34,
P3-35, P3-40) — implemented in `internal/ingest`, this ADR's own package.**
`Event.Operations` is an ordered, typed operation stream (span, alias,
control, and mutation-intent operations) hashed under
`ingest-payload/v3` (ADR 4), replacing the frozen v2 encoder for new events
only — v2 stays frozen and still validated for legacy replay. A submitted
operation's `RequestID` must be empty; `domain.OperationRequestID(p
Principal, occurrence string, operation, command uint64)` derives it only
after acceptance, so no caller can forge or predict one — its current
signature binds a full principal, not just a session ID (PR #6 round 1,
G3/SEC-1.2, fixing the stale `(session, occurrence, opIndex, ordinal)`
signature this ADR previously described). **H5 landed in this
reconciliation (PR #6 round 2, SEC-2.2/SEC-2.6, commit `a1d734f`, W1):
`OperationRequestID` now binds both principals explicitly, not one.** Its
current signature is `OperationRequestID(authenticated, owner Principal,
occurrence string, eventSeq, operation, command uint64)`: `authenticated`
is the ingesting principal H5 requires (`internal/ingest/ops.go`'s
`typedOperation` now passes `r.p`, the authenticated caller, not the
lowered source actor it passed before), and `owner` is the receipt owner
(the lowered actor or dispatcher) the request is filed under — the ID also
now binds the event's own sequence (`req_<eventSeq>_<inner>.<tag>`), so a
caller cannot name a future or past event's request. `MutationReceiptID`
runs before any receipt lookup and accepts a `req_` ID only for its exact
owner and only when its event sequence was allocated in the current
transaction: no principal, including one sharing every field with the
lowered actor, can name another principal's runtime request. GC runtime
IDs follow the same rule from `domain` directly:
`GCTriggerRequestID` binds the authenticated origin (`gc-trigger/v2`) and
`GCRequestRecordID` names the queued request; `gc_` and `gcq_` join `req_`
as reserved prefixes callers may never supply, checked by
`ValidateCallerRequestID` before every standalone lifecycle mutation
(item, grant, `CompleteTask`, `ReplaceDirective`) and manual `Collect`.
**An empty, non-nil `Operations` is rejected at validation, never
silently treated as "no operations" (G4 = SEC-1.11 = SPEC-1.2).**
`Event.ValidateV3` (`internal/domain/ingest_v3.go`) requires `Operations`
to be either `nil` (every span ingests in the frozen per-span order) or a
non-empty stream covering every span — before this fix, an event with
spans and an explicitly empty `Operations` slice (e.g. JSON
`"operations": []`) took the v3 path and stored its spans without ingesting
any of them, silently losing content under an `EventID` a caller could
never successfully retry (`Operations: nil` on retry produced
`ErrEventIDConflict` instead of re-ingesting). Tests:
`TestV3RejectsEmptyNonNilOperationStream`,
`TestEmptyOperationStreamRejected_G4`. Lifecycle-command v2 executes
Resolve/Unpin
in source order at each command's exact, allocated authorization sequence
(never a predicted one), with C-2's narrowed `DetailAccess` redaction
applied to the execution outcome itself, not just target resolution. A
malformed operation, an alias to a nonexistent prior result, or an
unauthorized source actor aborts the whole event atomically. Tests:
`TestV3_SpanOperationsFollowStreamOrder`, `TestV3_RetryUsesRecordedSchema`,
`TestV3_RetryUsesRecordedPolicy`, `TestV3_DirectiveNamespaceExplicit`,
`TestOps_OrderSequenceAndAliases`, `TestOps_AliasBindsSpanItem`,
`TestOps_AliasRejections`, `TestOps_SourceSpanActor`,
`TestOps_ControlEventOpensNothing`, `TestOps_MissingHandlerFailsClosed`,
`TestOps_CallerRequestIDRejected`, `TestCommandsV2_ExecuteInSourceOrder`,
`TestCommandsV2_DetailRedaction`, `TestCommandsV2_AbortsAtomically`,
`TestLifecycle_ExecutesInOrder_P335`, `TestConcurrency_IdenticalV3Retries`.
Frozen-fixture/upgrade coverage: `TestPhase2FixtureReplay` (a Phase 2 SQLite
database, frozen at `b5f6b1f`, replays every original receipt unchanged
under the new v3 code with no new sequence and no lifecycle command
executing).

**Semantic-change audit data (P3-36) — implemented across `internal/ingest`,
`internal/lifecycle`, and ADR 8's `internal/obligation`, stored and checked
by the stores.** `domain.SemanticChange` records old/new revision/status/
currentness for every lifecycle/replacement/proof-invalidation/observation-
state change, with an immutable, stored cause. Tests: `TestP336_ChangedRestatementReplaces`,
`TestP336_ReplacementHistoryReconstructible`, `TestP336_ResolvedRestatementStaysResolved`,
`TestP336_UnpinnedRestatementStaysUnpinned` (`internal/ingest`);
`internal/obligation`'s `TestSemanticChangeRecords` (ADR 8's "Implementation
decisions beyond the frozen text," change causes).

### SDD amendment (applied in v0.10)

- **FR-DIR-005 / FR-ING-005.** Add: "Ordinary identical restatement compares
  the immutable creation declaration and does not reopen, re-pin, unarchive,
  or rebind an obligation. TURN/TTL eligibility origin remains part of
  semantic identity." Retain Q1's changed-content/attributes requirement as
  the default; `ActionReplaceDirective` (above) is the one authorized
  exception, itself recorded at FR-DIR-005/FR-ING-005 (this same amendment)
  rather than as a weakening of this sentence.

Applied to SDD.md as v0.10 (this ADR does not itself edit SDD.md).

## Decision

Grouped by subsystem. Each group names the requirements it answers, the
accepted decision, and which package(s) in the work split own it.

### 1. Lifecycle commands: parsed and authorized, not executed (D1, R7)

FR-DIR-005, FR-AUTH-001/002, §8/9, trace T06.

**This section is Phase 2's original record and describes only Phase 2's own
v1 lifecycle-command behavior (frozen forever, per this ADR's §41 legacy
manifest): a Phase 2 (`lifecycle-command/v1`) command never executes, and
never will, even after upgrade.** Phase 3 (P3-35, this ADR's Phase 3
amendment above) executes new lifecycle commands in source order instead of
leaving them `PARSED_NOT_EXECUTED`; "not executed" below is not the current
behavior for a new command, only the permanent behavior for an old one.

Phase 2 parses `Resolve`/`Unpin` into immutable command records — action,
exact target spelling, the authenticated source actor (§11 below), span
authority/access, event occurrence, source range, parser version — and
resolves the target through `graph.ResolveLifecycleTarget` (ADR 16) against
ingestion-transaction state. Missing and inaccessible targets produce the
identical `ErrNotFound` diagnostic; several accessible current candidates
produce `ErrAmbiguousDirective`; neither aborts the event. The command
additionally validates the target's kind/current state and checks mutation
authorization (FR-AUTH-001/002) using the source actor — an unauthorized
source actor aborts the whole event atomically (R7 overrides the review's
silent-defer framing: authorization failure is fatal to the event, not
merely a diagnostic). An authorized, resolved command is recorded
`PARSED_NOT_EXECUTED`: Phase 2 changes no goal status, pin generation, or
obligation state, and reports no successful lifecycle transition. Phase 3
must independently revalidate access, source authorization, target
version/currentness, and any grant in the transaction that actually
executes the command; a Phase 2 resolution is not a capability, and
upgrading to Phase 3 does not retroactively execute historical Phase 2
commands. Other lifecycle words use the closed vocabulary in §4/M4 below.

Owner: `internal/ingest` (command construction, authorization check, event
abort), consuming `graph.ResolveLifecycleTarget`.

### 2. Classification: pure defaults in `internal/policy`, deterministic table in `internal/ingest` (D2, M2)

FR-ING-001/003, FR-DIR-003, FR-REL-007, §9/10/11.

D2 (package split) is unchanged from the brief: default
generation/scope/retention/mandatory-status tables and attribute rules
(FR-DIR-003) live in pure `internal/policy`; orchestration and
classification live in `internal/ingest`. M2 fixes the missing content:
classification is a versioned table keyed by (authenticated event/span
type, accepted directive section) → (kind, generation, scope, retention,
goal state, required structured fields), applied only inside an authorized,
parsed section. Ordinary text never infers goals, pins, obligations,
grants, security constraints, lifecycle commands, or tool success from
surface tokens (`MUST`, `SYSTEM`, `PASS`, role-shaped labels); a
low-authority kind hint is allowlisted and cannot create a privileged
record. Unsupported event variants are rejected explicitly. The event's
classification/parser/default policy versions are persisted with every
deterministic rule edge they produced.

Owner: `internal/policy` (tables), `internal/ingest` (table application,
version stamping).

### 3. Byte-exact snapshots and content hashing (D3, R8)

FR-ING-007, FR-DIR-004, FR-DOM-008/009, FR-PER-003, INV-08/09.

Raw span snapshots preserve every byte, including BOMs, invalid UTF-8, and
line-ending choice; their digest is `HashBytes(raw)`, distinct from
`domain.ContentHash(parts)` (the existing canonical-parts hash, ADR 4).
Parser v1 treats LF, CRLF, and lone CR as one line break, skips one UTF-8
BOM only at byte zero of a parse unit, and performs ASCII token recognition
with no Unicode repair, folding, or normalization; offsets are half-open
byte ranges into the original snapshot, and any presentation transformation
used to build directive text is D7's rule, recorded with the parser
version. Both stores must round-trip every accepted byte and verify content
identity after restart. R8: replacing SQLite's lossy `[]ContentPart` JSON
encoding (`internal/store/sqlite/schema.go:277`, which the review
reproduced replacing invalid UTF-8 with U+FFFD, silently breaking
`ContentHash` identity) with a lossless byte representation is a Phase 1
bug fix, lands first, in its own commit, with a `storetest` case, ahead of
the rest of Phase 2's store work.

Owner: `internal/domain` (hash/byte semantics), `internal/store` (both
backends + storetest, R8 first).

### 4. Parser grammar: keywords, heading recognition, section extent, item extraction (D4-D7)

FR-DIR-004/006/007, FR-ING-004/007, §9/12, trace T18.

- **Keywords (D4, agreed):** ASCII-only case-insensitive keyword matching —
  never `strings.EqualFold`/Unicode folding, so the Kelvin sign and long s
  never match a keyword. IDs and attribute values use only the ABNF's ASCII
  sets. `DerivedDirectiveID` (ADR 4) is only ever called with an
  already-validated canonical keyword; it is not itself a keyword
  recognizer.
- **Heading recognition and suppression (D5, amended):** a directive
  heading starts at byte-column zero with 1-6 `#`, one ASCII SP, and a
  complete keyword token; leading indentation, a tab in place of the SP,
  Setext syntax, and a seventh `#` never create one. Suppression is a local
  parser state machine, scoped to one parse unit (§5/M1): a fence opens
  with ≥3 identical backticks/tildes at indentation 0-3 and closes only
  with the same character, at least the opening length, indentation 0-3,
  and whitespace-only trailing bytes — a shorter fence or a closer with
  trailing text does not close it; block-quote lines and HTML comments
  (`<!--`...`-->`, possibly multi-line) are inert while a fence is open and
  vice versa; an unclosed fence or comment suppresses through parse-unit
  end. Stripping a wrapper (quote marker, comment) and reparsing the
  remainder as a heading is never done. Suppressed directive-looking
  headings, and headings in a non-capable span, produce a bounded
  `DirectiveNotParsed` diagnostic with an enumerated reason and the
  original byte range, never echoed content.
- **Section extent (D6, amended to match FR-DIR-006 literally):** an open
  section ends at the next *unsuppressed* heading of the same or higher
  level, or at parse-unit end — not, as the brief proposed, at any keyword
  heading regardless of level. A deeper heading (keyword or not) is body
  content and cannot itself open a directive or lifecycle command; only a
  peer-or-higher heading can close the current section, and only a valid
  keyword at that boundary opens a new one. A malformed heading at that
  boundary closes the prior section (with a diagnostic) but grants no
  directive semantics to following text until the next valid peer/higher
  directive heading.
- **Item extraction (D7, amended for lossless, isolated malformation):** a
  list section's top-level bullets create items; indented continuation/
  nested-list lines stay inside the preceding item. Only the specified
  bullet/ID/attribute syntax and continuation indentation are stripped;
  trailing whitespace on payload lines is never trimmed (it can be
  meaningful content whose loss would silently conflate two directives'
  hashes), and blank-line trimming in a single-body item touches only
  leading/trailing blank lines. Blank-line/indentation detection uses ASCII
  SP/HTAB only. Every item records its original header/body/item byte
  ranges. A heading ID on a list section is diagnosed and ignored, never
  copied onto every member. Invalid explicit-ID/braced-metadata syntax
  makes that item non-executable content (`ErrMalformedDirective`) rather
  than silently getting a derived ID; unknown/invalid/disallowed attributes
  are diagnosed and ignored per item, without dropping sibling items. Empty
  item text is diagnosed and creates no directive. `Resolve`/`Unpin` accept
  only a heading target with a blank body, or target-only list items
  (`[id]` with no text); any mixed or attributed form is malformed and
  creates no command for that unit.

Owner: `internal/directive` (all four); remaining ambiguities not fixed
above are M4 (§4's remaining gaps are closed by the classification/grammar
clarifications in the ADR's SDD amendment section).

### 5. Transcript items, derived items, and parse-unit isolation (D8, M1)

FR-ING-003/004, FR-DOM-007, FR-REL-003/008, FR-MAT-004, FR-ASM-010, trace
T01/T02/T18; ADR 6.

Each span produces exactly one immutable transcript item (verbatim span
content; kind by span authority: USER→`user_message`, AGENT→
`assistant_message`, TOOL→`tool_result`, RETRIEVED_CONTENT→`evidence`,
SYSTEM/HARNESS→`conversation` — corrected below, SPEC-2.6) plus zero or
more directive items, each
`DERIVED_FROM` its transcript item with `Coverage` naming that item —
byte-level ranges live on the directive item's own `SourceRef`/diagnostics,
not on the `Relationship`. A directive-bearing transcript is an
audit/pending-input envelope, never an independent current requirement
merely because its authority is SYSTEM or HARNESS: an accepted directive
gets its own semantic instruction item, and the raw transcript copy must
not persist as an active instruction after that directive is replaced —
future rendering (Phase 4/5) must track expiry against both. **Landed
transcript defaults differ from this ADR's original text (SPEC-1.5,
corrected here): every transcript item, regardless of authority, is
`policy.ForTranscript`'s row (`internal/policy/defaults.go`) — USER gets
kind `user_message`, AGENT `assistant_message`, TOOL `tool_result`,
RETRIEVED_CONTENT `evidence` (TOOL/RETRIEVED_CONTENT additionally get
EPHEMERAL generation, TURN scope, LOW retention), and SYSTEM/HARNESS get
kind `conversation` with WORKING generation and NORMAL retention (SESSION
scope for SYSTEM, TASK scope for HARNESS) — never `instruction`/DURABLE/
HIGH.** The `instruction`/DURABLE/HIGH/mandatory-for-SYSTEM row this ADR
originally described belongs to the separate residual instruction item
(`policy.ForResidual`, §24/R20 below), derived from a SYSTEM or HARNESS
transcript's unclaimed bytes, never to the transcript item itself — a raw
transcript never becomes an active instruction merely because of its
authority, which is D8's own concern and the safer of the two readings.
`TestTranscriptDefaults` and `TestResidualRows` lock the two rows
separately. Final item access is the requested scope boundary intersected
with the authenticated span boundary; an impossible combination is
rejected, and a derived item's access must stay within its
transcript/source boundary (scope attributes cannot declassify). Store a
complete mapping from transcript ranges to derived item IDs/versions and
section IDs.

M1 (parse-unit isolation) is the structural precondition this depends on:
an `Event` carries an ordered, typed span/part envelope, where each text
parse unit has exactly one authority, one directive-capable flag, one
validated access boundary, and one immutable snapshot identity; overlapping
ranges with conflicting metadata are rejected, and gaps in a shared buffer
never inherit neighboring authority. Every span, and every part within a
span, ends the parser's section/list/fence/comment state — adjacent spans
or parts are never concatenated into one directive token — and an
image/document part interrupts text parsing. Offsets are stored as
half-open ranges qualified by their owning snapshot/part ID, never a bare
unqualified offset. This closes the review's concrete attacks: a trusted
`'# Pin'` span followed by a retrieved `'ned\n...'` span, or a fence opened
in one span and closed in another, must never produce a privileged
directive.

Owner: `internal/domain` (Event/Span/part envelope, SourceRef ranges — a
schema change), `internal/directive` (per-unit state reset), `internal/ingest`
(transcript/derived item construction, access intersection).

### 6. Directive identity: derived vs. explicit IDs (D9, D20)

FR-DIR-002/006, ADR 4.

D9 (agreed): `domain.DerivedDirectiveID(validatedKeyword,
domain.ContentHash(parts))` — lowercased keyword, hyphen, full 64 lowercase
hex digits — is the only derived-ID scheme, distinct from the opaque item
ID of each occurrence/version. Derived IDs are published in
diagnostics/inspection even with no other warning. D20: directive IDs are
otherwise exact (case-sensitive); a user/system-supplied `[id]` that has the
*shape* of a derived ID (a lowercased content-section keyword — Goal,
Pinned, Working, Remember, References, or Ephemeral, never Resolve/Unpin,
since only content sections derive IDs (§6/D9) — a hyphen, and 64
lowercase hex digits; SPEC-2.7 corrects "a lowercased keyword" to name the
six-keyword scope explicitly, since `resolve-<64hex>`/`unpin-<64hex>` are
ordinary explicit IDs, not derived-ID-shaped rejections) is rejected with
`ErrMalformedDirective` and the item is dropped — not parsed with a
fallback derived ID — so an explicit ID can never collide with, or be
mistaken for, a derived one.

Owner: `internal/directive` (shape check at parse time), `internal/domain`
(`DerivedDirectiveID`, unchanged).

### 7. Deduplication, replacement, and Working snapshots (D10, D11)

FR-ING-005, FR-DIR-002/007, FR-DOM-003/005/007, FR-AUTH-001, INV-04/09;
ADR 4/16.

**D10 (amended):** deduplication runs only after source authority/access
and scope policy are validated, and never bypasses currentness. A directive
duplicate must match session, task, exact boundary, exact authority,
section, directive ID, canonical content, and effective
kind/generation/scope/retention/TTL/goal-state/obligation-declaration/
eligibility-origin (Working items additionally use D11's snapshot identity);
a TURN-bound item from a different turn, or a TTL item with a different
expiry origin, is never a duplicate stand-in. A same-ID lower-authority
write is never deduplicated against a higher-authority current item. An
authorized same-boundary replacement uses `graph.ReplaceDirective`; a
forbidden boundary/scope change is rejected outright, never silently
forked. A duplicate creates only an immutable audit item and a
`DUPLICATE_OF` edge — no `SUPERSEDES`, current-map update, obligation
creation/reset, or inherited metadata — and every current-version consumer
(literal lifecycle lookup, Working candidate selection) must require the
current-map entry name that item, no incoming supersession, and no
`DUPLICATE_OF` classification; this closes the review's reproduced hole
where `graph.IsCurrent`'s incoming-edge-only check let a duplicate still
resolve as current by literal ID. Canonical-candidate selection, once
access/identity filters run, is deterministic by `(Seq, ID)`.

**D11 (amended):** a Working section is one snapshot operation, partitioned
by task/exact source authority/exact final access boundary; its semantic
identity is its ordered member definitions and their effective metadata
(including explicit IDs). An identical whole snapshot is a duplicate
snapshot (no supersession); a changed snapshot creates fresh member
versions even where some member's bytes repeat a prior member's — repeated
bytes alone never make a changed-snapshot member a `DUPLICATE_OF` its
predecessor, closing the reproduced `W1={a,b}→W2={a}` failure where marking
`a` a duplicate first made `SupersedeSnapshot` unable to use it to retire
`b`. Replacement is one planned edge set per section: every prior current
member in the partition retires exactly once, new members of the same
section never supersede each other, and any denied replacement aborts the
whole event. An empty/all-invalid section retires nothing; a partially
malformed section preserves all raw bytes/diagnostics and performs no
replacement at all, so a parse failure can never erase an omitted member.
Multiple Working sections in one event process in source order.
`graph.SupersedeSnapshot`'s first-matching-new-member-as-superseder
convention is documented as deliberate (not all-to-all);
`graph.Supersede`'s `(session, old target, action, event)` audit-ID
collision on two retirements of the same old target in one event, and
same-ID pointer replacement after snapshot supersession, must both be
closed by the planned edge set itself.

Owner: `internal/graph` (D10's current predicate; D11's planned edge set
and audit-ID fix — `p2-graph`), `internal/ingest` (authority/scope/
eligibility validation ahead of dedup — `p2-ingest`).

### 8. Attribute values, TTL bound, scope widening (D12, R1)

FR-DIR-003/006, FR-ING-003, FR-REL-008, INV-05/09; ADR 6.

Attribute names and values are exact case (not ASCII-case-insensitive,
correcting the brief); only the exact scope spellings
TURN/TASK/WORKFLOW/SESSION/AGENT and exact lowercase FR-DIR-003 kind
spellings are recognized, under FR-DIR-006's per-section allowlist.
Unknown/invalid/disallowed attributes are diagnosed and ignored without
erasing a valid inherited (section-level) value. R1 sets the accepted
`ttl` range at 1..2147483647 (`math.MaxInt32`, portable across Go int
widths) with leading zeros allowed, parsed from ASCII decimal digits with
checked arithmetic; a larger syntactically-valid value fails event
validation with an explicit representation-limit error rather than being
ignored or silently unlimited — this is a storage bound distinct from, and
layered on top of, the syntax rule for a malformed attribute. For USER
spans, requested WORKFLOW/SESSION widening is ignored with a diagnostic;
final access is always the validated intersection of requested scope and
authenticated source boundary (AGENT scope, in particular, never removes
an existing task/workflow constraint, since `BoundaryFor(AGENT,p)` alone
would otherwise permit the same agent in another task), and SYSTEM/HARNESS
scope attributes likewise cannot remove source access constraints or
bypass D10's explicit-replacement rule.

Owner: `internal/policy` (attribute/scope rules), `internal/directive`
(ttl parse + bound check, R1), `internal/domain`/`internal/store` (int32-
bound TTL representation).

### 9. Obligation declarations on Pinned items (D13)

FR-AUTH-001/002, FR-OBL-001/002/004/006, trace T02/T06.

A valid, nonduplicate `obligation=<claim>` on a Pinned item creates an
UNRESOLVED obligation version bound to that exact source item, authority,
and access constraints; the claim name is persisted separately from an
executable `MatcherRef` — Phase 2 sets no matcher version and grants
nothing. A name in text is not a registry lookup, code load, authorized
assertion, or authority grant (registry/claim-pattern creation and
evaluation are Phase 3/ADR 8, explicitly out of scope here). Obligation
identity derives from the session/task/boundary/directive identity plus an
explicit declaration slot, versioned on actual source replacement, never
keyed globally by claim name. In the same transaction as any source
directive replacement, every obligation version bound to the retired
source is authorized, marked noncurrent with a `RetiredSeq` and audit
record, with status/evidence history preserved; this applies equally when
a replacement drops `obligation=` or moves the section away from Pinned. A
replacement obligation version is created only if the new source declares
one, and never automatically inherits proof, waiver, satisfaction, or a
grant. A duplicate creates no new obligation version.

Owner: `internal/domain` (obligation claim-name field, separate from
`MatcherRef` — a schema addition), `internal/graph` (retirement-on-
replacement, alongside D10/D11's replacement path — `p2-graph`).

### 10. Event idempotency, receipts, and the transaction-scoped ingestion core (D14, M3)

FR-ING-001/006/007, §10, FR-OBS-003, FR-CALL-003, INV-09/10, ADR 4/17.

**D14:** the caller's complete request is hashed with a versioned
`CanonicalEncoder` schema over principal, event kind/source/turn-boundary
metadata, ordered spans (each span's authority/access/capability/source),
ordered typed parts (media/encoding, verified byte/hash identity), and
supported structured relationship/obligation metadata — distinctions
parsing ignores are still hashed. Runtime-selected parser/policy versions
are recorded execution inputs, never silently folded into the caller's
retry fingerprint. Within the session transaction, an existing `EventID`
and the complete request are checked before any sequence, turn, item ID,
transition, or diagnostic is allocated; a match returns the *original*
immutable receipt (no reparsing, no re-resolving against current state); a
mismatch returns `ErrEventIDConflict` with no prior-result detail. The
receipt stores the original returned item versions/values, ordered IDs,
diagnostics, lifecycle parse results, generated IDs, and execution versions
— not just item IDs, so a later lifecycle mutation or a policy upgrade
cannot change what a replay of an old `EventID` returns. Events without a
caller `EventID` get a fresh internal occurrence ID (generated once per
`Ingest` attempt, outside any retried transaction callback) with no retry
guarantee, but full receipt/diagnostic persistence. Caller-supplied bytes
and an equivalent verified blob reference canonicalize identically, and
caller-owned buffers are deep-copied before hashing/parsing.

**M3:** occurrence and relationship IDs get one documented, deterministic
order: for a stable `EventID`, `domain.DerivedItemID(session, eventID,
index)`, with index order spans/parts in input order, transcript items
first, then residual/directive items in source order; relationship,
command, diagnostic, obligation, section/snapshot, and turn IDs use
separate versioned ID domains with explicit occurrence ordinals — an
explicit `DirectiveID` never substitutes for the immutable item ID. A
transaction-scoped ingestion core (authenticate → idempotency check → plan
ordered effects → validate the full plan → persist and return the
immutable receipt) is the single implementation the outer `Ingest` wrapper
calls, reused later by `RecordCallOutcome`; it must never re-enter
`Store.Update`/`Store.View` (SQLite deadlocks on re-entrant transactions),
and every semantic write goes through transaction-allocated, non-
`TargetCall` sequence records with no partial result on failure.

Owner: `internal/domain` (canonical schema, receipt/occurrence-ID types —
schema additions), `internal/store` (receipt persistence, both backends —
`p2-store`), `internal/ingest` (the transaction-scoped core, event
ordering, idempotency check-before-allocate — `p2-ingest`, after
contract/parser/store/graph land).

### 11. Authority, access, and the source actor (D15)

FR-ING-002/003/004, FR-AUTH-001/002, §8/9, INV-04/05/14.

The trusted embedding API — never event text — constructs the principal,
span authority/access, directive-capability flag, and event-kind/turn-
boundary envelope; every enum value and owner requirement is validated.
Authorization requires `principal.Authority.AtLeast(span.Authority)`
(reusing the existing `Authority.AtLeast`, `internal/domain/enums.go:35`,
which already encodes TOOL/RETRIEVED_CONTENT's non-domination correctly) —
never a numeric-rank or `SortRank` comparison. A span's boundary must be a
valid same-session boundary whose every nonempty owner constraint the
authenticated principal satisfies (`Access.Permits`), plus any narrower
authenticated integration grant; there is no implicit "principal boundary"
object synthesized from all principal fields. A directive's *source actor*
— the identity used for its own lifecycle commands and any indirect
mutation it triggers — carries the authenticated ownership IDs with
authority reduced to the span's authority, and only its own applicable
grants, never the stronger ingestion-service caller; item and obligation
authority always equal source authority. This closes the confused-deputy
path the review flagged for D1: a SYSTEM ingestion caller legitimately
carrying a USER span must never execute that span's `Resolve` as SYSTEM.
SYSTEM/HARNESS spans are always parsed, USER spans only when marked,
AGENT/TOOL/RETRIEVED_CONTENT never; marking one of the latter three
directive-capable is an invalid event and rejects atomically (a harness
bug, fail closed). Invalid incoming authority/access metadata rejects the
whole event; a lookup against an existing but inaccessible target returns
bare `ErrNotFound` with no existence detail.

Owner: `internal/ingest` (principal/span validation, source-actor
construction — the shared input to §1's lifecycle-command authorization).

### 12. Diagnostics: persistence and access (D16)

FR-DIR-004, FR-ING-006, FR-OBS-001, §8/9, INV-05/09; ADR 16.

Diagnostics are immutable, persisted atomically with the event receipt in
both stores, keyed by session, internal event occurrence ID, span/part
ordinal, and a deterministic per-span ordinal (a caller `EventID` is a
separate lookup key, since anonymous events collide on an empty one). Each
record carries the source access boundary, structured code/severity/
reason, section, a validated directive ID where applicable, original byte
range, parser version, and schema version. Messages are fixed templates
that never echo source text, malformed tokens, locators, inaccessible IDs,
or raw store errors; a validated ID is reported only to principals
authorized for that source span, and every diagnostic read or telemetry
projection enforces access before disclosing IDs, ranges, counts, or
reasons — repeating ADR 16's fix for graph-layer error text (identical
error text for missing vs. inaccessible) at the diagnostic layer, since
"never content" does not by itself make an ID, count, or range public.
Ordering and the 256-plus-one truncation rule (D17) are stable and stored;
an idempotent replay returns the receipt's original diagnostics unchanged.

Owner: `internal/domain` (diagnostic record type), `internal/store`
(schema migration, access-checked reads, both backends, restart/rollback/
anonymous-uniqueness/replay coverage in storetest — `p2-store`).

### 13. Parser and event resource limits (D17)

§9/12, NFR-002, FR-ING-001, INV-10.

Parser work is linear in accepted bytes plus bounded output, including
diagnostic scanning; default limits are 8 MiB per text span, 4096 parsed
items per span, 256 diagnostics plus one truncation marker per span.
Beyond the brief's per-span caps, finite versioned limits also bound total
event bytes, spans/parts, total items, total diagnostics, total blob
bytes, attribute tokens/length, and generated relationships, since
thousands of small spans could otherwise evade a per-span cap and force
repeated scans of an unbounded prior graph. Limits are checked with
overflow-safe arithmetic before allocating/copying or committing; exceeding
most limits rejects the entire event, changing no task/turn/item/
relationship/obligation/receipt. **Two limits truncate instead of
rejecting, a recorded deviation from a literal reading of FR-DIR-003, SPEC-2.4:**
diagnostic emission stops at the cap (`MaxDiagnosticsPerSpan`/
`MaxEventDiagnostics`) without stopping suppression-state tracking or
semantic validation, and `MaxReferenceLinks` stops creating further
REFERENCES edges for the rest of the event with a
`ReferenceLinksTruncated` diagnostic rather than rejecting the event —
full mechanism and rationale in the SEC-1.4 paragraph below (§28). Limits
are trusted configuration, never directive attributes. Fuzzing covers
panics, valid offsets, deterministic output, source gating, and
structural/allocation bounds via scaling benchmarks or instrumented
counters, not wall-clock assertions inside fuzz tests.

Owner: `internal/directive` (per-span/per-parse-unit limits),
`internal/domain` (limits config type), `internal/ingest` (whole-event
totals, graph-scan bounds).

**Deferred ruling: `tx.Grants()` per obligation retirement/lifecycle
command, and four sibling whole-session reads (SPEC-1.3/SPEC-2.1, part of
F1's bounded-lookup findings).** `graph
/obligation.go`'s retirement path and `graph/lifecycle.go`'s command
authorization both call `tx.Grants()` once per call — an unfiltered,
whole-session read of every `MutationGrant`, not an exact-key lookup —
before checking `domain.AuthorizeMutation`'s grant-based path. Unlike
F1's other findings, this is ruled a deferred *performance* item, not a
correctness or security gap: a grant can only be created by an issuer
`AuthorizeGrantIssuance` already authorized (ADR 16), so the read
discloses nothing a session-scoped actor couldn't already be trusted to
see, and an unbounded grant count neither locks out a legitimate mutation
(it still succeeds, just slower) nor leaks anything (no boundary or
authority check is skipped). It is indexed in Phase 3, alongside that
phase's other obligation/grant work (ADR 8), rather than in this round.
**SPEC-2.1 found four more reads with the same shape, ruled deferred on
the same reasoning — none of them run on the per-item ingest path, only
on an explicit cross-event listing call:** `store.ObligationTransitions`
(every transition ever recorded, filtered in Go by obligation ID),
`store.LifecycleEvents` (every lifecycle event, filtered by target/`MinSeq`),
`store.Obligations(taskID)` (every obligation version in the session,
reduced to the latest per obligation and filtered by task — distinct
from the per-ID `Obligation`/`ObligationVersions` and per-source
`ObligationsBySource` reads §26 already covers, which SPEC-2.1's fix
made key-based), and `store.Diagnostics`/`LifecycleCommands` called with
no `OccurrenceID` (`visibleReceipts` then lists every receipt in the
session rather than looking one up). Each is a caller-facing inspection
API, never called by `Ingester.Apply` itself; R18's deferred-read ruling
above already anticipated exactly this shape ("a `Limit` parameter and an
`ErrLimitExceeded` result... no such cross-event caller exists yet in
Phase 2's scope"), and these four are recorded next to `tx.Grants()`
rather than opening a new deferral.

### 14. Turn advancement (D18)

Terminology (§1 of the SDD), FR-DOM-003/007, FR-AUTH-001/003, FR-ING-006,
§10, trace T03/T10.

A trusted USER-event envelope, or a HARNESS envelope explicitly flagged as
a turn boundary, advances a task's turn exactly once, regardless of span
count or contained-directive authority; AGENT/TOOL/RETRIEVED_CONTENT
payloads can never assert either form, so low-authority content can never
forge a turn boundary to expire higher-authority TURN requirements.
Advancement is determined only after the idempotency lookup (§10) and
persisted with `PutTask`'s required `TargetTask` audit and CAS in the same
event transaction. A task becomes ACTIVE on its first valid task-bound
event (preserving immutable workflow ownership); before any opener it has
`Turn=0` and no `TurnID` — no event synthesizes a first USER turn, and
TURN-bound or TTL-dependent items cannot be created without a valid owning
turn. A new opener sets `Turn=old+1`, a stable derived `TurnID`, and its
ordered pending-input item IDs. Every applicable item (including
TASK-scoped ones) records a creation-turn index/owner for TTL; N-turn TTL
is eligible while `currentTurn >= creationTurn && currentTurn-creationTurn
< N` in the recorded owning task, using the difference form to avoid
overflow; TURN scope always expires on the next turn regardless of a
larger `ttl`. A cross-task consumer never substitutes its own turn
counter. A COMPLETED task is never reactivated by ordinary ingestion. An
exact idempotent retry returns the original `TurnID`/result and never
re-advances or refreshes TTL.

Owner: `internal/domain` (`TaskState` transition rule), `internal/ingest`
(envelope-kind gate, transactional ordering with §10).

### 15. Blob/image reference authorization (D19, R5)

FR-ING-007, FR-REL-008, §9, INV-05/08; ADR 4.

Image/document bytes are immutable, session-scoped blobs verified by digest
and size before a referencing item is accepted; a supplied hash reference
must resolve to bytes already in that session, and D19's general rule
requires the reference be authorized through an accessible source item or
an explicit authenticated blob-access record — possession of a digest
alone grants no read/copy permission, closing the path where an agent that
merely learns another agent's private-document hash could reference it
into a new accessible item. R5 fixes Phase 2's concrete check: a blob hash
reference is accepted only when an item the principal can already access
references that blob in the same session; otherwise the caller must supply
bytes outright. Missing and inaccessible references return the identical
public error. Derived item boundaries stay within all referenced source
boundaries; a caller supplying full bytes may make a new authorized
assertion at its own authority/boundary without borrowing hidden
provenance. Media type/encoding/options live on each `ContentPart` and its
canonical hash, not on whichever blob metadata won first insertion. No
locator (URL/path) is ever fetched, opened, OCR'd, or extracted during
ingestion or replay; externally extracted text must arrive as its own
authenticated span.

Owner: `internal/ingest` (R5's access check at reference time),
`internal/store` (session-scoped blob existence, unchanged from Phase 1).

### 16. References resolution and deferred tool dependencies (M5, R2)

FR-DIR-003, FR-REL-002/007, §9, INV-05/07.

R2 confirms M5's References half is in Phase 2: lookup of accessible
same-session targets runs in the declared scope, filtered before any
ID/count or ambiguity is disclosed; a source locator is matched under a
documented, versioned lexical identity rule, with no filesystem or
network access. **(SPEC-2.2, corrected)** V1's rule has no
repository/resource namespace component or base-directory scoping — R19
(§23) confirms this explicitly, and `domain.LocatorKey` takes no such
argument; two same-named locators from different conceptual repositories
within one session are therefore the same target until multi-repository
support exists (tracked in Open questions below, not designed
speculatively now). Matching authorized targets link
deterministically, preserving each immutable reference and target
snapshot; an inaccessible target stays indistinguishable from an absent
one. Later path ingestion may add `REFERENCES` edges only under both the
original reference's authenticated ownership context and the current
event's authorization — an older broad reference can never be made to
disclose newly-ingested private evidence — and unresolved references
persist so restart never loses future linking. R2 defers M5's
tool-dependency half to Phase 5: `tool_call` items are created by the call
ledger, which Phase 2 does not implement, so a Phase 2 TOOL span may carry
a `ToolCallID` recorded only in `SourceRef`, creating no edge and no
semantics until Phase 5.

Owner: `internal/ingest` (lexical locator identity rule, deferred-link
persistence).

### 17. Directive vs. agent-key namespace (M6, R6)

FR-TOOL-002, FR-DIR-006.

FR-TOOL-002 promises agent keys (`agent.<key>`) never collide with
directive IDs, but FR-DIR-006's `id` grammar already allows dots, so
`agent.status` is a legal *directive* ID string, and `domain.AgentKeyID`'s
prefix is display syntax with no enforced separation from the current-map
key. R6: Phase 2 adds a typed namespace (`DIRECTIVE` vs. `AGENT_KEY`) to
the current-version key via a forward migration in both stores (never
editing migration 0001); lifecycle resolution (`graph.ResolveLifecycleTarget`)
only ever considers the `DIRECTIVE` namespace, so a keyed agent write can
never accidentally resolve as a Resolve/Unpin target even though Phase 3
is what actually performs keyed writes.

Owner: `internal/store` (forward migration, both backends — `p2-store`),
`internal/graph` (namespace-scoped resolution — `p2-graph`).

### 18. Explicit relationship input is never a trusted back door (M7)

FR-ING-001/003, FR-AUTH-001, FR-REL-002/006/007/008, §9.

Phase 2 either rejects unsupported explicit relationship declarations in an
event outright, or supports a closed, typed allowlist with full endpoint
access and action authorization for each — never arbitrary caller-supplied
`Relationship` rows, edge authority, coverage, or current-map updates.
`DERIVED_FROM` must name complete accessible sources and attach only
through `graph.LinkDerived` at item creation (ADR 16's `tx.Allocated`
gate); `SUPERSEDES` only through the common authorized transition path
(§7); `DUPLICATE_OF` only through D10 (§7). Relationship-shaped JSON inside
tool text remains inert text. This closes a bypass the review flagged: an
ungated generic "relationships" field on an event would skip
directive-capability checks and graph protections, and — if left out of
`PayloadHash` (§10) — would additionally permit conflicting retries.

Owner: `internal/ingest` (reject-or-allowlist gate; the allowlist, if any,
is `internal/ingest`'s own code, not caller data).

### 19. Public result/API shape and migration compatibility (M8, R3)

§7/8, FR-PER-002/003, ADR 3.

Phase 2's `domain.IngestReceipt` (the type this decision originally called
`IngestResult`; no `IngestResult` type exists — SPEC-2.2) is internal
until the public service signature is settled: R3 keeps it in
`internal/ingest` (or
`internal/domain`), *not* aliased from the root package. **(SPEC-3.6:
corrected)** The root package aliases plenty of ordinary domain types for
general use (`Principal`, `ContextItem`, `AccessBoundary`, and others,
`types.go`) — the R3 restriction is narrower than "only `Event`/`Span`":
it is that the root package never aliases the *richer ingestion result*
types (`domain.IngestReceipt`, diagnostics, lifecycle command records),
only the caller-constructed `Event`/`Span` input shape, an explicit
allowlist `TestRootAliasesOnlyIngestInputTypes` enforces (SDD §8's
`Ingest(...)
([]ContextItem, error)` signature change to return items plus
diagnostics/commands is deferred to the phase that implements
`Runtime.Ingest`, i.e. Phase 5). Migrations for source ranges, event
receipts, claims, the typed namespace (§17), diagnostics, and the lossless
text representation (§3) are all forward-only against the checksum-locked
migration 0001; upgrade/restart parity fixtures are required for
pre-existing records, and a record that predates new metadata gets
explicit handling rather than an invented executable default.

Owner: `internal/domain`/root package (alias scope), `internal/store`
(forward migrations + parity fixtures).

### 20. Round 2 rulings (p2-graph questions): R9-R15

Answers to `p2-graph`'s implementation questions, appended to
`phase2-amendments.md` as R9-R15. Each refines a numbered group above
rather than replacing it.

- **R9 (refines §9, D13 — obligation retirement plumbing).** `p2-store`
  adds an obligations-by-source lookup; `internal/graph` retires every
  obligation version bound to a replaced source through that lookup,
  bounded per §13/D17's resource limits (never an unbounded scan);
  `internal/ingest` creates a replacement UNRESOLVED obligation version
  only when the new source itself declares `obligation=` (unchanged from
  D13, now with the lookup that makes retirement itself efficient and
  bounded).
- **R10 (refines §17, M6/R6 — transitional namespace fail-closed).** Until
  `internal/store` actually keys current versions by the typed
  DIRECTIVE/AGENT_KEY namespace (R6's forward migration),
  `internal/graph` fails closed with a new `ErrNamespaceConflict` rather
  than guessing a namespace from ID shape — a transitional safety rule for
  any code that runs against the pre-migration schema during the phase.
- **R11 (refines §7, D10 — `SameDirectiveSemantics` comparison basis).**
  D10's duplicate-vs-current comparison is implemented as
  `SameDirectiveSemantics`, comparing (in addition to D10's listed fields)
  creation turn, TTL origin, and obligation declaration once each is
  available on the item — the same eligibility-origin fields D10 already
  required, now named to one function so `p2-graph` and `p2-ingest`
  implement one comparison, not two.
- **R12 (refines §7, D11 — duplicate-snapshot partition key).** A Working
  snapshot's duplicate-vs-changed decision (D11) is made per `(task,
  authority, boundary)` partition — the same partition D11 already uses to
  freeze the prior current set — confirming FR-DIR-007's
  same-authority-and-boundary supersession scope applies to duplicate
  detection too, not only to replacement.
- **R13 (refines §7 and §11 — per-item vs. whole-event failure).** A
  same-ID write whose boundary differs from a visible current version
  (explicit-ID or derived-ID) rejects only *that item*, with a
  `boundary_conflict` diagnostic, and writes nothing for it; the rest of
  the event proceeds — except that a Working section containing such an
  item is a partially malformed section under D11 (§7), so it performs no
  snapshot replacement at all. This is narrower than D1/R7's event-wide
  abort: authorization, integrity, ownership, idempotency, and
  resource-limit failures still abort the whole event (§1, §11, §13); a
  same-ID boundary conflict on one item among several does not.
- **R14 (refines §1, D1/R7 — target-state mismatch is a diagnostic).** A
  lifecycle command whose target exists and is authorized but is in the
  wrong state for the action — Resolve on a non-OPEN goal, Unpin on a
  non-pinned target — is a diagnostic, not an event abort, matching how D1
  already treats an unknown/inaccessible/ambiguous target (R7 only makes
  an *unauthorized source actor* abort the event).
- **R15 (process note, §17).** Phase 1 test fixtures that needed updating
  for R6's namespace contract (e.g. a fixture constructing a
  current-version key without a namespace) are accepted as the contract
  change itself, not as regressions to preserve — `p2-graph`/`p2-store`
  update them in place rather than special-casing the pre-namespace shape.

### 21. Round 3 ruling (p2-contract questions): R16

Answers to `p2-contract`'s implementation questions, appended to
`phase2-amendments.md` as R16 (all accepted as recommended).

- **Parser output is not self-certifying (refines §2, D2/M2).**
  `internal/ingest` always re-checks a parsed directive against
  `policy.ForDirective` before classification takes effect — the parser's
  acceptance is necessary but not sufficient, since `internal/directive`
  cannot itself apply FR-DIR-003's defaults or FR-DIR-006's attribute
  allowlist. `p2-tests` adds a cross-check: every directive
  `internal/directive` accepts must also be accepted by
  `policy.ForDirective`, so the two packages' notions of "valid directive"
  cannot silently diverge.
- **A span cannot outrank its event kind (refines §11, D15).** An event's
  own kind/authority envelope bounds every span it carries; a harness that
  needs spans at genuinely different authorities sends separate events,
  one per authority, rather than one event whose kind understates a
  contained span's authority. This closes a variant of D15's
  confused-deputy concern one level up: authority is bounded by the event
  as a whole, not asserted per span inside a single envelope.
- **Caller `EventID` representation (refines §10, D14).** A
  caller-supplied `EventID` is printable ASCII, at most 256 bytes — a
  syntax bound on the idempotency key itself, distinct from D14's
  payload-hash/receipt semantics.
- **HARNESS residual instructions are not mandatory (refines §5, D8;
  wording corrected by SPEC-1.5).** FR-DOM-007's mandatory-by-policy list
  names SYSTEM-authority instructions only; a HARNESS-authority residual
  instruction item (`policy.ForResidual`, not the transcript item — §5's
  transcript defaults are the separate `conversation`/WORKING/NORMAL row)
  is eligible for scoring like any other non-mandatory item, never
  automatically mandatory merely because it is HARNESS. `ForResidual`
  confirms this precisely: only the SYSTEM row sets `Mandatory = true`.
- **Parser item ranges become `SourceRange.Slices` (refines §4/§5,
  D7/M1).** The parser's `Item.TextRanges` (D7's per-item byte ranges)
  populate `SourceRef.Slices` on the derived item (§5's transcript-range
  mapping) — one field name across the parser/domain boundary, so
  `internal/directive` and `internal/domain` are not each inventing their
  own range representation.
- **Anonymous occurrence IDs (confirms §10, M3).** Reconfirms M3: an event
  without a caller `EventID` gets its internal occurrence ID generated
  once, outside any retried transaction callback — R16 raised no change
  here, only confirmed it against a `p2-contract` question.

### 22. Round 4 rulings: R17 (p2-parser questions) and R18 (p2-store questions)

**R17 (p2-parser questions, all accepted):**

- **Validation ownership (refines §2/§21, D2/M2/R16).** `internal/directive`
  owns syntax-level attribute validation and executability (an attribute
  the grammar disallows, or a malformed value, is already rejected at
  parse time); `policy.ForDirective` (§2) remains a fail-closed,
  defense-in-depth re-check in `internal/ingest`, not the primary
  validator — R16's cross-check (a directive `internal/directive` accepts
  must also be `policy.ForDirective`-accepted) is how the two are kept
  from silently drifting. `p2-contract`'s work group 3 already closed the
  case-folding/TTL divergence between the two packages that made this
  cross-check necessary in the first place.
- **Diagnostic-cap application order (refines §13, D17).**
  `internal/ingest` merges a span's parse units in part order before
  applying D17's per-span cap: 256 diagnostics plus one truncation marker
  are (re-)applied per span across its merged parts, then the whole-event
  total cap (§13's finite versioned limit) is applied over every span's
  merged, already-capped diagnostics — a single well-defined two-stage
  cap, not an ambiguous interaction between per-part, per-span, and
  per-event limits.
- **Stricter-than-CommonMark choices kept (refines §4, D5/D6).**
  Confirmed, not changed: a fence opens on any 3-or-more identical
  backtick/tilde run (D5's "≥3" is exact, not a minimum CommonMark also
  relaxes elsewhere); a heading line with a trailing closing sequence of
  `#` characters (CommonMark's optional "closing ATX hashes") is malformed
  here, not stripped and accepted; a Setext-style heading (text underlined
  with `=`/`-`) never closes or opens a section, because D6 already
  excludes it from being a directive heading at all — these are recorded
  as parser-v1's deliberate, narrower-than-CommonMark grammar, not gaps to
  widen later without an explicit ADR change. **Two further deviations
  from a literal reading of FR-DIR-006's ABNF, recorded here (SPEC-2.7,
  previously only in `internal/directive/doc.go`):** trailing ASCII
  SP/HTAB bytes after a heading's metadata are tolerated even though the
  ABNF's heading production ends with `*(SP attr) EOL` and carries no
  trailing-whitespace clause (`internal/directive/doc.go`'s "Headings"
  section; locked by `TestIDAndAttributeLexing`); and D20's derived-ID
  shape check (§6) applies only to the six content-section keywords that
  actually derive IDs — `resolve-<64hex>`/`unpin-<64hex>` are ordinary
  explicit IDs on a lifecycle command, not derived-ID-shaped rejections,
  since Resolve/Unpin never derive an ID to collide with
  (`internal/directive/items.go`'s `derivedShaped`, `d20-derived-shaped-ids`
  golden).
- **Fail-closed surprises kept (refines §4/§8, D7/M4/D12/R1).** Confirmed:
  two list items sharing one explicit ID within a section are *both*
  dropped (not "first wins, second diagnosed") — M4's "repeated directive
  IDs within a single list are malformed and no member under that
  repeated ID is applied" means neither survives; a fence-opening or
  block-quote marker at column 0 inside what is otherwise a list body
  makes that whole section malformed rather than being tolerated as list
  content; an out-of-range `ttl` (R1's 1..2147483647 bound) is fatal to
  the event, not a per-attribute diagnostic that leaves the item live with
  no TTL.
- **Parser version pinning (new, refines §3/§12/§13; clarified, SPEC-2.7).**
  The parser version this ADR's records reference throughout (recorded
  with every item, diagnostic, and receipt) is `directive/v1`, defined as
  exactly the behavior this ADR and `internal/directive/doc.go` document —
  concretely, `directive/v1` is pinned to the grammar **as of the merged
  integration head**, not as of this ADR's original text: the SPEC-1.1
  fix (a column-0 fence/quote/comment line dropping every item and target
  of its section, §26) and the SPEC-1.11 ruling (heading-shaped lines
  close but never open a section, §27) both changed accepted grammar
  behavior after `directive/v1` was first defined, without bumping the
  version string. R17 says a grammar change is a new version string; this
  is deliberately not one, because no `directive/v1`-versioned record has
  ever been persisted outside this development branch — there is no V1
  release yet whose replay guarantee a silent grammar change could break.
  A future grammar change *after* release must bump the version string; a
  pre-release fix to match this ADR's own intended grammar does not.

**R18 (p2-store questions, all accepted):**

- **Retiring the untyped namespace methods (refines §17, M6/R6/R10 —
  landed, SPEC-1.4).** `p2-store` landed the typed methods
  `store.CurrentVersion(domain.CurrentKey)` and
  `store.CurrentVersions(taskID, ns, id)`; the pre-M6
  `CurrentDirective`/`CurrentDirectives`/`SetCurrentDirective` methods were
  deleted once every caller switched (`55761b8`; `internal/graph`'s call
  sites now use `CurrentVersion`/`CurrentVersions` exclusively,
  `0a99ac7`). R10's `ErrNamespaceConflict` transitional fail-closed rule
  was never actually implemented, since the switch itself landed directly
  — there was no gap for a transitional rule to cover, and no such symbol
  exists in the codebase; this ADR's Tests section no longer requires one
  (SPEC-1.4 correction, below).
- **Obligation retirement lookup, named (confirms §9/§20, D13/R9).**
  `store.ObligationsBySource(sourceItemID string, limit int)
  ([]domain.ObligationVersion, error)` and
  `store.RetireObligationVersion(obligationID string, version,
  expectedRevision uint64, event domain.LifecycleEvent)
  (domain.ObligationVersion, error)` are the landed methods
  (`internal/store/memory`, `internal/store/sqlite`) R9 called for — the
  `limit` parameter is D17's bounded-scan requirement made concrete.
- **`domain.UnresolvedReference` and migration 0008 (refines §16, M5/R2 —
  landed, SPEC-1.4).** `domain.UnresolvedReference` (`internal/domain
  /reference.go`) carries session, occurrence, span, and item IDs; lexical
  locator key and rule version; owner access boundary and authority;
  `Seq`; an ID derived from occurrence plus ordinal. Migration
  `0008_unresolved_references.sql` persists it as `rec_reference`, so an
  unresolved References item survives restart for later linking (§16's
  "unresolved references persist" requirement). **(SPEC-2.2, current state,
  not 0008's original)** 0008's own `reference_locator` index on
  `(session_id, locator_key, rule_version, seq, id)` was dropped by
  migration 0013 once superseded; the index the table is actually read
  through today is 0012's owner-column-qualified `reference_visible`
  (`internal/store/sqlite/access_lookups.go`, §26).
- **`domain.TTLLive`'s zero-creation-turn guard (refines §19, M8 —
  landed, SPEC-1.4).** `TTLLive(created, current uint64, n int) bool`
  (`internal/domain/item.go`) now reads `n > 0 && created > 0 && current
  >= created && current-created < uint64(n)`: a pre-Phase-2 item with no
  recorded creation turn (`created == 0`) reports `false` regardless of
  `current`/`n`, so a record migrated without turn metadata is never
  treated as live by an invented default (M8) merely because the
  difference formula would otherwise fall inside `n`.
- **Bounded diagnostic/lifecycle-command list reads (refines §12/§1 —
  deferred).** A future cross-event caller reading diagnostics or
  lifecycle commands by list (rather than by one event's receipt) needs a
  `Limit` parameter and an `ErrLimitExceeded` result, matching D17's
  bounded-scan discipline elsewhere; R18 records this as accepted but
  explicitly deferred — no such cross-event caller exists yet in Phase 2's
  scope to require it.

### 23. Round 5 ruling (p2-ingest questions): R19

Answers to `p2-ingest`'s implementation questions, appended to
`phase2-amendments.md` as R19.

- **`SourceRanges` is the transcript-to-semantic mapping, with no separate
  section record (confirms §5, D8/M1).** `ContextItem.SourceRanges
  []SourceRange` (`internal/domain/item.go`, `source_range.go`), already
  landed, *is* §5's "complete mapping from transcript ranges to derived
  item IDs/versions and section IDs" — each derived item names its own
  transcript coverage directly on the item, and there is no additional
  standalone section-record type to keep in sync with it.
- **`IngestReceipt.Items` is the record of a turn's opening items
  (confirms §10/§14, D14/D18).** `IngestReceipt.Items` (creation order),
  read together with the same receipt's `OpenedTurn`/`TurnID` (set only
  when the event actually advanced the turn), is the SDD's "turn-opening
  message" and its pending-input items — no separate pending-input
  structure is needed; the receipt already carries it.
- **Indexed lookups, not session-wide scans — superseded by F1's
  access-filtered redesign (refines §7/§13/§15/§16, D10/D17/D19/M5 —
  landed, SPEC-1.4; see §26 for the full F1 record).** `p2-store` first
  added bounded-but-session-wide indexed lookups for blob reference
  (migration 0009), duplicate candidates (migration 0010), and reference
  matching (migration 0008), plus a fourth for source-key matching
  (migration 0011) — each properly indexed, matching D17/NFR's
  bounded-work discipline, but each still returning every matching row in
  the session regardless of whether the calling principal could see it.
  The first external review's F1/SEC-1.1/SEC-1.2 findings (§26) caught
  this gap and required an access-filtered redesign, which superseded all
  four: `store.BlobReferrer`, `CanonicalCandidates`, `CurrentWorking`,
  `SourceItems`, and `VisibleReferences` (migration 0012, with 0009-0011's
  tables dropped by 0013) filter inside the query by the caller's
  `Viewer`, before any limit, and separately report a visible match whose
  content fails verification without blocking on it (DUR-1.4). ADR 3
  records the full migration sequence and every locking test. SPEC-1.3
  also found that *other* graph/ingest reads issued once per ingested item
  or section — `Relationships`/`Items` filtered by type/task — were not
  indexed even though the named lookups were. **Migration 0012's own
  `relationship_to`/item-task indexes did not actually close this
  (SPEC-2.1): they carried the key but not the `(Seq, ID)` order, so
  SQLite still preferred a session-wide order index over them to avoid
  sorting, and each read still grew with the session — `migration 0015`
  (`0015_ordered_graph_indexes.sql`, p2-store) fixes it properly with
  composite key-plus-order indexes, and a strengthened `assertIndexed`
  plan guard (`TestGraphReadsUseIndex`) now requires the exact key columns
  in the search constraint, catching the session-prefix-only plan the
  original guard missed.** `tx.Grants()`, `ObligationTransitions`,
  `LifecycleEvents`, `Obligations(taskID)`, and
  `Diagnostics`/`LifecycleCommands` with no `OccurrenceID` remain
  unfiltered whole-session reads, deliberately deferred. **(SPEC-3.3,
  corrected) `tx.Grants()` is not off the ingest path** — it runs once per
  replaced item that carries an obligation
  (`internal/ingest/directives.go:130` → `graph.go:222` →
  `graph/obligation.go:43`) and once per lifecycle command
  (`derive.go:357` → `graph/lifecycle.go:122`) — so it is deferred as a
  performance-only item precisely *despite* running on that path, not
  because it doesn't (§13 records the full ruling: a grant can only be
  created by an authorized issuer, so the read discloses nothing and only
  costs time, never correctness). `ObligationTransitions`, `LifecycleEvents`,
  `Obligations(taskID)`, and occurrence-less `Diagnostics`/`LifecycleCommands`
  genuinely are off the ingest path.
- **Locator identity has no repository namespace in V1 (resolves the §16
  open question, M5/R2).** Sharpens this ADR's earlier "References
  base-directory policy — resolved, deferred" open-question answer with
  the exact rule: V1's lexical locator identity has no repository/
  namespace component at all, not merely an unconfigured default: a
  locator is matched on its lexical form alone within the session, and
  multi-repository disambiguation is out of scope for V1 rather than a
  parameter this phase leaves at a default value.
- **TOOL/RETRIEVED_CONTENT evidence before the first turn is rejected
  (refines §14, D18).** A TOOL or RETRIEVED_CONTENT event or span arriving
  for a task before any turn has opened (`Turn=0`, no `TurnID`) is
  rejected outright — D18 already forbids creating a TURN-bound or
  TTL-dependent item without a valid owning turn, and every TOOL/
  RETRIEVED_CONTENT transcript item is EPHEMERAL/TURN-scoped by default
  (§5, D8), so it always needs one; R19 makes the rejection explicit
  rather than leaving it as a consequence to rediscover from D8+D18.
- **Diagnostic code paired with each R13/R14 reason (new pinning
  decision, refines §7/§1; SPEC-2.2: updated to the landed, current
  state).** `DiagnosticCode` was a closed seven-value set when this
  pairing was first proposed; it is now nine
  (`ErrUnsupportedDirective`, `ErrMalformedDirective`,
  `ErrAmbiguousDirective`, `DirectiveNotParsed`, `DiagnosticsTruncated`,
  `DirectiveIDDerived`, `DiagnosticNotFound`, `ItemUnverified`,
  `ReferenceLinksTruncated` — `internal/domain/diagnostic.go`), the last
  two added by DUR-1.4 and the SEC-1.4/§28 `MaxReferenceLinks` ruling
  respectively. R13/R14's new `DiagnosticReason`s must reuse an existing
  code; this is no longer merely a convention pinned by this ADR's prose
  but mechanically enforced — `Diagnostic.Validate` checks every reason
  against a `reasonCode` map (`internal/domain/diagnostic.go:145-151`) and
  fails if a diagnostic's `Code` doesn't match its `Reason`'s required one
  (locked by `TestIngestionReasonCodePairing`). This ADR pins
  the pairing so `p2-graph`/`p2-ingest` do not each invent one
  independently: R13's per-item `boundary_conflict` (§7, D10) pairs with
  `ErrMalformedDirective` — the item is dropped as non-executable content,
  the same category D7/M4 already use for an invalid or repeated ID;
  R14's `target_mismatch` (§1, D1) pairs with `DiagnosticNotFound`
  (`"ErrNotFound"`) on the diagnostic accompanying the
  `LifecycleCommandRecord`, alongside `Resolution: TargetMismatch` on the
  record itself — a target in the wrong state for its action is, from the
  caller's perspective, not currently a valid target for that action, the
  same outcome class as `TargetNotFound`, distinguished only by the
  `Reason` token, never by a different `Code`. **Added at Phase 3 (SPEC-1.3,
  PR #6 round 1; SPEC-2.13, this pair was missing from the pinned list):**
  `ReasonUnknownIdentity` ("unknown_identity") pairs with
  `ErrUnsupportedDirective` — an identical restatement of a version whose
  creation identity is unknown (a pre-upgrade item migration 0034 could not
  reconcile, ADR 3's amendment) is neither a duplicate nor an authorized
  replacement, so the line is dropped exactly as an unsupported lifecycle
  word would be, never silently accepted or promoted to a hard event abort.
**The unknown-identity limitation is not unique to plain directive lines; it
follows every caller of `SameDirective`/`knownDeclaration`, with a different
failure shape per caller (SPEC-2.9, PR #6 round 2, residual of SPEC-1.3).**
An attribute-only change to an unknown-identity directive (e.g. adding
`{obligation=…}` to otherwise identical text) still produces
`unknown_identity` and never lands as a replacement, because
`graph.knownDeclaration` fails closed whenever the prior declaration is
unknown, regardless of what changed — and the explicit
`lifecycle.ReplaceDirective` refuses the same source for the same reason, so
there is currently no authorized way to recommission such a directive short
of changing its text. The same `knownDeclaration` call is reached by two
other callers, each with its own, worse failure shape, both still open:
**Working snapshots (SPEC-2.9's first bullet)** — `isDuplicateSnapshot`
(`internal/graph/snapshot.go:336-345`) requires every member of an
identical-looking Working snapshot to pass `SameDirective` against its
prior row; an unknown-identity member fails the whole snapshot with
`ErrUnknownDeclaration`, aborting the *entire ingest event*, not dropping
one line — worse than a directive line's clean per-line diagnostic, for a
case that is otherwise an ordinary duplicate. **Tool-written agent keys
(SPEC-2.10)** — `internal/tools/keyed.go:94` also calls `SameDirective`
before recording a keyed write as a duplicate; an unknown-identity current
key returns a tool-level error instead of deduplicating, contradicting G5's
"so identical restatement dedups" ruling. None of these three callers
currently achieves G5's intended outcome for an unknown-identity target
except the directive-line case, and only by degrading to a diagnostic
rather than a successful dedup. SPEC-2.9's suggested fix — a pre-check in
`workingSection` emitting the same per-line diagnostic instead of aborting
— and SPEC-2.10's — reconciling agent keys the way 0034 reconciles
directives, or documenting the exception — are both open, assigned to
future work, not this pass.



### 24. Round 6 ruling (ingest-suite findings): R20

Findings from `p2-ingest`'s own test suite, appended to
`phase2-amendments.md` as R20.

- **Reserved internal ID prefixes on caller `EventID` (refines §10/§11,
  D14/D15/R16 — landed, SPEC-1.4).** `Event.Validate`
  (`internal/domain/ingest.go:128`) now also calls `ReservedIDPrefix`: a
  caller-supplied `EventID` equal to or prefixed like an internally
  generated occurrence or artifact ID — `evc_`, `eva_`, any `IDDomain`
  prefix (`dgn`, `cmd`, `sec`, `ref`), `itm_`, `call_`, `turn_`, `obl_`,
  `rel_`, `evt_`, or `lce_` (SPEC-1.13 added the last) — is rejected,
  locked by `TestEventIDRejectsReservedPrefixes`. "Ingest errors never
  echo item IDs" is likewise confirmed true: `internal/ingest`'s only
  formatted error (`internal/ingest/ids.go`) names a limit, never an ID.
- **`DirectiveIDDerived` notices for a refused section (refines §6/§10,
  D9/D14 — landed, SPEC-1.4).** `internal/directive` emits an
  informational `DirectiveIDDerived` diagnostic for every item it derives
  an ID for, before `internal/ingest` decides whether that item's section
  is actually applied. `internal/ingest`'s unit loop now filters this: for
  `d.Code == domain.DirectiveIDDerived`, `item, ok := r.written[d.Range];
  if !ok { continue }` (**SPEC-3.6: corrected — `r.written` is
  `map[domain.ByteRange]domain.AccessBoundary`, so the earlier bare
  `!r.written[d.Range]` boolean negation no longer type-checks against the
  current field; this quote was fixed only in the Tests section before**)
  — a derived-ID notice is reported only for a range ingestion actually
  wrote, so a section ingest refuses (a Working section dropped whole on a
  boundary conflict, per the D11 item below) never leaves a
  `DirectiveIDDerived` notice for an item that does not exist.
- **Residual instructions: no BOM/whitespace-only items, lossless bytes,
  and malformed trusted headings still produce residue (refines §5, D8 —
  landed, SPEC-1.4).** `internal/ingest/derive.go`'s pre-ruling
  `residualSlices`/`residualInstruction` were replaced by `residue`
  (called last from `applyUnit`, after every directive item and lifecycle
  command in the unit, per R21's amended M3 order below): a SYSTEM/HARNESS
  unit's residue is now every byte not claimed by an accepted section or a
  written item — computed from the parser's own `Section` extents, never
  re-scanned — so a malformed or refused trusted section's bytes join
  residue instead of vanishing, exactly as R20 required. The function's
  own doc comment states this plainly: "text inside a malformed or refused
  trusted section becomes instruction text rather than silently
  vanishing." Bytes are kept exactly (no separate trim-for-content step,
  closing the lossless-bytes gap this ADR previously flagged), and "a
  residue of only ASCII whitespace and a leading BOM creates no item"
  closes the BOM-only gap. What remains from a naive "any malformed
  trusted section becomes residual" reading — wrongly turning a malformed
  *lifecycle* section (Resolve, Unpin, or an unsupported word) into a
  residual instruction, which would surface a runtime command's text as
  trusted instruction content — is `residue`'s own explicit exclusion:
  "Lifecycle commands ... are never shown to the model as trusted
  instructions (R21)." §25 records that carve-out and R21's other two
  refinements in full; all three are landed together in the same
  function.
- **A malformed Working section writes no members, stricter than D11's
  literal text (confirms §7, D11 — landed).** `internal/ingest/derive.go
  :workingSection` already returns immediately, creating no items and no
  snapshot edges at all, when `sec.Malformed || len(sec.ItemIndexes) ==
  0` — sharper than D11's "preserves all raw bytes and diagnostics but
  performs no snapshot replacement," which read as leaving open whether a
  malformed section's well-formed members might still be created as
  freestanding (non-snapshot) items. R20 confirms the stricter reading is
  correct and records it here: a malformed Working section produces no
  items whatsoever, only diagnostics, so a parse error can never partially
  apply a snapshot. The same function also confirms this ADR's R19
  code/reason pinning exactly: a boundary conflict on any member aborts
  the whole section's write with `Code: ErrMalformedDirective, Reason:
  ReasonBoundaryConflict` (§23), not a per-member drop, because a Working
  snapshot is one atomic operation (D11).

### 25. Round 7 ruling: R21 (landed together with R20, SPEC-1.4)

Refines §24/R20's residual-instruction fix, plus a creation-order
amendment to M3. All three points below are implemented in the single
landed `internal/ingest/derive.go:residue` function (§24).

- **A residue that is only a bare heading line creates no item (refines
  §5/§24, D8/R20).** An empty malformed section — a heading with no body
  at all — would otherwise become a residual instruction whose entire
  content is the bare heading line itself. R21 narrows R20: `residue`'s
  `case blankBytes(c.text, s.BodyRange):` treats such a section's `Range`
  as claimed (excluded from residual) directly, so no residual instruction
  item is ever created for it — a heading alone conveys no instruction
  content worth preserving. A malformed section that *does* have body text
  is unaffected and still joins residual under R20's core fix.
- **Malformed lifecycle sections are never residual instructions (refines
  §5/§24 and §1/§9, D1/D8/R20 — a scoping limit on R20, not a gap).** R20's
  fix (§24) is scoped to malformed *content* sections (Goal, Pinned,
  Working, Remember, References, Ephemeral); it does not extend to a
  malformed Resolve, Unpin, or unsupported-lifecycle-word section (M4's
  closed vocabulary, §4). `residue`'s first case —
  `s.Status == directive.SectionUnsupported || s.Keyword == directive.Resolve
  || s.Keyword == directive.Unpin` — claims (excludes from residual) every
  lifecycle section regardless of whether it is malformed, so a malformed
  lifecycle heading in a trusted span stays transcript-only, with its
  diagnostic, exactly as before R20. The reason is D1's core guarantee: a
  lifecycle command is `PARSED_NOT_EXECUTED` and must never be rendered as
  a trusted instruction to the model; if a malformed
  `## Resolve`/`## Unpin`/unsupported-word heading became residual
  instruction text, a runtime command that failed to parse would reappear
  as ordinary trusted prose, which is worse than the diagnostic-only
  status quo it would replace. This reasoning is about a *malformed*
  command specifically and is unaffected by Phase 3's P3-35 execution of
  well-formed ones (§1's note above): a malformed lifecycle heading never
  executes in either version, so it must never be rendered as instruction
  text in either version.
- **Creation order: a unit's residual instruction item is created after
  its directive items, amending M3 (refines §10).** M3 (§10) originally
  ordered a unit's items as "transcript items first, then residual/
  directive items in source order" — treating residual and directive items
  as one byte-position-ordered group. R21 amends this: a unit's residual
  content, and thus its residual instruction item, can only be computed
  *after* every section and lifecycle command in the unit has been
  resolved, because whether a given section's bytes end up in residual
  depends on whether ingest ultimately refused that section (R20) and on
  that section's keyword (R21, above) — information that does not exist
  until the whole unit has been processed. `applyUnit` no longer sorts a
  residual step into its position-ordered `steps` list at all: the steps
  loop covers only directive items, Working sections, and lifecycle
  commands, and the function ends with an unconditional `return
  r.residue(c)` — residue always runs last for its unit, never ordered by
  byte position. This is the amended creation-order M3 (§10) now records:
  transcript item, then the unit's directive items and lifecycle commands
  in source order, then finally its residual instruction item, if any.

### 26. PR #5 review round 1: commander rulings F1-F6 (all landed, final pass)

The first external review of this PR (`round1-p5-fixes.md`) produced
commander rulings F1-F6. All six are now landed and reconciled against
the code at this ADR's final-pass head.

- **F1 (SEC-1.1, SEC-1.2, DUR-1.1, SPEC-1.3 — access-filtered lookups,
  refines §7/§9/§13/§15/§16, D10/D17/D19/M5/R19; full record, ADR 3 owns
  the migrations).** The original R19 lookups (§23) were properly indexed
  but session-wide: they returned every matching row regardless of
  whether the calling principal could see it (SEC-1.1's existence
  oracle/cross-task-lockout), and `SupersedeSnapshot`'s `freezePartitions`
  still ran a full per-task item scan (SEC-1.2). F1 redesigned every
  lookup as access-filtered from the start: `internal/store/lookups.go`
  defines `Cursor`, `Page`, and `Lookup{Items, Unverified, More, Next}`,
  and five `Validate()`-checked filters — `BlobReferrerFilter`,
  `CanonicalFilter`, `WorkingFilter`, `SourceFilter`,
  `VisibleReferenceFilter` — each carrying a `Viewer domain.Principal`.
  The `ReadTx` methods `BlobReferrer`, `CanonicalCandidates`,
  `CurrentWorking`, `SourceItems`, and `VisibleReferences`
  (`store.go:186-197`) replace the deleted `ItemsByBlob`/
  `DuplicateCandidates`/`ItemsBySourceKey`/`UnresolvedReferences` and the
  `freezePartitions` task scan; SQLite filters by owner columns on new
  tables (migration 0012's `lookup_blob`/`lookup_canonical`/
  `lookup_working`/`lookup_source`, ADR 3) inside the query, before any
  limit, so an inaccessible row is never counted, returned, or
  observable — closing SEC-1.1 — and `CanonicalFilter`/`WorkingFilter`
  fail `ErrLimitExceeded` only on *visible* overflow, so
  `SupersedeSnapshot` now does one indexed `CurrentWorking` lookup per
  partition instead of a task scan (`internal/graph/snapshot.go:279`),
  closing SEC-1.2. `lookup_canonical`/`lookup_working`/`lookup_source` hold
  live items only (excluded by subquery once superseded or classified
  `DUPLICATE_OF`), and the store deletes an item's rows from these three in
  the same write that retires it, so repeated identical content never
  grows them (DUR-1.1). **(SPEC-3.3, corrected) `lookup_blob` is not the
  same:** `retireLookups(itemID, duplicate bool)`
  (`internal/store/sqlite/access_lookups.go:56-60`) removes its own rows
  only when the retiring item is itself classified a duplicate
  (`duplicate == true`) — a superseded (non-duplicate) item's `lookup_blob`
  row is deliberately kept, since the canonical item's exact content and
  boundary authorize the same blob references its superseded predecessor
  did (the code's own comment); and migration 0012's `lookup_blob` backfill
  has no `DUPLICATE_OF` exclusion at all (unlike the other three tables'
  backfills), so a duplicate item's blob row that predates 0012 is never
  removed retroactively. Ingest call
  sites: `internal/ingest/directives.go:269` (`CanonicalCandidates`),
  `references.go:42` (`SourceItems`), `references.go:137`
  (`VisibleReferences`), `run.go:306` (`BlobReferrer`). Separately,
  SPEC-1.3 found that `internal/ingest`'s *other* per-item reads —
  `Relationships` filtered by type/from/to, `Items` filtered by task — were
  not indexed even though the three named R19 lookups were; the key-only
  indexes behind them — `relationship_to` (migration 0012) and
  `relationship_from`/`item_task` (migration 0001, **not 0012 — SPEC-3.3
  corrects the provenance**) — did not close this on their own (SPEC-2.1:
  no `(Seq, ID)` order, so SQLite still sorted a session-wide index
  instead) — migration 0015's composite key-plus-order indexes do.
  Tests:
  `TestAccessLookupsUseIndex`, `TestUpgradeAccessLookups`,
  `TestGraphReadsUseIndex`, `TestLegacyLookupsDropped`
  (`internal/store/sqlite/access_lookups_test.go`,
  `lookups_test.go`); storetest conformance rows `BlobReferrerAccess`,
  `CanonicalCandidates`, `CurrentWorking`, `SourceItems`,
  `VisibleReferences` (`internal/store/storetest/access_lookups.go`,
  registered `storetest.go:78-82`); ingest-level
  `TestLookupsNeverLockOut_F1` (`internal/ingest/lookups_test.go`).
  DUR-1.4's unverified-item handling rides the same redesign: a visible
  match whose stored content fails verification (a legacy row `0001`
  altered before the lossless fix, D3/R8) is excluded from `Lookup.Items`
  and named in `Lookup.Unverified` instead of blocking the whole lookup or
  hiding behind unrelated identical content; `internal/ingest/run.go`'s
  `reportUnverified` records one content-free `domain.ItemUnverified`/
  `ReasonUnverifiedItem` diagnostic per unverified ID, and reading the row
  directly still fails `domain.ErrIntegrity`. Tests:
  `TestLegacyUnverifiedNeverBlocks` (SQLite),
  `TestUnverifiedMatchesNeverBlock_DUR14` (`internal/ingest/lookups_test.go`).
- **F3 (SPEC-1.7, DUR-1.2 — retry precedes limit/policy checks, refines
  §10, D14; landed, confirming the mechanism §26's earlier draft
  described correctly).** `internal/ingest/ingest.go`'s `apply()` runs the
  idempotency lookup (`lookupReceipt`) before the second, limits-carrying
  `e.ValidateFor(p, limits)` call — "Only a new occurrence is held to the
  currently configured limits" (the function's own comment) — so a
  retry of a known `EventID` always replays the stored receipt verbatim,
  even when limits or policy versions changed since the original event;
  only a genuinely new event sees the new configuration. Test:
  `TestRetryAfterLimitsChange_F3` (`internal/ingest/fixes_r1_test.go`).
- **F4 (SPEC-1.2 — References-by-item-ID, refines §16, M5/R2/FR-DIR-003;
  landed).** FR-DIR-003's "a name that matches an item ID in scope … gets
  a REFERENCES edge" is now implemented: a References entry whose text is
  a valid item ID resolves it the same way any other accessible source
  resolves (`internal/ingest/references.go`), and links it, keeping an
  inaccessible or missing item ID indistinguishable — the same rule §16
  already specified for path/URL locators. Test: `TestReferencesByItemID_F4`
  (`internal/ingest/references_test.go`, SPEC-3.6: corrected — a
  separately named test does exist and §16 already cites it, contradicting
  this paragraph's earlier claim that it doesn't).
- **F6 (SEC-1.3 — diagnostic/lifecycle-command record access, refines
  §12/§1, D16/D1; diagnostic half landed; command half superseded by
  SEC-3.2, §31 below — SPEC-4.2 corrects this paragraph, which had drifted
  out of sync with the code).** A diagnostic's access is the *narrower* of
  the source span's boundary and the boundary of the content it describes,
  not the span boundary alone: diagnostics carry a `scopedDiag{d, access}`
  (`internal/ingest/diagnostics.go`) computed per diagnostic rather than
  defaulting to the span's transcript access, using
  `domain.Intersect(scope, spanAccess, targetAccess)`
  (`internal/domain/principal.go:101`, `internal/ingest/derive.go:297`)
  when the diagnostic's cause resolves to a narrower-boundary item. This
  closes the over-disclosure SEC-1.3 found: a span-boundary-only access
  field could let a principal who can see the span, but not a
  narrower-boundary target it describes, learn the target exists.
  **A lifecycle-command record does not narrow its own `Access` this way**
  — `rec.Access` stays at the transcript boundary regardless of outcome
  (`internal/ingest/derive.go:351`, `rec.Access = c.transcript.Access`); §31
  below records SEC-3.2, the mechanism that actually replaced F6's original
  per-command narrowing (a separate `DetailAccess` field and redaction,
  not an `Access` intersection). Reads filter on the narrowed
  boundary like any other access-checked record. Test:
  `TestRecordsAtNarrowerBoundary_F6` (`internal/ingest/fixes_r1_test.go`).
- **F5 (DUR-1.8, SPEC-1.9 — migration 0011's Go-step checksum) is recorded
  in ADR 3's Phase 2 migrations section**, not repeated here: migration
  0011's backfill is now a frozen, private copy of the locator rule with
  its identity folded into the migration's checksum, so it is no longer
  outside checksum protection and no longer depends on live domain code.

- **F2 (SEC-1.5 — caller `EventID` namespace, refines §10/§11, D14/D15).**
  `CallerOccurrenceID(session, eventID)` ignores the principal, so a
  session-wide `EventID` collision across tasks/agents/workflows lets one
  principal's guessable ID block another's identical retry and lets an
  attacker probe which IDs another principal has used (reproduced:
  `TestEventIDSquatIsBareConflict_F2` (`internal/ingest/f2_eventid_test.go`,
  SPEC-3.6: corrected from a cited `TestSEC_EventIDCrossPrincipal`, which
  does not exist), a `wf2/T2` principal using `EventID:
  "turn-2"` blocks a later, unrelated `T`-scoped event with the same ID).
  Ruling: mitigation, not redesign — FR-ING-006's session-scoped identity
  is unchanged; a different principal reusing an `EventID` still fails
  `domain.ErrEventIDConflict`
  (`errors.New("event ID conflict")`, `internal/domain/errors.go:24`),
  returned bare with no ID, principal, or session detail
  (`internal/ingest/ingest.go:233,240`) — `p2-contract` verifies this with
  a test. **Accepted residual risk, recorded here:** an `EventID` is a
  session-wide idempotency key, not a per-principal one; a harness that
  lets predictable, cross-principal-guessable `EventID`s reach the runtime
  (sequential counters, fixed strings like `"turn-2"`, anything derived
  from public state) allows one principal to block another's retry using
  the same ID, and to learn *that* another principal has used it, though
  never what payload it carried or that principal's identity beyond
  "someone already used this ID." **Harness guidance (binding on
  integrations, not enforced by the runtime):** generate `EventID`s with
  enough entropy that they cannot be guessed across principals, and
  prefix or otherwise derive them so that two principals never
  legitimately produce the same one by construction (for example,
  `<principal-scoped-namespace>:<random>` chosen by the harness, not the
  runtime). This is deliberately not enforced in `Event.Validate` — R20's
  reserved-prefix check (§24) rejects a caller `EventID` that collides
  with an *internal* generated-ID shape, but a harness's own entropy and
  uniqueness discipline across its principals is outside the runtime's
  authority to verify, and inventing a per-principal namespace now (the
  redesign SEC-1.5 offered as an alternative) was rejected: it would
  change FR-ING-006's session-scoped retry-key contract for every caller
  to close a risk that only materializes when a harness supplies
  low-entropy, cross-principal-predictable IDs against its own guidance.

### 27. PR #5 review round 1 rulings: SPEC-1.10, SPEC-1.11

- **SPEC-1.11 ruling: CommonMark-heading-shaped lines close but never open
  a section (refines §4, D5/D6/R17 — landed).** Parser v1 previously
  treated a bare ATX line (`#` through `######` alone) or a tab-separated
  one (`#\tOther`) as ordinary content: it neither opened nor closed a
  section, and inside a list body counted as stray prose (marking the
  section malformed). SPEC-1.11 flagged this as the one deliberate
  deviation that leaves *more* text inside a section than CommonMark
  would, and asked for a ruling rather than leaving it an open deviation
  forever. **Ruling:** a line matching CommonMark's ATX heading *shape*
  (0-3 leading spaces, 1-6 `#`, then SP, HTAB, or end of line — the same
  shape D5 already requires for a real directive heading, just without a
  valid keyword) closes an open section of the same or higher level
  exactly as a valid directive heading would (D6), but never opens a new
  section, since it has no keyword to open one with. A line that is not
  heading-shaped at all — seven or more `#`, 4+ space or tab indentation
  before the `#`, fenced/quoted/commented, or a bare `#` immediately
  followed by non-space text like `#5` — is unaffected and stays body or
  ordinary content. The parser has no list-container model, so a
  heading-shaped line indented inside a list item also closes its
  section; closing early only drops directive text, never fabricates any.
  **Landed:** `internal/directive/scan.go`'s heading-recognition loop —
  when a candidate line is heading-shaped (`atxShape(b) > 0`) but is not
  itself a valid directive heading (`blocked != ""` or no capable
  keyword) and its shape level is at or above the open section's, the
  section closes (`p.sections[active].end = line.start; active = -1`)
  without ever opening a new one; the code comment cites "SPEC-1.11
  ruling" directly. `internal/directive/doc.go`'s deviation list is
  updated to match. Locked by `TestHeadingShapedLinesClose`
  (`internal/directive/scan_test.go`, replacing the pre-ruling
  `TestBareAndTabATXLines`) and four canonical goldens:
  `testdata/directives/form-heading-shaped-close` (a bare `#\tOther`
  mid-list closes the open Pinned section), `form-heading-shaped-indented`
  (an indented `  ## Notes` inside a list also closes),
  `form-heading-shaped-inert` (4-space/tab indentation, seven `#`, and
  `#5 bolt` are not heading-shaped and stay body text), and
  `form-heading-shaped-never-opens` (the same heading-shaped line closes
  the prior section but never itself opens a new one). **(SPEC-2.7)** This
  round's other two R17 grammar deviations — trailing SP/HTAB tolerance
  after heading metadata, and D20's derived-ID shape check applying only
  to the six content-section keywords — are recorded next to this one in
  §22's "Stricter-than-CommonMark choices kept" bullet, not repeated here.
- **SPEC-1.10 ruling: parser/policy version-bump replay is covered by the
  general verbatim-receipt property; a real upgrade test is deferred
  (refines §10, D14).** SPEC-1.10 listed "replay after a policy/parser-
  version change" among several ADR clauses with no direct test.
  **Ruling:** this is already covered in substance by D14's general
  property that a retry never re-parses or re-classifies, only replays the
  stored receipt verbatim — `TestReplayNeverRecomputes`
  (`internal/ingest/clauses_test.go`) exercises exactly this property
  using a changed `Limits` configuration (which, like a parser or policy
  version bump, changes the ingester's recorded execution `Versions`): a
  retry under the changed configuration returns the original receipt and
  its original `Versions` unchanged, with no new sequence number, while
  the identical request as a genuinely new event does get the new
  configuration. A *literal* parser/policy version bump cannot be
  exercised in-process today, because `directive.ParserVersion` and the
  policy version are compile-time constants, not an injectable seam; a
  dedicated test for that exact case is deferred until the first real
  version bump, when introducing the seam is no longer speculative.

  SPEC-1.10's other missing-test clauses are also landed now, each in
  `internal/ingest/clauses_test.go` unless noted:
  `TestCompletedTaskNeverReactivated` (ADR §14, D18); `TestRetrievedBeforeFirstTurn`
  (R19: RETRIEVED_CONTENT before the first turn, matching the TOOL case
  §23 already covered); `TestToolCallIDCreatesNoEdge` (ADR §16: a TOOL
  span's `ToolCallID` lands in `SourceRef` only); `TestTruncationPersistedAndReplayed`
  (the 256-plus-one diagnostic cap, D16/D17); `TestAmbiguousLifecycleTarget`
  (FR-DIR-005 at the ingest layer, not only graph); `TestDefaultLimitValues`
  (`internal/domain/limits_test.go` — D17's 8 MiB/4096-item defaults);
  `TestReplaceDirective_ObligationFanOut` (`internal/graph/fanout_test.go`
  — R9's large-fan-out obligation retirement); `TestConcurrentSupersession`
  (`internal/ingest/supersession_test.go`, SPEC-3.6: previously uncited,
  SPEC-4.5: "one Working pin" corrected — the fixture uses `## Pinned`) —
  `n` concurrent events each replacing one `## Pinned` directive serialize
  into one supersession chain on both stores: exactly one current pin, one
  `SUPERSEDES` edge per replacement, every old version retired exactly
  once); `TestRetryIdentity_SessionScopedRich`
  (`internal/ingest/retry_test.go` — TEST-1.2's session-scoped rich-event
  retry, alongside the existing `_RichReceipt` case).

### 28. PR #5 review round 1: remaining DUR/SEC/SPEC findings (all landed, final pass)

Findings not folded into F1-F6 (§26) or already-recorded rulings (§27),
each with its own fix and test.

- **DUR-1.3: the poison primitive (refines §10, D14).** `Apply`/`apply`
  (`internal/ingest/ingest.go`) is failure-atomic: once the core has
  started writing, any error calls `tx.Poison(err)`
  (`store.Tx.Poison(err error)`, `store.go:274`) before returning, so
  the caller's `Update` rolls back everything the transaction wrote even
  if the caller ignores the returned error — no partial ingestion result
  can ever commit. `store.Guard` (`internal/store/guard.go`) implements
  it: the first `Poison` call wins, every write method of `TxBase` checks
  the poison first and short-circuits with it, and reads/`NextSeq`/
  `Allocated` keep working. Poisoning never poisons the `EventID` itself —
  nothing committed, so the same `EventID` remains retryable — only the
  in-flight transaction. `store.ErrPoisoned` is the sentinel for
  `Poison(nil)`. Tests: `TestApplyFailureAtomic_DUR13`
  (`internal/ingest/fixes_r1_test.go`); store-level `TestConformance
  /PoisonRollsBack`, `.../PoisonFirstErrorWins`, `.../PoisonBlocksEveryWrite`
  (`internal/store/storetest/poison.go`).
- **DUR-1.5: Working-snapshot derived-ID collisions across authorities —
  the rule actually chosen, not "option A" (refines §7, D11/FR-DIR-007;
  reframed, SPEC-3.5).** A Working member with a *derived* ID (no explicit
  `[id]`) ran the same same-ID replacement path as an explicit-ID member,
  but a derived ID carries no authority
  (`DerivedDirectiveID(section, contentHash)`), so an identical line under
  a different authority either aborted the whole event or partially erased
  the other authority's snapshot set. **The rule this ADR previously
  labeled "option A" does not restrict by-ID replacement to explicit IDs**
  (that would be the literal reading of "option A" as the finding defined
  it) — **the actual rule, stated directly:** explicit-ID members replace
  by ID exactly as before, unaffected by anything below; a *derived*-ID
  collision across authorities is superseded normally when the new item's
  authority is the same as or higher than the current version's
  (`derivedSlotHeldAbove`, via `graph.CurrentVersionFor`, refuses exactly
  when `!it.Authority.AtLeast(cur.Authority)` — so same-or-higher never
  refuses); a *lower*-authority derived-ID collision instead refuses the
  *whole Working section* as a unit, diagnosed
  `ErrMalformedDirective`/`ReasonBoundaryConflict` (R13's item-level
  diagnostic pairing, §7) — every member of that section is dropped, not
  only the colliding one (the same whole-section refusal §24/R20.4 already
  records for a same-ID boundary conflict; a Working section is either
  written entirely or not at all). The rest of the *event* still applies
  (other sections/items commit normally): this is a section-level
  refusal, not an event abort, and never `ErrInvalidAuthorityPromotion`.
  **Why this is a recorded deviation from FR-DIR-007 *and* FR-AUTH-001,
  and why the deviation is safe (SPEC-3.5, reworded per SPEC-4.5 —
  "consistent with FR-AUTH-001" was wrong: the rule below departs from its
  literal text, deliberately, not merely from an unaddressed gap):**
  FR-DIR-007 scopes a Working snapshot's supersession to the *same*
  authority, with by-ID supersession additionally available only for
  *explicit* IDs — it does not itself describe a derived-ID collision
  *across* authorities at all, so same-or-higher-authority derived-ID
  supersession is new ground this ADR covers, not a reading of
  FR-DIR-007's existing text; it is deliberately symmetric with
  explicit-ID behavior (a higher authority may always supersede a lower
  one's directive) rather than inventing a different rule for the
  derived-ID case. The lower-authority case is a refusal, not a
  supersession, so it does not touch FR-DIR-007's supersession rule at
  all. **FR-AUTH-001 is the literal deviation:** it requires "unauthorized
  operations fail atomically with `ErrInvalidAuthorityPromotion`," and
  FR-DIR-002 makes ID reuse (which a derived-ID collision is) explicitly
  "subject to FR-AUTH-001" — but the code returns
  `ErrMalformedDirective`/`ReasonBoundaryConflict`, refusing only the
  Working section, never `ErrInvalidAuthorityPromotion`, and never the
  whole event. The deviation is deliberate and safe because **this is an
  identity/boundary conflict refusal, not an executed unauthorized
  mutation**: nothing is partially written here, either for the section
  (all its members are refused together) or for the event (every other
  section/item still commits) — there is no partial-authorization state
  for FR-AUTH-001's atomicity guarantee to protect, because no
  authorization decision was actually acted on for the refused member.
  Explicit-ID members
  are unaffected by any of this (they still go through only the ordinary
  boundary-conflict check). Test:
  `TestWorking_DerivedIDAcrossAuthorities_DUR15`
  (`internal/ingest/working_test.go`) — asserts both halves: a USER
  Working section colliding with an existing SYSTEM line is refused whole
  (zero items, the rest of the event's other sections still apply, the
  SYSTEM snapshot is undisturbed), and a later SYSTEM line superseding an
  existing USER-authority derived-ID slot leaves the SYSTEM version
  current.
- **DUR-1.6: `MaxRelationships` now bounds the replacement and duplicate
  paths too (refines §13, D17).** The limit was checked before a
  `DERIVED_FROM` edge (`internal/ingest/directives.go`'s `linkDerived`)
  but not before the `SUPERSEDES` edge a replacement writes or the
  `DUPLICATE_OF` edge a duplicate writes, so either path could exceed the
  event's relationship budget uncounted. `internal/ingest/directives.go`
  now checks `r.rels >= r.limits.MaxRelationships` at every edge-writing
  site (three call sites, plus `derive.go`'s original one), failing
  `errLimit("MaxRelationships")` before the edge is written. Test:
  `TestRelationshipLimitOnReplaceAndDuplicate_DUR16`
  (`internal/ingest/fixes_r1_test.go`).
- **DUR-1.7: deterministic failure order for a Working snapshot's
  retirements (refines §7, D11).** When several planned retirements in one
  snapshot write could each independently fail authorization, the order
  they were checked in was map-iteration order — nondeterministic, so a
  replay could fail on a different member than the original attempt.
  `internal/graph/snapshot.go` now sorts the retirement set by `(Seq, ID)`
  (`bySeqID`) before validating, so "when several planned retirements
  would fail, the error is always the earliest one's" (the code's own
  comment) — deterministic across replay. Test:
  `TestSnapshot_DeterministicFailure_DUR17`
  (`internal/graph/snapshot_test.go`).
- **SEC-1.4: whole-event byte limits cover every caller-supplied string,
  and locators are constrained to a display-safe charset (refines §13,
  D17; new decisions, not previously recorded).** D17's original limits
  bounded span/item/blob/diagnostic counts and total bytes, but several
  individually-small, caller-supplied strings outside those counts —
  `Source.Locator`, `Source.ToolCallID`, `ContentPart.MediaType`, and
  every owner ID (session/workflow/task/agent, on both the principal and
  each span's access boundary) — were unbounded, so an event could carry
  an arbitrarily large amount of caller-controlled text `PayloadHash`
  hashes and ingestion persists without ever being counted by
  `MaxEventBytes`. **Landed:** `internal/domain/ingest.go` adds
  `MaxLocatorBytes = 4096`, `MaxToolCallIDBytes = 256`,
  `MaxMediaTypeBytes = 255`, and `MaxOwnerIDBytes = 256`, enforced in
  `SourceRef.Validate`, `InputPart` validation, and
  `validateIngestPrincipal`/span-boundary validation; every field is
  accepted at its bound and rejected one byte over, by `Validate`,
  `PayloadHash`, and `ValidateFor` alike (`TestEventMetadataBounds_SEC14`).
  **Locator UTF-8/charset rule:** a locator must be valid UTF-8 with no
  control characters (C0, DEL, C1) and no bidirectional-formatting
  characters (`displaySafe`, `internal/domain/ingest.go`) — "so a locator
  cannot inject line breaks, terminal escapes, or reordered text into
  logs, diagnostics, or rendered context" (the function's own comment); a
  non-ASCII but otherwise clean path or URL (e.g. a Unicode filename) is
  accepted, only control and bidi-override bytes are rejected. A tool-call
  ID must be printable ASCII with no space; a media type must be printable
  ASCII (parameters like `; charset=utf-8` allowed). **Harness guidance
  (binding on integrations, not enforced beyond charset validity):** a
  harness whose natural locator representation contains a byte this rule
  rejects — for example a URL component that legitimately needs a raw
  control byte — must percent-encode it before constructing the `Event`;
  the runtime validates the charset, it does not perform the encoding.
  Test: `TestEventMetadataCharsets_SEC14`
  (`internal/domain/sec14_test.go`, both tests). Separately landed in the
  same area: `Limits.MaxReferenceLinks` (default 256, §13/D17) bounds the
  REFERENCES edges one event may create; exceeding it stops adding
  optional edges and reports `ReferenceLinksTruncated`
  (`internal/ingest/references.go`), a new `DiagnosticCode` alongside
  `ItemUnverified` (DUR-1.4, §26). **This truncate-rather-than-reject
  behavior is a recorded departure from §13's general "exceeding any
  limit rejects the entire event" rule and from a literal reading of
  FR-DIR-003 ("a matching name gets a REFERENCES edge" / "is linked," with
  no truncation clause), SPEC-2.4: rejecting the whole event once a
  session has already accumulated 256 reference links would make an
  otherwise-ordinary event increasingly likely to fail for reasons outside
  the caller's control, so the ruling truncates the optional edges instead
  and preserves everything else the event does — the same trusted-limit
  rationale as diagnostic truncation (§13), extended to this one semantic
  (non-diagnostic) output.** `Diagnostic.Validate` now mechanically
  enforces every reason-to-code pairing through a `reasonCode` map
  (`internal/domain/diagnostic.go:145-151`:
  `ReasonBoundaryConflict→ErrMalformedDirective`,
  `ReasonTargetMismatch→DiagnosticNotFound`,
  `ReasonUnverifiedItem→ItemUnverified`,
  `ReasonReferenceLinksTruncated→ReferenceLinksTruncated`) — this
  formalizes and mechanically locks §23's R19 code/reason pinning decision,
  which was previously enforced only by convention at each call site. A
  receipt now also records the `MaxReferenceLinks` value an event was
  checked against (migration 0014, ADR 3), so a replayed old receipt
  reports the limit that actually applied, never today's default (M8).
  Test: `TestMaxReferenceLinks` (`internal/domain/limits_reference_test.go`).
- **SPEC-1.1: a suppressed line never starts a directive item, even
  mid-list (refines §4, D5/R17; distinct from — and narrower than — the
  column-0 fence/quote case §22 already records).** A bullet on a line
  that was itself suppressed (inside an indented fence, an HTML comment,
  or a quote, without triggering the column-0 whole-section-malformed
  case) could still start an item, because bullet recognition did not
  check the line's suppression state. `internal/directive/items.go`'s
  `startsItem` now requires `l.suppressed == ""`, so a suppressed line
  never starts an item regardless of position; a `poison` tracker
  separately flags the first column-0 suppressed line inside a list body
  and, when set, drops every item the whole section produced (`p.items =
  p.items[:first]`), per R17. Tests: `TestSuppressedListContent`
  (`internal/directive/attacks_test.go`), `TestSuppressedListContent_Ingest`
  (`internal/ingest/structure_test.go`); canonical goldens
  `testdata/directives/suppress-list-{comment,fence,indented-control,
  indented-fence,lifecycle-fence,quote}`.
- **SPEC-1.12: R11's duplicate-semantics comparison now includes the
  obligation declaration (refines §7/§20, D10/D13/R11).** R11 required
  `SameDirectiveSemantics` to compare the obligation declaration "so
  `p2-graph` and `p2-ingest` implement one comparison, not two," but the
  comparison left it to callers, and `graph.LinkDuplicate` could link a
  Pinned item as `DUPLICATE_OF` a canonical item whose declared obligation
  claim actually differed. `internal/graph/duplicate.go`'s new
  `SameDirective(tx, it, newClaim, canonical)` wraps
  `SameDirectiveSemantics` and additionally compares the obligation claim
  via `tx.ObligationsBySource(canonical.ID, maxDeclaredClaims)` (bounded,
  R9/D17): an exact match requires no claim on either side, or exactly one
  current version with the same claim. Test:
  `TestLinkDuplicate_ComparesObligationClaim`
  (`internal/graph/duplicate_test.go`).
- **TEST-1.1/1.2/1.3 (test-suite gaps the first review found, not tied to
  a fix).** TEST-1.1: `TestD10_MappedDuplicateNeverCurrent`
  (`internal/graph/current_test.go`, alongside the existing
  `TestD10_DuplicateDirectiveNeverCurrent`) locks that a `DUPLICATE_OF`
  item can never become current through a stale current-map pointer,
  independent of `TestD10_DuplicateDirectiveNeverCurrent`'s coverage.
  TEST-1.2: `TestRetryIdentity_SessionScopedRich` (above). TEST-1.3
  (`internal/ingest/retry_concurrency_test.go`, both comments cite
  "TEST-1.3, D17" directly — **SPEC-3.6: corrected, split into its two
  actual halves**): `TestLateLimitRejectionIsAtomic` locks that a late
  limit rejection is atomic and never poisons the `EventID` for a future
  retry; `TestDiagnosticsCapTruncates` locks that the diagnostics cap
  truncates correctly under it.

## Alternatives considered

- **D1:** the brief's read-only resolution without an explicit
  `PARSED_NOT_EXECUTED` marker and without an authorization check was
  rejected by the review: an apparently successful Unpin that changed
  nothing is a dangerous API lie, and read-only resolution is not itself
  an authorization check — a SYSTEM caller carrying a USER span could
  otherwise execute that span's Resolve as SYSTEM (confused deputy). The
  review's own proposal deferred authorization failure to a diagnostic;
  R7 overrides that and makes it abort the event atomically instead,
  matching FR-AUTH-001's atomicity for every other mutation.
- **D3:** silently substituting U+FFFD for invalid UTF-8 (the shipped
  Phase 1 SQLite behavior) was rejected — it breaks hash identity, audit
  integrity, and store parity, and a raw hash was never meant to be
  interchangeable with `ContentHash`.
- **D5/D6:** the brief's "any KEYWORD heading at any level closes the
  section" was rejected as a direct contradiction of FR-DIR-006, which
  specifies same-or-higher-level closure; nested-directive semantics, if
  ever wanted, need an explicit grammar amendment, not an ADR override.
  The brief also omitted fence-closer suffix rules and comment/fence state
  precedence, which the review's state-machine formulation supplies; the
  added HTML-comment exclusion is recorded as a conservative parser-v1
  choice, not a re-derivation of full CommonMark.
- **D7:** trimming trailing whitespace from item text (the brief's implied
  behavior) was rejected — trailing whitespace can be meaningful content,
  and trimming it can silently make two distinguishable directives hash
  identically; salvaging an invalid `[id]` with a fallback derived ID was
  also rejected, since that turns a malformed line into a new, powerful
  directive.
- **D8:** treating every SYSTEM/HARNESS transcript as an independently
  mandatory instruction merely because of its authority (a natural reading
  of the brief's "one transcript item, kind by authority") was rejected —
  it would keep a replaced pin mandatory forever through its unsuperseded
  transcript copy; separately identified semantic instruction items are
  required instead.
- **D10:** the brief's implicit reliance on `graph.IsCurrent`'s existing
  incoming-supersession-edge check as sufficient for "never becomes
  current" was rejected — the review reproduced a duplicate resolving as
  current by literal ID lookup, since that check never also excludes a
  stale current-map pointer or a `DUPLICATE_OF` classification.
- **D11:** the brief's two-helper-call composition (mark duplicates, then
  supersede) was rejected — marking a duplicate member first makes it
  unusable as a superseder for the member it should retire, reproducing
  `W1={a,b}→W2={a}`'s failure to retire `b`. A single planned edge set per
  snapshot section replaces the two-step composition.
- **D12:** ASCII-case-insensitive scope/attribute values (the brief's
  stated rule) directly conflicts with FR-DIR-006's "everything else is
  exact" and was rejected. An unbounded `ttl` under an "ignore over-limit
  values" rule was also rejected, since silently ignoring a large-but-finite
  TTL can accidentally make a finite-lived item unlimited; R1's explicit
  representation-limit error was chosen over the review's own unspecified
  int64 bound, for portability across Go int widths.
- **D18:** letting `TaskState.Validate`/`PutTask` alone enforce turn
  semantics (the brief's implicit assumption) was rejected — low-authority
  content could otherwise forge a turn boundary and indirectly expire
  higher-authority TURN requirements; an explicit envelope-kind/
  authenticated-caller gate is required.
- **D19:** trusting session scoping alone as sufficient blob protection
  (the brief's stated position) was rejected — session scoping stops
  cross-session probes but not narrower-boundary ones within a session;
  R5's item-must-already-reference-the-blob check was chosen over a new
  standalone blob-access-record type for Phase 2, deferring that type
  until a real use case needs it.
- **M5/R2:** implementing tool-result `DEPENDS_ON` edges in Phase 2 (the
  review's original M5 scope) was rejected by R2 — `tool_call` items
  belong to the call ledger, which is Phase 5 work; recording a bare
  `ToolCallID` in `SourceRef` now avoids inventing edge semantics ahead of
  the ledger that will actually own them.
- **M6/R6:** reserving an undocumented ID prefix for agent keys instead of
  a typed namespace field was rejected — FR-DIR-006 already legally allows
  dotted IDs like `agent.status`, so a prefix reservation would
  retroactively make an otherwise-legal directive ID illegal; a typed
  namespace field is additive instead.
- **M8/R3:** exporting the rich Phase 2 `domain.IngestReceipt` as a
  root-package alias now (the review's original phrasing, "if the public API will
  return items plus diagnostics/commands") was narrowed by R3 to
  explicitly not do so until the SDD §8 signature itself changes in
  Phase 5 — an early alias would let Phase 2's internal shape leak into the
  public contract before the contract is actually revised.

## Consequences / compatibility impact

- Phase 1 schema additions required before Phase 2 store/graph work can
  proceed: `domain.Event`/`Span`/part envelope (§5), `SourceRef` byte
  ranges (§5), obligation claim-name field separate from `MatcherRef`
  (§9), receipt/occurrence-ID types (§10), diagnostic record type (§12),
  limits config (§13) — all additive to `internal/domain`, none breaking an
  already-accepted Phase 1 ADR's decision.
- `internal/store/sqlite`'s lossless text fix (§3, R8) changes on-disk
  representation for `ContentPart`; it lands before other Phase 2 store
  work and needs its own `storetest` case, but is a bug fix against
  Phase 1's own contract (FR-ING-007), not a new Phase 2 requirement.
- Forward migrations are required for: lossless text (§3), source
  ranges/receipts (§10/19), obligation claim names (§9), diagnostics
  (§12), and the typed directive/agent-key namespace (§17) — all additive
  to migration history; migration 0001 is never edited (R8).
- `graph.IsCurrent`'s existing incoming-edge-only semantics were
  insufficient for D10's "never becomes current" guarantee (§7); resolved
  (see ADR 4/16's Phase 2 amendment notes) — every current-version
  consumer now also excludes a `DUPLICATE_OF` item and a stale
  current-map pointer.
- `graph.Supersede`'s `(session, old target, action, event)` audit-ID
  scheme collided on two retirements of the same old target within one
  event; resolved by versioning the audit identity
  (`internal/graph/ids.go`'s `lifecycleEventIDTag` v1→v2), which adds the
  retirement's counterpart (its successor) to the hashed identity, so two
  audit records about one target in one event can never alias — a v1 ID
  already stored is left as it is. DUR-1.7 (§28) separately fixed a
  related nondeterminism: which of several retirements' *failures*
  surfaces first, now ordered by `(Seq, ID)`.
- The SDD §8 `Runtime.Ingest([]ContextItem, error)` signature is unchanged
  by Phase 2 (§19, R3); any code written against a richer expected return
  type must wait for the Phase 5 amendment.
- D1's `PARSED_NOT_EXECUTED` framing (§1) means Phase 3's
  lifecycle-execution work cannot treat a Phase 2 resolution as
  pre-authorized; it must redo access, authority, currentness, and grant
  checks in its own transaction.
- The classification table (§2) and the closed unsupported-lifecycle
  vocabulary (§4, applied to the SDD below) are now parser-versioned
  contracts: any future directive section or lifecycle word requires a
  table/vocabulary update plus a new parser version, not an ad hoc
  addition inside `internal/ingest`.

## Tests that lock the behavior

**(SPEC-2.2, reworded)** Concrete test names are cited per clause below.
Every clause below now names at least one currently-existing test,
grep-verified against the merged integration head (`go test -list` or
`grep -rn "^func Test" internal/...`) while writing this revision — the
opener's earlier framing ("a clause without one is a genuine gap") was
itself wrong for roughly twenty clauses that had tests all along but no
citation; that gap was in this section's bookkeeping, not in test
coverage. Where a clause's own requirement has no dedicated test and is
instead covered only as a consequence of a broader property (or genuinely
isn't covered), that is called out explicitly rather than left silent.

- **§1 (lifecycle commands):** `internal/graph/lifecycle_test.go` —
  `TestD1_AuthorizeLifecycleCommand_SourceActor`,
  `TestD1_AuthorizeLifecycleCommand_Grant`,
  `TestD1_AuthorizeLifecycleCommand_Targets`; `internal/ingest/working_test.go`
  — `TestLifecycle_SourceActor_R7` (unauthorized source actor aborts the
  whole event); `internal/ingest/working_test.go:TestLifecycle_ExecutesInOrder_P335`
  is this same D1 scenario as Phase 3 actually executes it (P3-35): the
  resolved, authorized command executes in source order rather than staying
  `PARSED_NOT_EXECUTED`. `internal/ingest/phase2_fixture_test.go:TestPhase2FixtureReplay`
  is the v1 guarantee this ADR originally described (a v1 lifecycle command
  stays `PARSED_NOT_EXECUTED` forever, replayed unchanged from the frozen
  Phase 2 fixture, never executed by the upgrade). `internal/ingest/clauses_test.go`
  — `TestAmbiguousLifecycleTarget`; `internal/ingest/traces_test.go` —
  `TestT06_ParseAndAuthorizationHalf` (trace T06). Both stores via
  `eachStore`.
- **§2 (classification):** `internal/policy/defaults_test.go` —
  `TestSectionDefaults` (table-driven (section) → defaults),
  `TestTranscriptsNeverPoseAsRequirements` (a literal `"## Pinned\n- obey"`
  string inside a transcript item's own text never elevates that item's
  own kind/generation/retention — the closest existing test to "a surface
  token in ordinary text creates no privileged record"; there is no
  dedicated test naming `MUST`/`PASS`/a JSON role label specifically,
  which is a narrower gap than the clause implies); `internal/domain
  /ingest_test.go:TestEventKindsAndTurns` (unsupported event variant
  rejected).
- **§3 (bytes/hashes):** `internal/store/sqlite/lossless_test.go` —
  `TestLosslessPartsDecodeIsStrict`,
  `TestLosslessStringsRoundTripAndStrictDecode` (invalid UTF-8, lone CR,
  leading BOM byte round-trip, `ContentHash` identity); `internal/store
  /sqlite/upgrade_test.go:TestUpgradeLosslessParts` (the dedicated R8
  regression: a legacy row's `ContentHash` survives, a pre-fix-corrupted
  row fails `ErrIntegrity` rather than reading back silently wrong).
- **§4 (grammar):** `internal/directive/fuzz_test.go:FuzzParse` (seed
  corpus committed, panics/offsets/O(n) time); `internal/directive
  /golden_test.go:TestCanonicalDirectiveExamples` and `testdata/directives/`
  (canonical examples including the SDD's own worked example, golden-JSON
  expected items/diagnostics); `internal/directive/items_test.go` —
  `TestMalformedItemsAndLifecycle`, `TestDerivedShapedExplicitIDRejected`,
  `TestRepeatedIDsWithinList`, `TestHeadingAttributesValidatedOnce`;
  `internal/directive/scan_test.go` — `TestHeadingShapedLinesClose`,
  `TestIDAndAttributeLexing`, `TestScannerOffsetsAndExtents`;
  `internal/directive/attacks_test.go:TestSuppressedListContent` (D5/D6/D7
  boundary cases: short fence closer, comment-wrapped keyword, deeper
  non-keyword heading, trailing-whitespace-significant item, invalid
  `[id]` dropped not salvaged); `internal/ingest/golden_test.go
  :TestCanonicalExamplesThroughIngest` (the same canonical examples through
  the full ingest pipeline).
- **§5 (transcript/derived items, isolation):** `internal/policy
  /defaults_test.go` — `TestTranscriptDefaults`, `TestResidualRows` (the
  two separate rows this clause's own corrected text distinguishes);
  `internal/ingest/directives_test.go:TestDirectives_Residual_D8`;
  `internal/directive/canonical_test.go
  :TestT18PastedDocumentAndWorkingSections`; `internal/ingest/working_test.go
  :TestWorking_T18`; `internal/ingest/t18_test.go:TestT18_EndToEnd` (trace
  T18 end-to-end, both stores); `internal/ingest/ingest_test.go
  :TestT18_PastedDocument`.
- **§6 (derived vs. explicit IDs):** `internal/directive/items_test.go
  :TestDerivedShapedExplicitIDRejected`; `internal/domain/ids_test.go` —
  `TestDerivedDirectiveIDFormat`, `TestDerivedDirectiveID_LowercasesKeyword`,
  `TestDerivedDirectiveID_StripsHashPrefixOnly`; `internal/policy
  /ids_test.go:TestExplicitIDNamespaceD20`.
- **§7 (dedup/replacement/Working):** `internal/graph/current_test.go` —
  `TestD10_DuplicateDirectiveNeverCurrent`, `TestD10_MappedDuplicateNeverCurrent`
  (closing the review's reproduced hole), `TestD10_UnmappedDirectiveItemNeverCurrent`,
  `TestD10_StalePointerIsNotAPreviousVersion`,
  `TestD10_CurrentVersionsDeterministicAndFiltered`; `internal/graph
  /duplicate_test.go` — `TestLinkDuplicate_DirectiveDuplicate`,
  `TestLinkDuplicate_RestatedAcrossTurns` (cross-turn/cross-TTL-origin items
  never deduplicated as stand-ins), `TestLinkDuplicate_ComparesObligationClaim`,
  `TestLinkDuplicate_RejectsNonDuplicates`,
  `TestLinkDuplicate_CannotRetireExistingItems`; `internal/graph
  /snapshot_test.go` — `TestSnapshot_T18_RepeatedMemberRetiresAll`
  (`W1={a,b}→W2={a}` retires `b`), `TestSnapshot_IdenticalIsDuplicate`,
  `TestSnapshot_ChangedSnapshots`, `TestSnapshot_ExplicitIDComposes`,
  `TestSnapshot_MultipleSectionsOneEvent`,
  `TestSnapshot_PartitionsByBoundaryAndAuthority`,
  `TestSnapshot_RejectsBeforeWriting`; `TestSupersede_AuditIdentityNamesSuccessor`
  (two retirements of the same old target in one event do not collide on
  audit ID).
- **§8 (attributes/TTL/scope):** `internal/policy/attributes_test.go` —
  `TestAttributeAllowList`, `TestAttributeValuesExact` (exact-case
  rejection), `TestParseTTLR1` (`ttl=0001` leading zeros;
  above-2147483647 representation-limit failure), `TestScopeExactAndWidening`
  (USER-span WORKFLOW/SESSION widening ignored with a diagnostic — **SPEC-3.6:
  corrected; this test does not also assert the AGENT-scope-never-removes-an-
  existing-constraint half, which has no dedicated test and is a genuine
  gap for `p2-tests`**), `TestForDirectiveFailsClosed`;
  `internal/directive/items_test.go:TestTTLRepresentationLimit`;
  `internal/directive/policycheck_test.go:TestRepresentationLimitAgreement`.
- **§9 (obligations):** `internal/graph/obligation_test.go` —
  `TestD13_ReplacementRetiresBoundObligations` (replacing the pin retires
  the old version and starts a fresh UNRESOLVED version only if
  redeclared), `TestD13_SnapshotIDReplacementRetiresObligations`,
  `TestD13_DuplicateLeavesObligations`,
  `TestD13_UnauthorizedIndirectRetirementAbortsReplacement`;
  `internal/domain/obligation_test.go
  :TestDerivedObligationIDKeyedByDirectiveNotClaim`; `internal/ingest
  /directives_test.go:TestDirectives_ReplacementAndObligations_T02` (trace
  T02's obligation half).
- **§10 (idempotency/receipts):** `internal/ingest/retry_test.go` —
  `TestRetryIdentity_SessionScoped`, `TestRetryIdentity_SessionScopedRich`,
  `TestRetryIdentity_RichReceipt`, `TestRetryConflict_EveryPayloadField`
  (a conflicting payload/principal fails `ErrEventIDConflict`);
  `internal/ingest/gate_test.go` — `TestReceiptIsImmutable_D14`,
  `TestConcurrentRetries_D14`; `internal/ingest/retry_concurrency_test.go`
  — `TestAnonymousOccurrencesNeverAlias` (anonymous events stay isolated
  under an empty key), `TestConcurrentIdenticalRetries_Rich`,
  `TestConcurrentAnonymous`; `internal/ingest/clauses_test.go
  :TestReplayNeverRecomputes` (a retry after a changed `Limits`/version
  configuration replays the original receipt unchanged — the property
  SPEC-1.10's ruling below relies on for the version-bump case). No test
  is named for a literal `Store.Update`/`View` reentrancy deadlock
  specifically; every SQLite-backed case above would hang rather than pass
  if the transaction-scoped core reentered, so the property is exercised
  continuously rather than by one dedicated regression.
- **§11 (authority/actor):** `internal/ingest/ingest_test.go
  :TestAuthority_D15`; `internal/ingest/directives_test.go
  :TestDirectives_ConfusedDeputy_D15`; `internal/domain/item_test.go
  :TestContextItemValidate_SectionRequiresLifecycleAuthority` (directive-capable
  marking on an AGENT/TOOL/RETRIEVED_CONTENT item fails validation);
  `internal/domain/authz_test.go` —
  `TestAuthorizeMutation_InaccessibleTargetReturnsNotFoundBeforeAuthority`,
  `TestAuthorizeSupersession_InaccessibleEndpointNotFound` (bare
  `ErrNotFound`, access checked before authority). The SYSTEM-carried
  USER-span-executes-at-USER-authority half is explicitly a Phase 3
  behavior per this clause's own text ("once Phase 3 lands"); Phase 2 has
  no test for it because Phase 2 does not execute lifecycle commands at
  all (D1, §1).
- **§12 (diagnostics):** `internal/domain/diagnostic_test.go
  :TestDiagnosticRecordKeysAndAccess` (unreadable outside the source's
  access boundary), `TestIngestionReasonCodePairing`;
  `internal/ingest/retry_concurrency_test.go:TestDiagnosticsCapTruncates`;
  `internal/ingest/clauses_test.go:TestTruncationPersistedAndReplayed`
  (the 256-plus-one marker, stable and stored, replayed unchanged);
  `internal/store/sqlite/durability_test.go:TestRestartPreservesRecords`;
  `internal/store/sqlite/ingestion_test.go:TestReceiptRowCoversReceipt`
  (migration/restart/replay coverage). No dedicated rollback-of-diagnostics
  test exists distinct from the general poison/rollback conformance rows
  (DUR-1.3, §27); that is inherited coverage, not a clause-specific test.
- **§13 (limits):** `internal/directive/adversarial_test.go` —
  `TestPathologicalLinearScan` (many-small-units input bounded in total
  work), `TestPerUnitMetadataLimits`; `internal/directive/attacks_test.go
  :TestAdversarialFatalLimits`; `internal/ingest/gate_test.go` —
  `TestLimits_D17`, `TestItemsPerSpanAcrossParts_D17` (an oversized event
  rejected wholesale with no partial state change); `internal/domain
  /limits_test.go:TestDefaultLimitValues`.
- **§14 (turns):** `internal/ingest/ingest_test.go:TestTurns_D18` (Turn=0→1
  on a first task-bound event; AGENT/TOOL/RETRIEVED_CONTENT cannot advance
  a turn), `TestTurnOwnership_D18`; `internal/ingest/clauses_test.go
  :TestCompletedTaskNeverReactivated`,
  `:TestRetrievedBeforeFirstTurn`; `internal/domain/source_range_test.go
  :TestTurnOwnershipAndTTL` (a TURN-scoped item expires next turn even
  with a larger `ttl`). "An exact retry never re-advances the turn" is
  exercised inside `TestReplayNeverRecomputes` (§10) rather than a
  dedicated turn-specific test.
- **§15 (blobs):** `internal/ingest/ingest_test.go:TestBlobReferences_R5`.
- **§16 (references):** `internal/ingest/references_test.go` —
  `TestReferences_M5`, `TestLocatorKey_M5`, `TestReferences_SurviveRestart`,
  `TestReferencesByItemID_F4`, `TestReferenceLinkBudget_Ruling1`;
  `internal/graph/reference_test.go:TestLinkReference_M5`;
  `internal/ingest/clauses_test.go:TestToolCallIDCreatesNoEdge` (a Phase 2
  TOOL span's `ToolCallID` lands only in `SourceRef`, no edge created).
- **§17 (namespace):** `internal/domain/namespace_test.go
  :TestItemNamespaceSeparatesDirectivesFromAgentKeys` (`agent.status`
  accepted as a legal directive ID string, no collision with a keyed
  agent-write entry); `internal/graph/namespace_test.go` —
  `TestR6_LifecycleResolvesOnlyDirectiveNamespace`,
  `TestR6_NamespacesNeverReplaceEachOther`; `internal/store/sqlite
  /current_directives_test.go:TestCurrentDirectivesAcrossBoundaries`
  (SQLite-specific).
- **§18 (relationship input):** no dedicated test, and this is a genuine
  structural point rather than a test gap (SPEC-2.2): `domain.Event` has
  no caller-supplied relationship field at all — there is nothing for
  `Event.Validate` to reject, because the grammar admits no such input in
  the first place. "Relationship-shaped JSON inside tool text produces no
  edge" is covered only as an instance of the general property that
  ordinary content text is never interpreted as anything but text (§2's
  `TestTranscriptsNeverPoseAsRequirements` is the closest existing
  evidence); no test names a relationship-shaped JSON payload specifically,
  which is a real, if narrow, gap for `p2-tests`.
- **§19 (API shape):** `api_test.go:TestRootAliasesOnlyIngestInputTypes`
  (**SPEC-3.6: corrected — this, not `imports_test.go:TestPackageBoundaries`,
  is the allowlist test; `TestPackageBoundaries` checks the import/dependency
  graph, an unrelated property**) — the root package never aliases
  `domain.IngestReceipt`, diagnostics, or lifecycle command records, only
  `Event`/`Span` among the ingestion-relevant types (§19's decision text
  above, corrected the same way);
  `internal/store/sqlite/upgrade_test.go` — `TestUpgradeProvenanceColumns`,
  `TestUpgradeCurrentNamespace`, `TestUpgradeLosslessParts`,
  `TestUpgradeLosslessStringLists`, `TestUpgradeReceiptLimits` (the
  upgrade/restart parity fixtures for each pre-Phase-2 record shape read
  after the new migrations land).
- **§20 (round 2 rulings, R9-R15):** `internal/graph/fanout_test.go
  :TestReplaceDirective_ObligationFanOut` (R9: retiring obligations bound
  to a replaced source uses the obligations-by-source lookup and stays
  bounded under a large fan-out).
  **R10's `ErrNamespaceConflict` transitional rule (SPEC-1.4: removed
  here) never needed a test:** the typed namespace switch (M6/R6) landed
  directly, with no gap for a transitional fail-closed rule to cover, so
  no such symbol exists and none should be expected.
  `internal/graph/duplicate_test.go:TestLinkDuplicate_ComparesObligationClaim`
  and `TestLinkDuplicate_RestatedAcrossTurns` (`SameDirectiveSemantics`
  includes creation turn, TTL origin, and obligation declaration in its
  comparison, both in `internal/graph` unit tests and as the single
  comparison `internal/ingest` calls, R11); `internal/graph/snapshot_test.go
  :TestSnapshot_PartitionsByBoundaryAndAuthority` (the Working-snapshot
  duplicate-vs-changed decision is scoped per `(task, authority,
  boundary)`, R12); `internal/graph/namespace_test.go
  :TestR13_CheckBoundaryConflict` and `internal/ingest/directives_test.go
  :TestDirectives_BoundaryConflict_R13` (a same-ID boundary conflict on one
  item among several rejects only that item and commits the rest, R13);
  `internal/ingest/working_test.go:TestLifecycle_ExecutesInOrder_P335`
  (Resolve on a non-OPEN goal / Unpin on a non-pinned target each produce a
  `TargetMismatch` diagnostic with result status `domain.CommandNotExecuted`
  (`"NOT_EXECUTED"`, a `domain.CommandStatus` value, not itself a diagnostic)
  and commit the rest of the event, R14; this is the same test that now also
  carries D1's execution-order scenario under Phase 3, above).
- **§21 (round 3 ruling, R16):** `internal/directive/policycheck_test.go`
  — `TestParserAcceptedImpliesPolicyAccepted`, `FuzzPolicyAgreement` (the
  fuzz/property cross-check that every `internal/directive`-accepted
  directive is also `policy.ForDirective`-accepted, so a directive
  `internal/directive` accepts but policy would reject is never classified
  as if accepted); `internal/domain/ingest_test.go
  :TestEventIDAndToolCallRules` (an `EventID` with non-printable-ASCII
  bytes or over 256 bytes rejected before any idempotency lookup);
  `internal/policy/defaults_test.go:TestResidualRows` (a HARNESS residual
  instruction item is never mandatory under FR-DOM-007's policy-mandatory
  path); `internal/domain/source_range_test.go:TestItemSourceRanges`
  (`Item.TextRanges`/`SourceRef.Slices` round-trip the same byte ranges for
  a canonical fixture). No test is named specifically for "an event whose
  kind implies lower authority than one of its spans is rejected
  wholesale"; `internal/domain/ingest_test.go:TestEventValidationAuthorityAndLimits`
  is the closest existing coverage of event/span authority validation, but
  does not name this exact kind-vs-span-authority mismatch case — a
  narrow gap for `p2-tests`.
- **§22 (round 4 rulings, R17-R18):** `internal/directive/policycheck_test.go
  :TestParserAcceptedImpliesPolicyAccepted` (R17, reconfirming R16); a span
  whose diagnostics split across multiple parts are capped per-span after
  merging in part order, then again at the whole-event total (R17) — no
  test name is given for this exact merge-then-cap-twice ordering
  specifically, a narrow gap; `internal/directive/items_test.go
  :TestRepeatedIDsWithinList` (two list items sharing one explicit ID are
  both absent, not one-survives-one-diagnosed, R17);
  `internal/directive/attacks_test.go:TestSuppressedListContent` (a
  column-0 fence/quote marker inside a list body malforms the whole
  section, R17); `internal/directive/items_test.go
  :TestTTLRepresentationLimit` (an out-of-range `ttl` aborts the event
  rather than leaving the item live with no TTL, R17, confirming R1); every
  persisted item/diagnostic/receipt records parser version `directive/v1`
  (R17) — asserted structurally by every golden/canonical fixture's
  expected `ParserVersion` field rather than one dedicated test.
  `internal/store/storetest` —
  `ObligationsBySource` with a `limit` lower than the bound source's
  obligation count fails `store.ErrLimitExceeded` (SPEC-1.4: corrected —
  not "returns exactly `limit` versions"; R9/D17's bounded-scan discipline
  rejects an oversized request outright rather than silently truncating
  it), `bysource.go:61-62` (R9/R18). Landed and covered: no remaining
  `internal/graph` call site uses the deleted `CurrentDirective`/
  `CurrentDirectives`/`SetCurrentDirective` (R18); `domain.TTLLive(0,
  current, n)` is `false` for every `current`/`n` (R18); `domain
  .UnresolvedReference` round-trips through migration 0008 and survives
  restart (R18).
- **§23 (round 5 ruling, R19):** `internal/domain/source_range_test.go
  :TestItemSourceRanges` (a derived item's `SourceRanges` alone
  reconstructs its transcript coverage, with no companion section record
  to keep consistent, R19); `internal/domain/receipt_test.go
  :TestIngestReceiptValidate` (an `IngestReceipt` with `OpenedTurn`/`TurnID`
  set has its turn-opening pending-input items exactly in `Items`, R19);
  `internal/domain/diagnostic_test.go:TestIngestionReasonCodePairing` (a
  diagnostic paired with `ReasonBoundaryConflict` always carries `Code:
  ErrMalformedDirective`, one paired with `ReasonTargetMismatch` always
  `DiagnosticNotFound`, never a different code for either — the pinning
  decision above).
  `internal/ingest` — `TestRetrievedBeforeFirstTurn` (`clauses_test.go`)
  confirms a TOOL or RETRIEVED_CONTENT event for a task with `Turn=0` is
  rejected before any item is created (R19); a References locator match
  never considers a repository/namespace component, so two same-named
  locators in different conceptual repositories within one session are
  treated as the same target (R19, until multi-repo support exists).
  Landed, then superseded by F1's access-filtered redesign (§26): blob
  reference, duplicate-candidate, working-snapshot, and source-key lookups
  each run in bounded time via `TestAccessLookupsUseIndex` and
  `TestGraphReadsUseIndex` (`internal/store/sqlite/access_lookups_test.go`),
  independent of session size and of the calling principal's visibility.
  **(SPEC-2.1/SPEC-2.2) This claim holds for every read on the per-item
  ingest path** — including `Relationships`/`Items` by type/task, fixed by
  migration 0015's composite key-plus-order indexes after `relationship_to`
  (0012) and `relationship_from`/`item_task` (0001, not 0012 — SPEC-3.3
  corrects the provenance) turned out not to be enough on their own —
  **but not for the five reads §13 records as deliberately deferred**
  (`tx.Grants()`, `ObligationTransitions`, `LifecycleEvents`,
  `Obligations(taskID)`, `Diagnostics`/`LifecycleCommands` with no
  `OccurrenceID`): four of the five never run during ingestion at all, so
  "independent of session size" was never meant to, and does not, cover
  them. `tx.Grants()` is the fifth and different case (SPEC-3.3
  corrects an earlier claim here that grouped it with the other four): it
  *does* run on the ingest path, once per replaced item with a bound
  obligation and once per lifecycle command, and is deferred precisely
  because it is *not* bounded there — a deliberate, recorded exception to
  this claim, not an absence from the path. See ADR 3.
- **§24 (round 6 ruling, R20 — all landed, SPEC-1.4):** `internal/domain
  /ingest_test.go:TestEventIDRejectsReservedPrefixes` — an `EventID` equal
  to or prefixed like `evc_`, `eva_`, any `IDDomain` prefix, or `lce_`
  fails `Event.Validate`. `internal/ingest/r20_test.go` —
  `TestR20_2_NoDerivedNoticeForRefusedItems` (a `DirectiveIDDerived`
  diagnostic for an item whose section ingest refused is absent from the
  receipt — the filter is `_, ok := r.written[d.Range]; !ok`,
  `derive.go`; **SPEC-2.2: `r.written` is `map[domain.ByteRange
  ]domain.AccessBoundary`, so the earlier `!r.written[d.Range]`
  quote — a boolean negation of a non-bool map value — no longer matches
  the code and is corrected here**), `TestR20_3_Residuals` (a residual
  instruction is never created for a leading-BOM-only or whitespace-only
  residue, `blankResidue`; a residual instruction's content includes
  leading and trailing whitespace exactly as it appeared in the
  transcript, `residue` keeps bytes exactly; a malformed section inside a
  SYSTEM or HARNESS span still produces a residual instruction item from
  its bytes), `TestR20_1_ErrorsNeverEchoItemIDs`. Also confirmed by
  `internal/ingest/working_test.go:TestWorking_DuplicateAndMalformed`:
  `workingSection` creates zero items for a `Malformed` section or one
  with no items, the correct (stricter) reading of D11; a boundary
  conflict on any Working-section member aborts the whole section's write
  with `ErrMalformedDirective`/`ReasonBoundaryConflict`, never a partial
  commit, confirming §23's pinning.
- **§25 (round 7 ruling, R21 — landed with R20, SPEC-1.4):**
  `internal/ingest/r21_test.go:TestR21_ResidualExclusions` — a unit whose
  only residue is a bare malformed heading with no body creates no
  residual instruction item (R21); a malformed Resolve/Unpin/
  unsupported-lifecycle-word section in a SYSTEM or HARNESS span never
  produces a residual instruction, staying transcript-only with its
  diagnostic, even though a malformed content section in the same span
  now does (R21) — the negative case a naive "any malformed trusted
  section becomes residual" implementation would get wrong; a unit with
  both a refused section and an earlier-positioned trusted directive item
  creates its residual instruction item, if any, after every directive
  item and lifecycle command the unit produced, never ordered by the
  residual's own byte position (R21, amending M3's creation order) — no
  test asserts this creation-order claim specifically by ID/`Seq`, a
  genuine gap for `p2-tests`; it is implied by, but not the same
  assertion as, `TestR20_3_Residuals`'s content checks above.
- **§26 (F1-F6, all landed):** `TestAccessLookupsUseIndex`,
  `TestUpgradeAccessLookups`, `TestGraphReadsUseIndex`,
  `TestLegacyLookupsDropped`, `TestLegacyUnverifiedNeverBlocks` (SQLite,
  F1/DUR-1.1/DUR-1.4); storetest `BlobReferrerAccess`/`CanonicalCandidates`/
  `CurrentWorking`/`SourceItems`/`VisibleReferences`; `TestLookupsNeverLockOut_F1`,
  `TestUnverifiedMatchesNeverBlock_DUR14`, `TestRetryAfterLimitsChange_F3`,
  `TestRelationshipLimitOnReplaceAndDuplicate_DUR16`,
  `TestRecordsAtNarrowerBoundary_F6` (`internal/ingest`); a repeated
  `EventID` from a different principal fails `domain.ErrEventIDConflict`
  as a bare sentinel, no ID/principal/session detail in its text or
  wrapped chain (F2). Full citations and mechanisms are in each finding's
  own §26 paragraph above, not repeated here.
- **§27-§28 (further round 1 rulings and findings, all landed):** see each
  finding's own paragraph above for its exact test name(s) —
  `TestHeadingShapedLinesClose` and its four canonical goldens (SPEC-1.11);
  `TestReplayNeverRecomputes` plus SPEC-1.10's other named clauses;
  `TestApplyFailureAtomic_DUR13` and the `storetest/poison.go` conformance
  rows (DUR-1.3); `TestWorking_DerivedIDAcrossAuthorities_DUR15` (DUR-1.5);
  `TestSnapshot_DeterministicFailure_DUR17` (DUR-1.7);
  `TestEventMetadataBounds_SEC14`/`TestEventMetadataCharsets_SEC14`/
  `TestMaxReferenceLinks` (SEC-1.4); `TestSuppressedListContent`(`_Ingest`)
  (SPEC-1.1); `TestLinkDuplicate_ComparesObligationClaim` (SPEC-1.12);
  `TestD10_MappedDuplicateNeverCurrent` (TEST-1.1);
  `TestLateLimitRejectionIsAtomic`/`TestDiagnosticsCapTruncates`
  (TEST-1.3, its two halves — SPEC-4.5: previously credited to
  `TestDiagnosticsCapTruncates` alone).
- **Cross-cutting (decision-review gate additions):** every path above run
  under `-race` where concurrent ingestion applies; injection-resistance
  tests for each §9-of-the-SDD item reachable in Phase 2 (retrieved/tool
  injection, pasted-document directives, tool-based escalation, authority
  downgrade, cross-authority supersession/dedup, cross-session/cross-task
  lookup, fenced/quoted/comment smuggling, Unicode look-alike keywords,
  CRLF/BOM tricks, over-long IDs, attribute injection).

### 29. PR #5 review round 2: SEC-2.1/2.2, DUR-2.1/2.2/2.3, further SPEC-2.x fixes (all landed; recorded here per SPEC-3.3)

A second external review of the PR (`r5-sec2.md`, `r5-dur2.md`,
`r5-spec2.md`) found further gaps in the final-pass head §26-§28 recorded;
all are now fixed and landed, but — per SPEC-3.3 — none of the rulings,
mechanisms, or new tests below had been recorded in this ADR until now.

- **SEC-2.1: an over-limit new event never enters the write transaction,
  copies, or hashes its payload (refines §13, D17/F3).** The prior
  admission path validated sizes only after `Clone`/`PayloadHash`, so a
  large new event still paid that cost before rejection.
  `internal/ingest/sizes.go`'s `checkSizes`/`hardSizes` now run first,
  from lengths alone, outside any write transaction: a hard,
  non-configurable ceiling (`hardMaxSpans`, `hardMaxParts`,
  `hardMaxEventBytes`) is checked unconditionally, even against a retry of
  a known `EventID`; then the configured limits are checked, and an
  over-limit event proceeds past them only as the retry of a known
  `EventID`, established by a cheap `View` read of its receipt (never a
  full `Clone`/hash) — a new or anonymous over-limit event is rejected
  right there (`internal/ingest/ingest.go:83-124,155-176`). Item and
  attribute limits are still checked inside the transaction, after
  `PayloadHash`, as before — this gate covers only span/part counts and
  span/blob/event byte totals, the ones cheap to check from lengths
  alone. An over-limit event whose `EventID` matches a Phase-1-era record
  with no receipt now fails the limit check, not `ErrEventIDConflict` (the
  limit check runs first). Tests:
  `TestOverLimitNewEventsStayOutOfWriteTx_SEC21` (a counting store wrapper
  asserts zero `Update` calls for an over-limit new/anonymous event, and
  that a known retry still succeeds even over the limits, and that the
  hard ceiling rejects even a retry before any transaction),
  `TestSizeGateMatchesValidateFor_SEC21` (`checkSizes` accepts exactly what
  `Event.ValidateFor`'s limit accounting accepts, at the edge, on both
  sides) — both `internal/ingest/fixes_r1_test.go`.
- **SEC-2.2: an ambiguous lifecycle record's diagnostic and a
  boundary-conflict diagnostic are readable only where their causes are
  (completes SEC-1.3, refines §7/§1/§20, D10/D1/R13/R14; text corrected,
  SPEC-4.2 — see §31 below for what this round's SEC-3.2 changed about the
  *command record* itself).** `graph.AuthorizeLifecycleCommand` now
  reports the access boundaries of the accessible current versions that
  made a target ambiguous, and `graph.CheckBoundaryConflict` returns the
  conflicting version's boundary alongside `ErrBoundaryConflict`
  (`internal/graph/graph.go`, `internal/graph/lifecycle.go`). Ingest
  stamps the `AMBIGUOUS` diagnostic and the `boundary_conflict` diagnostic
  at the base (transcript) boundary intersected with every cause boundary,
  rather than the base boundary alone — so another agent can no longer
  learn that a version it cannot see exists, merely because it caused an
  ambiguity or conflict the agent *can* see the outcome of.
  **The `AMBIGUOUS` *command record* is a different case, changed again by
  SEC-3.2 (§31): it no longer narrows its own `Access`; instead its
  resolution is what narrows, via `DetailAccess`, and a viewer outside it
  reads the record redacted (`WITHHELD`), not omitted** — so "another
  agent... shows neither record" no longer describes the command-record
  half correctly: agent B now reads the `AMBIGUOUS` command record too,
  redacted. The source actor whose write caused it reads every record and
  diagnostic unredacted. Test:
  `TestRecordsNeverRevealHiddenVersions_SEC22`
  (`internal/ingest/fixes_r1_test.go`, **SPEC-4.2: description corrected to
  match the test's current assertions**) — sets up both cases (an
  agent-private `[plan]` making a task-wide `[plan]` ambiguous; an
  agent-private `[q]` boundary-conflicting with a task-wide restatement),
  then asserts the SEC-3.2 redaction property with a third pair in the same
  test (a hidden vs. a merely missing Unpin target): a different agent's
  `LifecycleCommands`/`Diagnostics` read is structurally identical for the
  hidden and the missing case — one redacted command record and the same
  diagnostics either way — while the source actor's own read still shows
  the full resolution.
- **DUR-2.1: bounded-lookup and bounded-cursor work, addressing part of
  SPEC-2.1's remaining store-side gaps (refines §26, F1; completed by
  SPEC-3.1, §30 below).** Two of three fixes in this batch landed here:
  (1) `internal/store/sqlite/access_lookups.go`'s lookups now read `(seq,
  item_id)` rows in `LIMIT`-bounded batches resumed from a cursor and load
  full items only until they have what they need (one blob referrer,
  `limit+1` candidates, one page plus one), instead of loading every
  matching row before stopping; each lookup's SQL comes from a named
  builder the plan guard runs, requiring an exact-key index search *and*
  a `LIMIT`. (3) `internal/ingest/references.go`'s
  `linkPageLimit()` sizes one reference-lookup page by
  `min(lookupLimit, MaxReferenceLinks-refLinks+1)` — the remaining budget
  plus one, never the full `LookupLimit` — so a dropped linkable
  candidate is still seen and reported as truncation, and a page never
  loads thousands of items to write a handful of edges. **The third
  fix — `internal/store/memory/index.go`'s `orderedIndex.commit` — is
  recorded in §30 below, not here:** this round's own attempt at it
  (merging via a cursor) turned out not to be enough on its own (SPEC-3.1
  item 3 found it still rebuilt each touched key's list), so the
  properly-bounded version is §30's fix, not this one. Tests:
  `TestLookupsDoBoundedWork` (memory and SQLite — finding one blob
  referrer and one page of sources loads a handful of items regardless of
  session size, and a duplicate leaves the blob index);
  `TestSourcePageSizedByBudget_DUR21` (`internal/ingest/references_test.go`
  — a `MaxReferenceLinks: 5` event linking 3 sources then querying for more
  requests exactly `[6, 3]` items per page, the remaining budget plus one,
  not the full lookup limit).
- **DUR-2.2: truncation is reported only for a candidate that could
  actually have been linked (refines §16/§13, ruling 1).** `linkReference`
  checked the `MaxReferenceLinks` budget before the access/`Within`
  checks, so once the budget was spent, *any* further candidate reported a
  spurious truncation — even one that was never linkable in the first
  place (missing, inaccessible, or out of boundary). It now checks
  linkability first and only reports truncation when a linkable candidate
  meets an exhausted budget. Test:
  `TestReferenceLinkTruncationReporting_SPEC23_DUR22`
  (`internal/ingest/references_test.go`) — covers this alongside SPEC-2.3
  below, since both bugs are in the same budget-vs-truncation path.
- **DUR-2.3: the `MaxReferenceLinks` field comment now says "per event"
  (refines §13).** Reworded from "per References entry or new source" to
  match `run.refLinks`, which is event-wide and never reset, and ADR 19
  §13/§28's own description of the ruling.
- **SPEC-2.3 (completing the F4/item-ID reference path): a truncated
  item-ID reference is diagnosed too (refines §16, F4).**
  `referenceItemID` discarded the `stop` result `linkReference` returned,
  so an item-ID reference dropped at the budget reported no
  `ReferenceLinksTruncated` diagnostic, unlike the locator path. It now
  reports on `stop`, the same as every other path. Test: covered by
  `TestReferenceLinkTruncationReporting_SPEC23_DUR22` above (an item-ID
  case alongside the locator case).
- **SPEC-2.5 (confirms F5, ADR 3's migration-0011 Go-step recording): the
  registered function itself is now pinned, not only the step ID.**
  `TestCommittedStepsUnchanged` (`internal/store/sqlite/steps_test.go`)
  now also asserts the registered step's function name via
  `runtime.FuncForPC(reflect.ValueOf(fn).Pointer()).Name()` and the SHA-256
  of `steps_0011.go`, closing the gap where step 11's registry entry could
  be repointed at a different function without failing any test.
- **Regression guards added alongside the fixes above, so a future
  revert of any bounded-read property fails a test, not only a manual
  timing probe:** `TestKeyedReadsDoNotScan`
  (`internal/store/memory/scan_test.go` — `CurrentVersions` and
  `ObligationsBySource` read only their key's index entry, never a
  whole-table iteration, with hundreds of unrelated pointers/obligations
  present); `TestObligationReadsUseIndex`
  (`internal/store/sqlite/access_lookups_test.go` — `ObligationVersions`
  plans on `(session_id, id)`, `ObligationsBySource` on `(session_id,
  f_source_item_id)`); `TestHotReadsUseTheirBuilders`
  (`internal/store/sqlite/hotreads_test.go` — the reads graph/ingest issue
  per item run exactly the plan-guarded named builders, so reverting one
  to an ad hoc session-wide query fails here even if the plan test alone
  would not catch it); `TestSupersessionCycleCheckIsLocal`
  (`internal/store/sqlite/supersession_test.go` — a new version with no
  incoming `SUPERSEDES` edge visits nothing however many unrelated chains
  the session holds, and a real cycle is still found after visiting only
  its own chain).

### 30. PR #5 review round 3: SPEC-3.1 (all five items fixed), DUR-3.1, SPEC-3.2 (all landed)

A third external review round (`r5-spec3.md`) found that §29's fixes left
several reads still growing with session size, plus a paging bug and gaps
in the regression guards §29 added. All are now fixed; per the commander's
relay, this section supersedes §29's DUR-2.1 hedge above with the
completed picture — no deferral entries, since every SPEC-3.1 item is
fixed.

- **SPEC-3.1 item 1: retiring an item's lookup rows no longer searches the
  whole session (refines §26, F1/DUR-1.1).** Every `SUPERSEDES`/`DUPLICATE_OF`
  edge deletes the retired item's rows from `lookup_canonical`,
  `lookup_working`, `lookup_source`, and (on a duplicate) `lookup_blob`;
  their primary keys start with the lookup key, not the item ID, so those
  `DELETE`s searched the whole session. Migration
  `0017_lookup_item_indexes.sql` adds a `(session_id, item_id)` index to
  each of the four tables. Test: `TestRetireLookupsUseIndex`
  (`internal/store/sqlite/access_lookups_test.go`, via the named
  `retireLookupSQL` builder the plan guard runs).
- **SPEC-3.1 item 2: one large event is no longer quadratic in SQLite,
  and the fix's own two follow-on gaps are closed (refines §5/§7, D8/D10;
  this bullet is the final, converged behavior — SPEC-4.1/SPEC-4.4 changed
  it twice more since the paragraph below was first written).** Every
  derived item's `graph.LinkDerived` loaded and fully decoded its span's
  growing transcript item again, and `InsertRelationship` did the same to
  check both endpoints exist — for `n` derived items from one transcript,
  that is `O(n)` reloads of a transcript whose own size is also growing,
  i.e. quadratic. The fix: each `*transaction` keeps an `itemCache` of
  items it has already decoded and verified this transaction (`Item`
  returns a clone from the cache when present, populates it otherwise), so
  a transcript is decoded once per transaction rather than once per
  derived item — `UpdateItem` deletes its entry (`write.go:100`, the next
  read decodes the updated row), and a rolled-back store-method savepoint
  clears the whole cache, so a cached value is never stale relative to
  what the transaction itself has written.
  **SPEC-4.1 (D17): the cache is now bounded, not unbounded.** A first
  version cached every item `Item()` ever decoded for the life of the
  transaction with no limit — `scanLookup` called `Item()` for every
  indexed row it paged through, including rows the viewer can't see, so
  one transaction paging thousands of large sourced items grew ingest
  memory with the session's matching items, not with the event (a
  resource bound moved from time to memory, D17's concern either way).
  `itemcache.go`'s `itemCache` is now a genuine LRU, capped at 1024
  entries and a baseline 16 MiB of item text. The byte cap grows to
  twice the largest transcript read in the transaction, so a configured
  `MaxSpanBytes` above 16 MiB does not disable transcript caching;
  non-transcript items cannot raise it, and an item over the current cap
  is never cached. `scanLookup` now
  calls the internal `loadItem(id, cache=false)` instead of `Item`, so
  paging past many matches neither grows the cache nor evicts the one
  transcript derived-linking actually re-reads. **SPEC-4.4: `InsertRelationship`
  verifies both endpoints again, not only that they exist.** SPEC-3.1
  item 2's own first fix replaced the endpoint check with an
  existence-only primary-key probe to avoid a full decode — but that
  silently dropped the pre-existing guarantee that linking a corrupted
  legacy item (e.g. migration 0001's `�`-repaired text) fails
  `ErrIntegrity` rather than linking it. `InsertRelationship` now loads
  each endpoint through `loadItem(id, cache=true)` (`domain.ErrNotFound`
  becomes `ErrDanglingRelationship` as before; a verification failure is
  `ErrIntegrity`), so both endpoints are decoded and hashed at most once
  per transaction while cached rather than once per edge — the same cache
  that already keeps the transcript hot, so this restores the integrity
  guarantee without reopening the quadratic cost SPEC-3.1 item 2 fixed.
  `store.go`'s `InsertRelationship` contract now states the integrity
  requirement explicitly. Test:
  `TestInsertRelationshipRejectsCorruptEndpoint_SPEC44`
  (`internal/store/sqlite/endpoint_integrity_test.go`, corrupt endpoints
  on either side fail with `ErrIntegrity` and write no edge).
  Store-level item bytes loaded, 500 vs.
  4000 derived items from one transcript: x65.5 before caching, x8.0 with
  the (then-unbounded) cache (linear in item count, not transcript size
  too); end-to-end SQLite ingest of one event, 500 vs. 4000 Pinned items:
  x32 before, x9.1 with the unbounded cache, x10.5-x10.8 with the current
  bounded cache plus restored endpoint verification (still linear, not the
  original x32). Tests: `TestDerivedLinksLoadTranscriptOnce`
  (`internal/store/sqlite/scaling_test.go`, the store-level bytes-loaded
  check); `TestOneLargeEventScalesLinearly`
  (`internal/store/sqlite/event_scaling_test.go`, **SPEC-4.2: previously
  uncited** — the same property end to end through `Ingester.Ingest`,
  asserting about x8 for 4000 items vs. 500, not the roughly x32 a
  per-item transcript reload costs); `TestItemCacheBounded_SPEC41`
  (one transaction pages `SourceItems` over 48 sourced items of 512 KiB
  each — 24 MiB total, above the byte cap — then reads each directly; the
  cache's own entry/byte footprint, exposed via `itemCacheFootprint`,
  never exceeds its caps at any stage), `TestItemCacheEntryCap_SPEC41`,
  `TestItemCacheLRU` (eviction order, oversize-item exclusion, and byte
  accounting), `TestLookupScanDoesNotFillItemCache_SPEC41` (lookup pages
  do not fill or evict the point-read cache) — all
  `internal/store/sqlite/itemcache_test.go`;
  `TestLargeTranscriptStaysCached_SPEC41`
  (`internal/store/sqlite/scaling_test.go`, a 17 MiB transcript is decoded
  once across eight derived links, with a 34 MiB byte cap);
  `TestRolledBackMethodClearsItemCache`
  (`internal/store/sqlite/itemcache_test.go`) locks the savepoint-rollback
  cache clear above, previously asserted only in prose.
- **SPEC-3.1 item 3: the memory ordered-index commit is now proportional
  to the change, not the key's existing size (refines DUR-2.1 above,
  which this supersedes).** `orderedIndex.commit` previously rebuilt each
  touched key's *entire* list from an iterator on every commit that
  touched it — DUR-2.1's own "merge from a cursor" fix did not change
  this. It now merges the per-transaction overlay into the committed list
  in place: a removal (now located by its `Seq`, tracked in `gone
  map[K]map[string]seqRef`) is deleted by binary search, and an addition
  is appended when it is newest (the common case, since additions are
  usually new) or inserted by binary search otherwise. **(SPEC-4.5:
  corrected)** This is not unconditionally "never proportional to the
  key's existing entry count" — `slices.Delete`/`slices.Insert`
  (`index.go:209,224`) still move every entry after the affected position,
  so a removal, or an addition that is not the newest, costs work
  proportional to how much of the list sits after it. It is the common
  case this fixes: an addition is (almost) always newest in practice
  (append-only ingestion), and this no longer costs a full-list rebuild
  regardless of position, which is what made *every* commit proportional
  to the key's size before. `Update` holds the session's write lock for
  the whole transaction, so an in-place edit to a committed slice never
  races a concurrent `View`. Committing one more referrer (append case) of
  a blob that already has 20,001: 347µs before, 4.7µs after. Test:
  `TestOrderedIndexCommitMerges`
  (`internal/store/memory/bounded_test.go`).
- **SPEC-3.1 item 4: SQLite lookup cursors now seek inside the index
  search instead of sorting a union (refines §26, F1).** A lookup's owner
  filter was an `IN` list per owner column, combined with an `OR`
  cursor-comparison predicate; SQLite planned this as a `TEMP B-TREE` sort
  over every match, discarding the index's own order, so a page near
  position 20,000 of 20,001 cost as much as scanning them all.
  `ownerCombos` now enumerates the (at most eight) exact `(workflow, task,
  agent)` owner combinations a viewer (or a viewer pair, for
  `BlobReferrer`'s within-check) permits, and each lookup runs one exact
  query per combination with a `(seq, item_id) > (?, ?)` (or `(f_seq, id)
  > (?, ?)` for references) row-value range that SQLite applies *inside*
  the index search, merging the combinations' batches in `(Seq, ID)` order
  in Go. A page of 1 after a cursor near position 19,998 of 20,001: 2.68ms
  before, within noise of the first page after. Test:
  `TestLookupCursorsSeek` (`internal/store/sqlite/access_lookups_test.go`,
  via `assertSeeks`, which fails unless the plan searches an index with
  the cursor inside its constraint and sorts nothing).
- **SPEC-3.1 item 5: memory relationship reads are keyed by `(type,
  endpoint)`, not endpoint alone (refines §7, D10 — predates this PR, on
  the dedup path).** `relsFrom`/`relsTo` were `map[string][]string` keyed
  by endpoint only, so reading `SUPERSEDES` into an item with thousands of
  unrelated `DUPLICATE_OF` edges into the same item walked all of them.
  Both maps are now keyed by `relKey{Type, ID}`; a read by endpoint alone
  (no type filter) probes keys for every type returned by
  `domain.RelationshipTypes()`, which `RelationshipType.Valid` also uses.
  `Relationships(SUPERSEDES, ToID=c)` with 8,000
  `DUPLICATE_OF` edges into `c`: 257µs before. Test:
  `TestRelationshipsReadTheirTypedKey`
  (`internal/store/memory/scan_test.go` — a read by type and endpoint
  walks at most one index entry and scans zero edges, counted via the
  index's own yield counter, with 1,000 unrelated `DUPLICATE_OF` edges
  into the same target present);
  `TestUntypedEndpointReadsAllRelationshipTypes_SPEC31` (iterates the
  domain list and fails if either endpoint read misses a new type).
- **DUR-3.1: `SourceItems` reports each unverified ID on exactly one page,
  not once per page it happens to be skipped past (refines DUR-1.4,
  §26).** A page reads one row past its limit only to learn whether more
  remain; an unverified row skipped between the page's `Next` cursor and
  that extra row was still recorded in `Unverified` on *this* page, and
  the next page — which resumes at `Next` — read and reported it again.
  `scanLookup` now also returns the cursor position of every unverified
  row it skipped; `SourceItems` keeps only those at or before its actual
  `Next` (via the new `after` cursor-order helper) and defers the rest to
  the next page, which will see them again starting from `Next`. Tests:
  `TestSourceItemsReportsUnverifiedOnce`
  (`internal/store/sqlite/access_lookups_test.go` — pages one at a time
  through sources interleaved with altered rows and asserts each
  unverified ID is reported exactly once across every page); **(SPEC-4.2:
  previously uncited)** `internal/ingest`'s complementary, ingest-level
  half of the same fix — `r.unverified map[string]bool` (`run.go`) records
  each unverified ID once per event, for both lookups
  (`reportUnverified`) and Working snapshots (`workingSection`), so even a
  store that (like SQLite's page lookahead, or a future store) reports the
  same ID from more than one call still yields one `ItemUnverified`
  diagnostic in the receipt, not one per sighting. Test:
  `TestUnverifiedReportedOncePerEvent_DUR31`
  (`internal/ingest/lookups_test.go`).
- **SPEC-3.2: regression guards for every SPEC-2.1 read, closing the gaps
  the second review found, plus the four further gaps a third review round
  later found in these same guards (refines §29; SPEC-4.3's fixes folded
  in below, per the commander's relay).** (1)
  `TestKeyedReadsDoNotScan` (memory) is extended past `CurrentVersions`/
  `ObligationsBySource` to also cover per-item `Relationships`/`Items`
  reads, keyed or full-scan (**SPEC-4.5: corrected — its fixture carries
  no relationships, so it cannot also guard the type-keying property**);
  `TestRelationshipsReadTheirTypedKey` (`internal/store/memory/scan_test.go`)
  is the guard for SPEC-3.1 item 5 specifically: it counts both the typed
  index's own yields and its underlying scan, so serving a typed read from
  an endpoint-only or type-only index — either would return the right
  answer while walking the wrong amount of work — fails it. **(SPEC-4.3)**
  Its fixture initially carried no *unrelated* edges of the queried type,
  so a type-only index still returned the right (empty) answer without
  extra work and the mutation survived; it now adds 200 unrelated
  `SUPERSEDES` edges into the same target first, so a type-only index
  visits them and fails. (2) `TestSupersessionCycleCheckIsLocal` now
  measures the cycle check *from outside* the function it tests: every
  `*transaction` records `rowsRead`
  across every multi-row query the transaction issues (a `countedRows`
  wrapper around `*sql.Rows` behind the shared `t.query` helper every
  multi-row `SELECT` — `queryRecords`, `listRecords`, `nextRows` — now
  goes through), so a mutation that made the check load the whole
  `SUPERSEDES` graph but still *report* only the nodes it walked (which
  the prior guard, trusting the function's own `visited` count, could not
  catch) now fails on rows actually read. **(SPEC-4.3)** `rowsRead` was
  originally asserted only around the insert that returns before walking
  (a new version with no incoming edge); it now also bounds the
  cycle-closing insert and a walking insert that finds no cycle, so
  loading the whole graph inside the walk while reporting only the
  visited count (a mutation only these two cases could catch) fails too.
  (3) `CurrentVersions` gets its
  own named builder (`currentVersionsQuery`), a plan guard
  (`TestCurrentVersionsUseIndex`, **SPEC-4.2: previously uncited** —
  `internal/store/sqlite/access_lookups_test.go`, locking the read to its
  `(session_id, task_id, namespace, directive_id)` primary-key prefix),
  and joins `ObligationsBySource` in `TestHotReadsUseTheirBuilders`'s
  per-item-read guard, so reverting either to an ad hoc query fails there
  even if the plan test alone would not catch it. (4)
  `TestUpgradeOrderedGraphIndexes` (`internal/store/sqlite/upgrade_test.go`)
  is the migrated-layout parity fixture for 0014→0015: a
  relationship and items stored before 0015 are read back correctly
  through the new `relationship_from_seq`/`relationship_to_seq`/
  `item_task_seq` indexes after upgrade, and the three indexes 0015
  replaced are confirmed gone. **(SPEC-4.3) It stopped at 0015 — 0016 and
  0017 had no upgrade-parity test at all.**
  `TestUpgradeCommandDetailAndLookupItemIndexes`
  (`internal/store/sqlite/upgrade_test.go`) closes this: upgrading from
  14, 15, and 16 each to head confirms `rec_command`'s five
  `f_detail_access_*` columns (0016) and all four `lookup_*_item` indexes
  (0017) exist afterward, a command record stored before 0016 reads its
  NULL detail columns back as the zero `DetailAccess` and still validates,
  and (from 14) the same fixture also exercises 0015's indexes in the same
  pass.
  **(5, SPEC-4.3) The memory ordered-index commit's own regression guard
  didn't check *how* it stayed fast.** `TestOrderedIndexCommitMerges`
  counted only `commitWork`'s own increments, so a mutation that kept
  `commitWork` low by `slices.Clone`-ing the touched key's list first (an
  extra full-list copy on every commit, defeating the point) survived. It
  now also asserts the key's backing array is unchanged after a commit
  that only removes then adds within capacity — a copy would move it to a
  new array. A mid-list removal or non-newest addition still legitimately
  shifts the tail in place (no allocation, but not free either — §30 item
  3 above records this honestly).
  **(6, SPEC-4.3) No committed test paged a lookup across more than one
  owner combination.** `TestConformance/SourceItemsMixedOwners`
  (`internal/store/storetest/access_lookups.go`) interleaves visible
  items across five owner-boundary shapes (session, workflow, task,
  agent, task+agent) with hidden ones and pages at sizes 1-4 on both
  stores, requiring one merged `(Seq, ID)` order with no duplicates or
  skips — dropping the per-combination merge in SQLite's `nextRows` fails
  it.

### 31. PR #5 review round 4: SEC-3.1 (retry admission hardened), SEC-3.2 (command-detail redaction) — all landed, recorded here per SPEC-4.2

A fourth external review round found that round 3's admission gate and
round 1's diagnostic/command narrowing (F6, §26) each had a residual gap;
both are now fixed. Migration 0016 adds the columns SEC-3.2 needs; ADR 3
records it in the migration list. No SDD amendment is needed for either:
FR-ING-006's retry identity is unchanged (SEC-3.1 only narrows *which*
retries are admitted before validation, not what counts as a match), and
FR-DIR-005 doesn't specify diagnostic readership, so narrowing where its
unknown-target diagnostic is readable is this ADR's decision to make, the
same as F6/SEC-1.3/SEC-2.2 already were.

- **SEC-3.1: an over-limit event can no longer smuggle an oversized
  payload past the limit gate by reusing a known `EventID` with a
  different payload (refines §29's SEC-2.1, §13, D17/F3).** §29's
  admission gate let an over-limit event through as a retry once it found
  *any* receipt stored under the caller's `EventID` — it never checked
  that the new event actually matches what that receipt was for. A
  different principal, or the same principal with a larger payload under
  the same `EventID`, could therefore still reach `Clone`/`PayloadHash`/the
  write transaction. `admitKnownRetry` (`internal/ingest/sizes.go`) now
  requires, from cheap reads alone before anything is copied or hashed:
  the stored receipt's `Principal` must equal the caller's, and the new
  event's *shape* — `Kind`, `TurnBoundary`, span count, and per-span
  authority/part-count/every-byte-length (`sameShape`) — must equal the
  stored envelope's exactly. Anything else is a bare `ErrEventIDConflict`
  from the read, never a limit error, so a mismatched retry attempt
  discloses nothing about why it failed; an exact retry still replays
  (F3), for the same principal only. Test:
  `TestKnownEventIDCannotSmuggleOversizePayload_SEC31`
  (`internal/ingest/fixes_r1_test.go`) — the same principal and a
  different principal both fail `ErrEventIDConflict` when retrying a known
  `EventID` with an oversized payload, and neither enters a write
  transaction.
- **SEC-3.2: a lifecycle-command record's resolution is redacted outside a
  narrower detail boundary, instead of the whole record narrowing (refines
  F6/§26, SEC-1.3/SEC-2.2/§29; supersedes both for the *command record*
  specifically — see the corrections above).** F6/SEC-2.2 narrowed a
  command record's own `Access` to the intersection of every cause
  boundary. That means the record's very *existence* — not only its
  resolution — became invisible to a reader outside that intersection, so
  a lifecycle command a source actor issued could vanish entirely for
  another reader of the same transcript, an inconsistency with every other
  record type (which is always readable at its transcript boundary; only
  *some* fields narrow further). `domain.LifecycleCommandRecord` now
  carries a separate `DetailAccess` field: `Access` stays at the
  transcript boundary always (`rec.Access = c.transcript.Access`,
  `internal/ingest/derive.go:351`, never narrowed), and `DetailAccess` is
  computed per outcome via `causeAccess` — `RESOLVED`/`MISMATCH` narrow to
  the resolved target's boundary, `AMBIGUOUS` to the intersection of every
  candidate's boundary, and **`NOT_FOUND` narrows to the *source actor's
  own* boundary** (`actorOwn`, `internal/ingest/derive.go:356-365`) — a new
  narrowing this round adds, since a genuinely missing target has no
  target boundary to narrow to. A reader outside `DetailAccess` calls
  `LifecycleCommandRecord.Redacted(viewer)` (`internal/domain/diagnostic.go`)
  and gets the record back with `Resolution: WITHHELD`, no resolved item
  ID or version, and `DetailAccess` reset to `Access` — the same shape
  whether the real resolution was hidden or the target never existed, so
  `WITHHELD` never itself discloses which. `TargetWithheld` is a value
  ingestion never stores; it exists only as `Redacted`'s output.
  `internal/store/sqlite/ingestion.go` and `internal/store/memory/read.go`
  both call `Redacted(f.Viewer)` on every record `Access` already permits,
  in `LifecycleCommands`. Migration `0016_command_detail_access.sql` adds
  `rec_command`'s five `f_detail_access_*` columns; a record written
  before 0016 reads them back NULL, which decodes as the zero
  `AccessBoundary`, which `Redacted` treats as "no narrowing beyond
  `Access`" (M8: pre-existing records keep exactly the visibility they had
  before `DetailAccess` existed, never an invented narrower or wider
  default). Test: `TestCommandRecordDetailRedaction`
  (`internal/domain/diagnostic_test.go`).

## Open questions

Five open questions, all resolved by commander ruling:

- **Classification-table versioning (§2/M2) — resolved.** The table gets
  its own version number, independent of the parser version: Phase 3 will
  add sections the table doesn't yet cover, and coupling the two would
  force a parser bump for a table-only change.
- **References base-directory policy (§16) — resolved, deferred.** The
  default lexical-identity rule this ADR assumes is sufficient for Phase 2;
  a configurable base-directory/namespace policy is deferred until a
  multi-repository session actually exists, not designed speculatively now.
- **`MutationGrant` exact-target-ID matching (ADR 16, touching §9) —
  resolved, out of scope here.** Whether it is expressive enough for a
  Phase 3 matcher-registry grant scoped to "any target satisfying
  obligation claim X" stays with ADR 8 / Phase 3; it is not this ADR's or
  Phase 2's decision to make.
- **SDD §8 `Runtime.Ingest` signature amendment (§19, deferred to Phase 5
  by R3) — resolved: the disposition is decided, the drafting is
  deliberately deferred (SPEC-2.2: "drafted in a later round" read as
  already done, which contradicted the sentence beneath it — reworded).**
  The amendment will be added to this ADR as an explicit **unapplied**
  proposal — a "Required SDD amendment" subsection, not "applied" — once
  `p2-ingest`'s `domain.IngestReceipt` shape has actually stabilized
  against the landed contract/parser/store/graph work; drafting it now,
  before that shape exists, would risk proposing a signature Phase 2's own
  implementation then contradicts. **Not yet drafted as of this ADR
  revision — that absence is the resolution, not a gap in it:** the open
  question was *whether* to draft it now or later, and the ruling was
  later, once Phase 5 actually implements `Runtime.Ingest`.
- **`graph.Supersede`'s per-event audit-ID collision (§7, Consequences) —
  resolved.** `p2-graph` versions the audit identity (rather than
  restructuring D11's planned edge set to guarantee at most one retirement
  of a given old target per event); this ADR's requirement is unchanged —
  the collision must not occur — only the mechanism is now fixed.

## SDD amendment (applied in v0.9)

Five clarifications, applied in SDD v0.9 (R4):

- **FR-ING-004 (parse-unit isolation, §5/M1):** added: "Each span, and each
  separate text part within a span, is an isolated parse unit:
  fenced/quoted/comment suppression state never carries across a span or
  part boundary, adjacent spans or parts are never concatenated into one
  directive token, and an image or document part interrupts text
  parsing."
- **FR-ING-005 (Working-snapshot duplicate identity, §7/D11):** added: "A
  Working snapshot's duplicate identity is the complete snapshot
  membership and its members' effective metadata; equal member text in an
  otherwise changed snapshot does not by itself make that member a
  duplicate."
- **FR-DIR-002 (derived-ID-shaped explicit ID rejection, §6/D20):** added:
  "An explicit [id] that has the derived-ID shape (a lowercased keyword, a
  hyphen, and 64 lowercase hex digits) is rejected with an
  ErrMalformedDirective diagnostic and the item is dropped, never assigned
  a derived ID, so explicit and derived IDs can never collide." **(SPEC-3.4:
  this is the original v0.9 insertion, restored verbatim — SPEC-2.7's
  wording fix below is a separate, later amendment, not a rewrite of this
  historical quote.)**
- **FR-DIR-005 (unsupported-lifecycle vocabulary, §1/§4/M4):** added: "The
  unsupported-lifecycle vocabulary recognized for this diagnostic is
  Archive, Unarchive, Promote, Demote, Block, Unblock, Waive, CompleteTask,
  and Reopen; other headings are not lifecycle commands at all and remain
  ordinary content or DirectiveNotParsed diagnostics under FR-DIR-006."
- **FR-DIR-006 (ttl representation bound, §8/D12/R1):** added, in the
  attribute-allowlist paragraph: "ttl accepts ASCII decimal digits (leading
  zeros allowed) with a value from 1 to 2147483647 inclusive, parsed with
  checked arithmetic; a syntactically valid but larger value fails event
  validation with a representation-limit error rather than being ignored
  or treated as unlimited."

These five are the exact insertions applied to SDD.md as v0.9; see that
file's v0.9 diff for placement.

**A sixth, later SDD.md edit (2026-09-26; SPEC-1.13/SPEC-2.4; date added
per SPEC-4.5, previously missing), not part of the
original v0.9 batch above and previously unrecorded here:** commit
`18e1b69`, after Phase 2 code landed and this ADR's own Status was still
`Proposed`, added "ADR 19." to §11 item 2's phase-2 ADR list and item 19
("Directive parsing and ingestion …") to §15's ADR list — the listing
fix this ADR's own Review section (below) and `docs/adr/README.md`
already describe as a commander ruling, but that this subsection had not
itself recorded as an SDD edit. No FR/INV text changed; only the two
phase/ADR index lists gained ADR 19's entry, keeping SDD.md internally
consistent with an ADR that otherwise existed but was absent from both
lists.

**A seventh, later SDD.md edit (2026-09-26; SPEC-2.7, then repaired here
per SPEC-3.4; date added per SPEC-4.5, previously missing): FR-DIR-002's
derived-ID-shape wording, scoped to content-section
keywords.** Commit `404c379` changed three FR-DIR-002 sentences (this is a
content change, unlike the sixth edit above — the FR-DIR-002 bullet in the
original five above is deliberately left quoting the *pre-404c379* text,
so that historical record stays an exact substring of what v0.9 actually
inserted). The current SDD.md FR-DIR-002 sentences, exactly:
- "An item without an ID receives a derived ID (a lowercased
  content-section keyword, a hyphen, and the 64 lowercase hex digits of
  its content hash), reported in diagnostics and inspection."
- "An explicit [id] that has the derived-ID shape (a lowercased
  content-section keyword, a hyphen, and 64 lowercase hex digits) is
  rejected with an ErrMalformedDirective diagnostic and the item is
  dropped, never assigned a derived ID, so explicit and derived IDs can
  never collide."
- "Only the six content-section keywords (Pinned, Working, Remember,
  Ephemeral, References, Goal) derive IDs this way; a lifecycle keyword
  (Resolve, Unpin) never derives one, so `resolve-<64hex>`/`unpin-<64hex>`
  are ordinary explicit IDs, not derived-ID-shaped rejections."

Rationale: the original "a lowercased keyword" read as covering every
directive keyword including Resolve/Unpin; only the six content sections
ever derive an ID (`internal/directive/items.go`'s `derivedShaped`), so
`resolve-<64hex>`/`unpin-<64hex>` are ordinary explicit IDs on a lifecycle
command, never derived-ID-shaped rejections (§22's R17 bullet). SDD.md's
header still reads `Version: 0.9` — this is recorded as a post-v0.9
clarification rather than a version bump, consistent with the sixth edit
above.

## Review

This ADR records the commander's disposition of an adversarial decision
review (Codex gpt-6-astra xhigh) of the Phase 2 brief against the merged
Phase 1 codebase and SDD v0.8: D2/D4/D9 agreed as written; D1, D3, D5-D8,
D10-D19 amended; M1-M8 added as missing decisions; all accepted, with
commander rulings R1-R8 (`phase2-amendments.md`) overriding eight of them,
followed by seven further rounds of worker questions and rulings (R9-R21)
as recorded above.

**(SPEC-1.4, 2026-09-26) Phase 2 code landed** across `internal/domain`,
`internal/policy`, `internal/directive`, `internal/store` (memory and
SQLite), `internal/graph`, and `internal/ingest`, and the decision groups
above were re-verified and corrected against it in place (each correction
marked "SPEC-1.4" or "landed" inline) rather than left describing a
pre-implementation state. A first external review of the resulting PR
(`round1-p5-fixes.md`, §26-§28 above) found further gaps, all now fixed by
their owning workers and merged into this ADR's final-pass head
(`p2fix/adr` at commit `dd9813d`, integrating every `p2fix/*` branch):
F1-F6, every named DUR/SEC/SPEC/TEST finding, and the SPEC-1.10/SPEC-1.11
rulings are all landed and recorded above with grep-verified test
citations, not intended designs.

**(2026-09-26) Final reconciliation pass complete.** Every statement in
this ADR was checked against the code at the merged integration head, not
assumed from a commit message or an earlier draft; three genuine drift
points were caught and fixed in the process — F1's access-filtered lookup
redesign superseded the R19 lookups this ADR (and ADR 3) had described as
current (§7/§9/§13/§15/§16/§23, §26); SPEC-1.6's diagnostic/reason pairing
is now mechanically enforced by a `reasonCode` map, not merely a call-site
convention (§13/§28); and SPEC-1.11's fix, R21's fix, and R20's fix had
all landed since this ADR's previous revision described them as pending.
This ADR is ready for the commander to move from Proposed to Accepted at
Phase 2 exit, pending only the open questions recorded above (§ Open
questions). **(2026-09-26) The SDD §11/§15 amendment SPEC-1.13 flagged is
applied:** by commander ruling, SDD §11 item 2 now names "ADR 19." and
§15 lists it as item 19 ("Directive parsing and ingestion (grammar
deviations, parse units, ingestion pipeline, receipts, dedup/replacement/
snapshots, lifecycle parse/authorize)"), so Phase 2 can exit with this
ADR accepted; docs/adr/README.md is updated to match, and this is no
longer an open item.
