// Package directive is the pure, source-gated directive parser (FR-DIR-006,
// FR-ING-004). It never reads stores, resolves targets, applies defaults, or
// mutates semantic state. It imports only internal/domain.
//
// # Parse units (M1)
//
// Ingestion calls ParseUnit once per domain.ParseUnit: one text part of one
// span. Section, list, fence, quote and comment state never crosses a unit,
// so a keyword, fence or heading split across spans or parts has no effect.
// Only SYSTEM, HARNESS and directive-capable USER units are interpreted; other
// units yield at most DirectiveNotParsed diagnostics.
//
// # Bytes and offsets (D3)
//
// Input is raw bytes: nothing is normalized, folded, repaired or trimmed, and
// invalid UTF-8 is ordinary non-keyword payload. LF, CRLF (one break) and
// lone CR end lines. One UTF-8 BOM is skipped only at byte 0 of the unit.
// Every range is a half-open offset into the unit's original bytes.
//
// # Headings (D4, D5)
//
// A directive heading starts at byte-column 0 with 1-6 '#', one SP, and a
// complete keyword token matched with ASCII-only case folding (the Kelvin
// sign, long s, dotless i and similar never match). Metadata follows as
// `[SP "[" id "]"] *(SP attr)`; trailing SP/HTAB is tolerated. Anything else
// after the keyword (tabs or doubled spaces as separators, closing ATX
// hashes, non-attr tokens, attributes on Resolve/Unpin) makes the heading
// malformed. Indented, tab-separated, setext and seven-'#' headings are
// never directives. Suppression is a per-unit state machine:
//   - A fence opens on 3+ identical '`' or '~' after 0-3 spaces, with any
//     info string. It closes only on the same character, at least the opening
//     length, 0-3 spaces of indentation, and an SP/HTAB-only tail. Unclosed
//     fences run to the unit end. Quote and comment markers are inert inside.
//   - Explicit quote lines ('>' after 0-3 spaces) are suppressed; lazy
//     continuation lines are ordinary lines.
//   - A line that opens, closes, or lies inside an HTML comment is
//     suppressed; fence markers are inert inside a comment.
//
// Wrappers are never stripped to reparse the rest of a line. Suppressed or
// non-capable keyword headings produce DirectiveNotParsed with an enumerated
// reason and the heading's byte range, never its text.
//
// # Section extent (D6)
//
// A section ends at the next unsuppressed heading of the same or higher
// level, or at the unit end. Deeper headings, keyword or not, are body text
// (with a nested_heading diagnostic for keywords). A malformed keyword
// heading or an unsupported lifecycle word still opens a region that grants
// no directive semantics until the next same-or-higher heading.
//
// # Items (D7, M4)
//
// A body whose first non-blank line is a top-level bullet ("- ", "* ",
// "N. ") is a list; otherwise it is one item. Item text is exactly the bytes
// named by Item.TextRanges: the recognized bullet, [id], {attrs} and one SP
// each, and the continuation indentation (the fewest leading SP/HTAB bytes
// over non-blank continuation lines), are the only bytes removed. Line
// endings and trailing whitespace are kept. A single body keeps everything
// between its first and last non-blank lines. In a list, blank and indented
// lines continue an item, trailing blank lines are structure, and unindented
// prose is malformed content through the next bullet. Blankness is ASCII
// SP/HTAB only. These drop the item (ErrMalformedDirective) and mark the
// section Malformed: an invalid or over-long [id], an explicit ID in the
// derived-ID shape (D20), a missing separator, attribute syntax errors or an
// unterminated '{', empty text, and an ID that repeats within one list
// (every member under it is dropped). A heading ID on a list is diagnosed and
// ignored. Repetition across sections is left to source order.
//
// # Attributes (D12, R1, M4)
//
// Names and values are exact case. The allow-list is kind on
// Pinned/Working/Remember/Ephemeral (values per FR-DIR-003), scope on every
// content section (TURN, TASK, AGENT; WORKFLOW and SESSION only in SYSTEM or
// HARNESS units), ttl on Working/Remember/References/Ephemeral, and
// obligation on Pinned. ttl is ASCII digits, leading zeros allowed, value
// 1..domain.MaxTTLTurns. Unknown, disallowed, invalid, widening and repeated
// attributes are diagnosed and ignored. The first valid occurrence per level
// wins, a valid item value overrides the section value, and an invalid
// override keeps the inherited value. An allowed ttl above the bound is not
// ignored: the unit fails with ErrRepresentationLimit.
//
// # Lifecycle (D1, D7, M4)
//
// Resolve and Unpin accept either a heading target with an otherwise blank
// body, or a list of target-only items. Mixed forms, missing targets,
// attributes, or text create no command for the malformed unit. Targets may
// have the derived-ID shape. Archive, Unarchive, Promote, Demote, Block,
// Unblock, Waive, CompleteTask and Reopen produce ErrUnsupportedDirective.
// Commands are parsed, never executed.
//
// # Limits (D17)
//
// Per-unit limits (bytes, items, heading bytes, attribute count and token
// bytes, ID bytes, heading level) reject the unit with no partial result.
// Whole-event limits are ingestion's job. At most MaxDiagnosticsPerSpan
// diagnostics, the earliest in source order, are kept, followed by one
// DiagnosticsTruncated marker. The cap never changes a parse decision.
package directive
