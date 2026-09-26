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
future rendering (Phase 4/5) must track expiry against both. Transcript
defaults: USER/AGENT messages get WORKING generation, TASK scope, NORMAL
retention; TOOL/RETRIEVED_CONTENT get EPHEMERAL generation, TURN scope, LOW
retention; SYSTEM/HARNESS instructions get DURABLE generation, HIGH
retention, SESSION scope (SYSTEM) or TASK scope (HARNESS) — all RESIDENT on
creation. Final item access is the requested scope boundary intersected
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
