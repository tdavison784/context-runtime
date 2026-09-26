# 19. Directive parsing and ingestion

Status: Proposed
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

## Decision

Grouped by subsystem. Each group names the requirements it answers, the
accepted decision, and which package(s) in the work split own it.

### 1. Lifecycle commands: parsed and authorized, not executed (D1, R7)

FR-DIR-005, FR-AUTH-001/002, §8/9, trace T06.

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
SYSTEM/HARNESS→`instruction`) plus zero or more directive items, each
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
*shape* of a derived ID (lowercase keyword, hyphen, 64 lowercase hex) is
rejected with `ErrMalformedDirective` and the item is dropped — not parsed
with a fallback derived ID — so an explicit ID can never collide with, or
be mistaken for, a derived one.

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
any limit rejects the entire event, changing no task/turn/item/
relationship/obligation/receipt. Diagnostic emission stops at the cap
without stopping suppression-state tracking or semantic validation. Limits
are trusted configuration, never directive attributes. Fuzzing covers
panics, valid offsets, deterministic output, source gating, and
structural/allocation bounds via scaling benchmarks or instrumented
counters, not wall-clock assertions inside fuzz tests.

Owner: `internal/directive` (per-span/per-parse-unit limits),
`internal/domain` (limits config type), `internal/ingest` (whole-event
totals, graph-scan bounds).

