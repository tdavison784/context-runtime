# Canonical directive examples

This directory is the Phase 2 canonical example set required by FR-DIR-006.
It is part of the test contract: a behavior change that alters any
`expected.json` is a parser-version change and must be reviewed as one.

Each example is a directory:

| File | Contents |
|---|---|
| `input.md` | Raw bytes of the first parse unit. Nothing is normalized: CR, CRLF, BOMs and invalid UTF-8 are deliberate (`.gitattributes` marks them `-text`). |
| `unit.json` | Trusted unit metadata: `description`, `requirements`, `authority`, `directive_capable`, optional `span_index`/`part_index`, `no_directives` (asserted independently of the golden), and `more_units` for multi-unit (M1) examples. |
| `expected.json` | Golden parse of each unit: `sections`, `items` (IDs, derived IDs, typed attributes, `text` or `text_hex`, `range`, `text_ranges`), `lifecycle` commands, and `diagnostics` (code, reason, section, range; never content). `error` is `invalid_record` or `representation_limit` when the unit is rejected. All ranges are half-open byte offsets into the unit's own file. |

The golden test is `TestCanonicalDirectiveExamples` in `internal/directive`.
It parses every unit through `directive.ParseUnit` and checks these on every
example, whatever the golden says:

- source gating;
- each item's text rebuilds exactly from its `text_ranges`;
- diagnostics are valid;
- lifecycle commands are valid;
- `policy.ForDirective` accepts every item.

Regenerate goldens with:

    go test ./internal/directive -run TestCanonicalDirectiveExamples -update

and review every changed golden by hand. `-update` records behavior; it does
not validate it.

Examples are grouped by name prefix:

| Prefix | Covers |
|---|---|
| `sdd-example` | The FR-DIR-006 example, verbatim |
| `form-` | Section bodies, lists, continuations, extent (D6, D7, M4) |
| `lifecycle-` | Resolve/Unpin forms and the unsupported vocabulary (FR-DIR-005, M4) |
| `attr-` | Allow-list, exact case, scope widening, ttl, precedence (D12, R1, M4) |
| `suppress-` | Fences, quotes, comments, indentation, heading shape, source gate (D5, FR-ING-004) |
| `d20-` | Derived-ID-shaped explicit IDs (D20) |
| `t18-` | Trace T18 parse halves |
| `inject-` | SDD section 9 injection cases (M1, D3, D4, D12) |
