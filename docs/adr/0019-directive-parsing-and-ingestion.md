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