**Deferred ruling: `tx.Grants()` per obligation retirement/lifecycle
command (SPEC-1.3, part of F1's bounded-lookup findings).** `graph
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
documented, versioned lexical identity rule that includes
repository/resource namespace and a relevant base directory, with no
filesystem or network access. Matching authorized targets link
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

Phase 2's `IngestResult`/receipt types are internal until the public
service signature is settled: R3 keeps them in `internal/ingest` (or
`internal/domain`), *not* aliased from the root package — the root package
aliases only `Event`/`Span` input types (SDD §8's `Ingest(...)
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
  widen later without an explicit ADR change.
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
- **Parser version pinning (new, refines §3/§12/§13).** The parser version
  this ADR's records reference throughout (recorded with every item,
  diagnostic, and receipt) is `directive/v1`, defined as exactly the
  behavior this ADR and `internal/directive/doc.go` document; a future
  grammar change is a new version string, never a silent reinterpretation
  of `directive/v1`.

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
  `0008_unresolved_references.sql` persists it as `rec_reference`, indexed
  on `(session_id, locator_key, rule_version, seq, id)`, so an unresolved
  References item survives restart for later linking (§16's "unresolved
  references persist" requirement).
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
- **Indexed lookups, not session-wide scans (refines §7/§13/§15/§16,
  D10/D17/D19/M5 — landed, SPEC-1.4).** `p2-store` added indexed lookups
  so blob reference (`ItemsByBlob`, migration 0009, §15/R5),
  duplicate-candidate (`DuplicateCandidates`, migration 0010, §7/D10), and
  reference matching (`UnresolvedReferences`, migration 0008, §16/M5/R2)
  are bounded, not a scan of every item in a session, matching D17/NFR's
  bounded-work discipline; a fourth, `ItemsBySourceKey` (migration 0011,
  R19), was added for the same reason. ADR 3 records the migrations and
  the `assertIndexed` `EXPLAIN QUERY PLAN` tests locking each to a real
  index. **These three lookups are properly indexed; the *other* graph and
  current-version reads `internal/ingest` calls once per ingested item or
  section — repeated `Relationships`/`Items`/`Grants` calls — reintroduce
  an unbounded scan one level up, which is a separate, still-open finding
  (SPEC-1.3/F1, §26).**
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
  decision, refines §7/§1).** `DiagnosticCode` is a closed seven-value set
  (`internal/domain/diagnostic.go`'s `Severity` switch); R13/R14's new
  `DiagnosticReason`s must reuse an existing code, and no code is paired
  with either in the merged code as of this ADR revision. This ADR pins
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
  `Reason` token, never by a different `Code`.

### 24. Round 6 ruling (ingest-suite findings): R20

Findings from `p2-ingest`'s own test suite, appended to
`phase2-amendments.md` as R20.

- **Reserved internal ID prefixes on caller `EventID` (refines §10/§11,
  D14/D15/R16 — landed, SPEC-1.4).** `Event.Validate`
  (`internal/domain/ingest.go:111`) now also calls `ReservedIDPrefix`: a
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
  is actually applied. `internal/ingest`'s unit loop now filters this: `if
  d.Code == domain.DirectiveIDDerived && !r.written[d.Range] { continue
  }` — a derived-ID notice is reported only for a range ingestion actually
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
  status quo it would replace.
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

### 26. PR #5 review round 1: F2's accepted residual risk (SEC-1.5)

The first external review of this PR (`round1-p5-fixes.md`) produced
commander rulings F1-F6. F1 and F3-F6 require code changes in
`internal/store`/`internal/graph`/`internal/ingest` that are other
workers' assignments and had not landed as of this revision; this ADR
will reconcile their exact landed shape in a final pass once every fix is
in, rather than describing an intended design ahead of the code (the
mistake SPEC-1.4 found with this ADR's earlier "not yet landed"
paragraphs). F2 is recorded in full now because the commander ruled it a
documentation-only mitigation with no code change beyond a verification
test:

- **F2 (SEC-1.5 — caller `EventID` namespace, refines §10/§11, D14/D15).**
  `CallerOccurrenceID(session, eventID)` ignores the principal, so a
  session-wide `EventID` collision across tasks/agents/workflows lets one
  principal's guessable ID block another's identical retry and lets an
  attacker probe which IDs another principal has used (reproduced:
  `TestSEC_EventIDCrossPrincipal`, a `wf2/T2` principal using `EventID:
  "turn-2"` blocks a later, unrelated `T`-scoped event with the same ID).
  Ruling: mitigation, not redesign — FR-ING-006's session-scoped identity
  is unchanged; a different principal reusing an `EventID` still fails
  `domain.ErrEventIDConflict`
  (`errors.New("event ID conflict")`, `internal/domain/errors.go:24`),
  returned bare with no ID, principal, or session detail
  (`internal/ingest/ingest.go:170,177`) — `p2-contract` verifies this with
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

F1 (bounded lookups, `p2-store`/`p2-graph`), F3 (retry-before-limits,
`p2-graph`), F4 (References-by-item-ID, `p2-graph`), and F6 (diagnostic/
lifecycle-command record access, `p2-graph`) are tracked, not yet
described here in full, pending this ADR's final reconciliation pass
against their landed code. **F5 has landed and is recorded in ADR 3's
Phase 2 migrations section** (SPEC-1.9): migration 0011's Go step is now
a frozen, private copy of the locator rule with its identity folded into
the migration's checksum, so it is no longer outside checksum protection
and no longer depends on live domain code.

### 27. PR #5 review round 1 rulings: SPEC-1.10, SPEC-1.11

- **SPEC-1.11 ruling: CommonMark-heading-shaped lines close but never open
  a section (refines §4, D5/D6/R17 — decided, fix not yet landed).** Parser
  v1 previously treated a bare ATX line (`#` through `######` alone) or a
  tab-separated one (`#\tOther`) as ordinary content: it neither opened nor
  closed a section, and inside a list body counted as stray prose (marking
  the section malformed). SPEC-1.11 flagged this as the one deliberate
  deviation that leaves *more* text inside a section than CommonMark would,
  and asked for a ruling rather than leaving it an open deviation forever.
  **Ruling:** a line matching CommonMark's ATX heading *shape* (0-3 leading
  spaces, 1-6 `#`, then SP, HTAB, or end of line — the same shape D5
  already requires for a real directive heading, just without a valid
  keyword) closes an open section of the same or higher level exactly as a
  valid directive heading would (D6), but never opens a new section, since
  it has no keyword to open one with. A line that is not heading-shaped at
  all — seven or more `#`, 4+ space or tab indentation before the `#`,
  fenced/quoted/commented, or a bare `#` immediately followed by non-space
  text like `#5` — is unaffected and stays body or ordinary content. The
  parser has no list-container model, so a heading-shaped line indented
  inside a list item also closes its section; closing early only drops
  directive text, never fabricates any. This is locked by
  `TestHeadingShapedLinesClose` (`internal/directive/scan_test.go`,
  replacing the pre-ruling `TestBareAndTabATXLines`), covering both the
  closing cases (bare `#`, `#\tOther`, `##`, a heading-shaped line with
  indentation) and the still-not-heading-shaped cases (7 `#`, `#5 bolt`,
  tab-indented, fenced/quoted/commented). The test is committed as a red
  regression (`p2fix/parser`); the parser fix implementing the ruling is
  not yet landed as of this revision, and `internal/directive/doc.go`'s
  deviation list still describes the pre-ruling behavior and needs
  updating once it does.
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
- **M8/R3:** exporting the rich Phase 2 `IngestResult` as a root-package
  alias now (the review's original phrasing, "if the public API will
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
- `graph.IsCurrent`'s existing incoming-edge-only semantics are
  insufficient for D10's "never becomes current" guarantee (§7); every
  current-version consumer, not just the write path, must be updated to
  also exclude a `DUPLICATE_OF` item and a stale current-map pointer.
- `graph.Supersede`'s `(session, old target, action, event)` audit-ID
  scheme collides on two retirements of the same old target within one
  event; D11's planned edge set (§7) must resolve this itself rather than
  leaving it for `p2-graph` to discover independently.
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

Required, not yet written except where a package/file is named; `p2-tests`
reconciles exact names in a later round.

- **§1 (lifecycle commands):** `internal/ingest` — an unauthorized source
  actor's Resolve/Unpin aborts the whole event (no partial mutation); an
  unknown/inaccessible/ambiguous target produces the expected diagnostic
  without aborting; a resolved, authorized command is recorded
  `PARSED_NOT_EXECUTED` and changes no goal/pin/obligation state. Trace
  T06's parse/authorization half, both stores.
- **§2 (classification):** `internal/policy` — table-driven unit tests per
  (event/span type, section) → defaults; `internal/ingest` — a surface
  token (`MUST`, `PASS`, a JSON role label) in ordinary text creates no
  privileged record; an unsupported event variant is rejected.
- **§3 (bytes/hashes):** `internal/store/storetest` — a common byte
  round-trip case across memory and SQLite for invalid UTF-8, a lone CR,
  and a leading BOM, asserting `ContentHash` identity survives a restart;
  a dedicated SQLite regression for the fixed lossy encoding (R8, its own
  commit).
- **§4 (grammar):** `internal/directive` fuzz tests (seed corpus
  committed) for panics, O(n) time, offsets-in-bounds, round-trip
  invariants, and capability gating; `testdata/directives/` canonical
  examples including the SDD's own worked example, golden-JSON expected
  items/diagnostics; targeted unit tests for each D5/D6/D7 boundary case
  (short fence closer, comment-wrapped keyword, deeper non-keyword
  heading, trailing-whitespace-significant item, invalid `[id]` dropped
  not salvaged).
- **§5 (transcript/derived items, isolation):** `internal/ingest` —
  exactly one transcript item per span plus expected directive items,
  `DERIVED_FROM` coverage naming the transcript item; a trusted heading
  fragment split across two spans creates no directive; a fence opened in
  one span and closed in another creates no suppression; an
  image/document part interrupts text parsing. Trace T18 end-to-end on
  both stores.
- **§6 (derived vs. explicit IDs):** `internal/directive` — an explicit
  `[id]` with derived-ID shape is dropped with `ErrMalformedDirective`,
  never silently reassigned a derived ID.
- **§7 (dedup/replacement/Working):** `internal/graph` — a duplicate
  directive never resolves as current by literal ID (closing the review's
  reproduced hole); `internal/store/storetest` — cross-turn/cross-TTL-origin
  items are never deduplicated as stand-ins; `internal/graph` —
  `W1={a,b}→W2={a}` correctly retires `b`; identical-snapshot,
  partial-overlap, repeated-explicit-ID, mixed-boundary, and
  multiple-Working-sections-in-one-event cases; two retirements of the
  same old target in one event do not collide on audit ID.
- **§8 (attributes/TTL/scope):** `internal/policy`/`internal/directive` —
  exact-case attribute/scope rejection; `ttl=0001` accepted (leading
  zeros); a `ttl` value above 2147483647 fails event validation with the
  representation-limit error, not a silent ignore; USER-span
  WORKFLOW/SESSION widening ignored with a diagnostic; AGENT scope never
  removes an existing task/workflow constraint.
- **§9 (obligations):** `internal/graph`/`internal/domain` — a Pinned
  `obligation=` declaration creates an UNRESOLVED version bound to its
  source; replacing the pin retires the old obligation version in the
  same transaction and starts a fresh UNRESOLVED version only if the new
  source redeclares one; dropping `obligation=` on replacement retires
  without creating a replacement. Trace T02's obligation half.
- **§10 (idempotency/receipts):** `internal/store/storetest` — a repeated
  identical `EventID`/payload returns the *original* receipt even after an
  intervening lifecycle mutation or policy-version upgrade; a conflicting
  payload/principal fails `ErrEventIDConflict`; anonymous (no-`EventID`)
  events remain isolated from each other under an empty key;
  `internal/ingest` — the transaction-scoped core never re-enters
  `Store.Update`/`View` (a deadlock regression test against SQLite
  specifically). Trace T10.
- **§11 (authority/actor):** `internal/ingest` — a SYSTEM-carried USER
  span's Resolve executes (once Phase 3 lands) at USER authority, never
  SYSTEM; directive-capable marking on an AGENT/TOOL/RETRIEVED_CONTENT
  span rejects the event; an inaccessible target lookup returns bare
  `ErrNotFound`.
- **§12 (diagnostics):** `internal/store/storetest` — a diagnostic is
  unreadable by a principal outside its source's access boundary;
  migration/rollback/restart/replay coverage; the 256-plus-one truncation
  marker is stable and stored.
- **§13 (limits):** `internal/directive` fuzz/benchmark — a pathological
  many-small-spans input is bounded in total work, not just per-span; an
  oversized event is rejected wholesale with no partial state change.
- **§14 (turns):** `internal/domain`/`internal/ingest` — an
  AGENT/TOOL/RETRIEVED_CONTENT payload cannot advance a turn; a first
  task-bound event creates Turn=0→1 correctly; a TURN-scoped item expires
  on the next turn even with a larger `ttl`; an exact retry never
  re-advances the turn. Trace T03/T10.
- **§15 (blobs):** `internal/ingest` — a blob hash reference not already
  referenced by an accessible item in-session is rejected, requiring bytes
  instead; missing and inaccessible references are indistinguishable.
- **§16 (references):** `internal/ingest` — a References item lexically
  matches an accessible same-session target and links deterministically;
  an inaccessible target is indistinguishable from absent; a Phase 2 TOOL
  span's `ToolCallID` lands only in `SourceRef`, with no edge created.
- **§17 (namespace):** `internal/store/storetest` — `agent.status` is
  accepted as a legal directive ID string and does not collide with a
  keyed agent-write namespace entry, both stores; `internal/graph` —
  `ResolveLifecycleTarget` never resolves an `AGENT_KEY`-namespace entry as
  a directive target.
- **§18 (relationship input):** `internal/ingest` — an event carrying an
  arbitrary/unsupported explicit relationship field is rejected wholesale;
  relationship-shaped JSON inside tool text produces no edge.
- **§19 (API shape):** static/import-boundary check — the root package
  aliases only `Event`/`Span`, not `IngestResult`; `internal/store/storetest`
  — an upgrade/restart parity fixture for a pre-Phase-2 record read after
  the new migrations land.
- **§20 (round 2 rulings, R9-R15):** `internal/graph`/`internal/store` —
  retiring obligations bound to a replaced source uses the obligations-
  by-source lookup and stays bounded under a large-fan-out source (R9);
  `internal/graph` returns `ErrNamespaceConflict` for an ambiguous
  pre-migration lookup rather than guessing a namespace (R10);
  `SameDirectiveSemantics` includes creation turn, TTL origin, and
  obligation declaration in its comparison, both in `internal/graph` unit
  tests and as the single comparison `internal/ingest` calls (R11); the
  Working-snapshot duplicate-vs-changed decision is scoped per `(task,
  authority, boundary)`, confirmed with two different-boundary snapshots
  under the same task never being compared to each other (R12); a same-ID
  boundary conflict on one item among several in one event rejects only
  that item with `boundary_conflict` and commits the rest, while the same
  conflict inside a Working section blocks that section's snapshot
  replacement without aborting the event, distinct from an
  authorization/integrity/idempotency/resource failure that still aborts
  the whole event (R13); Resolve on a non-OPEN goal and Unpin on a
  non-pinned target each produce a diagnostic and commit the rest of the
  event, never an abort (R14).
- **§21 (round 3 ruling, R16):** `internal/ingest` — a directive
  `internal/directive` accepts but `policy.ForDirective` would reject
  (e.g. a disallowed attribute combination the parser doesn't itself
  enforce) is not classified as if accepted; `p2-tests` — a fuzz/property
  cross-check that every `internal/directive`-accepted directive in
  `testdata/directives/` is also `policy.ForDirective`-accepted; an event
  whose kind implies lower authority than one of its spans is rejected
  wholesale, never silently downgraded or upgraded; an `EventID` containing
  non-printable-ASCII bytes or exceeding 256 bytes is rejected before any
  idempotency lookup; a HARNESS residual instruction item is never selected
  as mandatory by FR-DOM-007's policy-mandatory path; `internal/directive`'s
  `Item.TextRanges` and `internal/domain`'s `SourceRef.Slices` round-trip
  the same byte ranges for a canonical fixture.
- **§22 (round 4 rulings, R17-R18):** `internal/ingest` — a directive
  `internal/directive` accepts is also `policy.ForDirective`-accepted
  (R17, reconfirming R16); a span whose diagnostics split across multiple
  parts are capped per-span after merging in part order, then again at
  the whole-event total, never per-part independently (R17); two list
  items sharing one explicit ID are both absent from the result, not
  one-survives-one-diagnosed (R17); a column-0 fence/quote marker inside a
  list body malforms the whole section (R17); an out-of-range `ttl`
  aborts the event rather than leaving the item live with no TTL (R17,
  confirming R1); every persisted item/diagnostic/receipt records parser
  version `directive/v1` (R17). `internal/store/storetest` —
  `ObligationsBySource` with a `limit` lower than the bound source's
  obligation count returns exactly `limit` versions, deterministically
  ordered (R18). Deferred until landed: once `internal/graph`/
  `internal/ingest` switch to `CurrentVersion`/`CurrentVersions`, a static
  check asserts no remaining call site uses the deprecated
  `CurrentDirective`/`CurrentDirectives`/`SetCurrentDirective` (R18); once
  landed, `domain.TTLLive(0, current, n)` is `false` for every
  `current`/`n` (R18); once landed, `domain.UnresolvedReference`
  round-trips through migration 0008 and survives restart (R18).
- **§23 (round 5 ruling, R19):** `internal/domain` — a derived item's
  `SourceRanges` alone reconstructs its transcript coverage, with no
  companion section record to keep consistent (R19); an `IngestReceipt`
  with `OpenedTurn`/`TurnID` set has its turn-opening pending-input items
  exactly in `Items` (R19); a diagnostic paired with `ReasonBoundaryConflict`
  always carries `Code: ErrMalformedDirective`, and one paired with
  `ReasonTargetMismatch` always carries `Code: DiagnosticNotFound`, never
  a different code for either (R19, the pinning decision above).
  `internal/ingest` (once it lands) — a TOOL or RETRIEVED_CONTENT event
  for a task with `Turn=0` is rejected before any item is created (R19); a
  References locator match never considers a repository/namespace
  component, so two same-named locators in different conceptual
  repositories within one session are treated as the same target (R19,
  until multi-repo support exists). Deferred until `p2-store` lands the
  indexes: blob-reference lookup, duplicate-candidate lookup, and
  reference matching each run in bounded time independent of session
  size, not as a linear scan (R19).
- **§24 (round 6 ruling, R20):** `internal/domain` — once landed, an
  `EventID` equal to or prefixed like `evc_`, `eva_`, or any `IDDomain`
  prefix fails `Event.Validate` (R20). `internal/ingest` — once landed, a
  `DirectiveIDDerived` diagnostic for an item whose section ingest refused
  is absent from the receipt, not merely present-but-orphaned (R20); a
  residual instruction is never created for a leading-BOM-only residue,
  matching the existing whitespace-only case (R20); a residual
  instruction's content includes leading and trailing whitespace exactly
  as it appeared in the transcript, once the trim-for-emptiness and
  trim-for-content paths are split (R20); a malformed section inside a
  SYSTEM or HARNESS span still produces a residual instruction item from
  its bytes, so the malformed-heading-drops-a-trusted-requirement case has
  a regression test (R20). Already passing: `workingSection` creates zero
  items for a `Malformed` section or one with no items, confirmed as the
  correct (stricter) reading of D11 (R20); a boundary conflict on any
  Working-section member aborts the whole section's write with
  `ErrMalformedDirective`/`ReasonBoundaryConflict`, never a partial commit
  (R20, confirming §23's pinning).
- **§25 (round 7 ruling, R21):** `internal/ingest` (once R20 lands) — a
  unit whose only residue is a bare malformed heading with no body creates
  no residual instruction item (R21); a malformed Resolve/Unpin/
  unsupported-lifecycle-word section in a SYSTEM or HARNESS span never
  produces a residual instruction, staying transcript-only with its
  diagnostic, even though a malformed content section in the same span
  now does (R21) — the negative case a naive "any malformed trusted
  section becomes residual" implementation would get wrong; a unit with
  both a refused section and an earlier-positioned trusted directive item
  creates its residual instruction item, if any, after every directive
  item and lifecycle command the unit produced, never ordered by the
  residual's own byte position (R21, amending M3's creation order).
- **§26 (F2, SEC-1.5):** `internal/domain`/`internal/ingest` — a repeated
  `EventID` from a different principal fails `domain.ErrEventIDConflict`
  and the returned error is the bare sentinel: no ID, principal, or
  session detail is present in its text or wrapped chain
  (`p2-contract`'s verification test for this finding).
- **Cross-cutting (decision-review gate additions):** every path above run
  under `-race` where concurrent ingestion applies; injection-resistance
  tests for each §9-of-the-SDD item reachable in Phase 2 (retrieved/tool
  injection, pasted-document directives, tool-based escalation, authority
  downgrade, cross-authority supersession/dedup, cross-session/cross-task
  lookup, fenced/quoted/comment smuggling, Unicode look-alike keywords,
  CRLF/BOM tricks, over-long IDs, attribute injection).

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
  by R3) — resolved, drafted in a later round.** The amendment will be
  added to this ADR as an explicit **unapplied** proposal — a "Required SDD
  amendment" subsection, not "applied" — once `p2-ingest`'s `IngestResult`
  shape has actually stabilized against the landed contract/parser/store/
  graph work; drafting it now, before that shape exists, would risk
  proposing a signature Phase 2's own implementation then contradicts. Not
  yet drafted as of this ADR revision.
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
  a derived ID, so explicit and derived IDs can never collide."
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

These are the exact insertions applied to SDD.md; see that file's v0.9 diff
for placement.

## Review

This ADR records the commander's disposition of an adversarial decision
review (Codex gpt-6-astra xhigh) of the Phase 2 brief against the merged
Phase 1 codebase and SDD v0.8: D2/D4/D9 agreed as written; D1, D3, D5-D8,
D10-D19 amended; M1-M8 added as missing decisions; all accepted, with
commander rulings R1-R8 (`phase2-amendments.md`) overriding eight of them
as recorded above. No Phase 2 code exists yet against which to re-verify
these decisions; that verification is this ADR's own gate (the tests in
the section above) once `p2-contract`, `p2-parser`, `p2-store`, `p2-graph`,
and `p2-ingest` land their work, and it is expected to move this ADR from
Proposed to Accepted at Phase 2 exit, not before.
