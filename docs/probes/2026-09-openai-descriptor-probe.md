# 2026-09 OpenAI descriptor probe

Phase 1 exit gate (SDD.md section 11, phase 1): "A throwaway live probe of the ... OpenAI
descriptor assumptions (reasoning binding, cache reads, compaction protocol) runs during this
phase so ADR 12 rests on observed behavior before the vertical slice." This document answers the
probe brief's R1-R5 (reasoning binding), C1-C4 (cache reads), K1-K3 (compaction protocol), and
counter/model-list questions from the sanitized fixtures committed under
`probes/descriptor/testdata/openai/`, and closes with a draft `Capabilities` descriptor per model
for ADR 12.

Evidence standard: every claim below cites a fixture file and quotes the observed field or error
verbatim. Where the committed fixtures do not settle a question, it is marked **NOT DETERMINED**
rather than inferred.

## Method

- Code: `probes/descriptor/openai` (nested Go module, `github.com/openai/openai-go/v3 v3.66.0`),
  driven by the OpenAI Responses API (`/responses`, `/responses/input_tokens`,
  `/responses/compact`, `/models`).
- Models probed: `gpt-6-astra` (current flagship reasoning model) and `gpt-6-luna` (cheaper
  tier), selected from the 132 IDs returned by `/models` (`models.json`) — the `gpt-6-*` family is
  the newest available, alongside an unprobed `gpt-6-sol`.
- Run: 2026-09-26 (fixture `created_at`/`completed_at` epoch times resolve to that date, e.g.
  `probes/descriptor/testdata/openai/gpt_6_astra_k1_compact.json` `created_at: 1790427089` =
  2026-09-26 12:51 UTC).
- Fixtures are sanitized per the probe brief: `encrypted_content`, `signature`, `id`, `call_id`,
  and `previous_response_id` values are replaced with `{length, sha256}`; `organization`,
  `project`, `user`, and `safety_identifier` are redacted. No headers, keys, or account
  identifiers are present (verified with
  `git diff | grep -iE "sk-|api[_-]?key|bearer|authorization|org-|proj_"`, no matches). Error
  bodies are plain text, not JSON, so they bypass that key-based hashing; `scrubError` separately
  regex-matches and hash-redacts embedded `resp_`/`rs_`/`msg_`/`call_`/`fc_`/`cmp_` object IDs
  (SEC-1.5 fix; see `TestScrubErrorRedactsObjectIDs` in `probes/descriptor/openai/main_test.go`).
- No additional API calls were made in this write-up session; all answers below come from the
  fixtures already committed. One question (C4 pricing multipliers) is answered from public OpenAI
  documentation instead, since the fixtures only show the accepted/rejected TTL *values*, not
  pricing.

## Reasoning binding (R1-R5)

Setup (`probes/descriptor/openai/main.go` `reasoning()`): a `high`-effort tool-call turn
(`*_r1_tool`) produces a `reasoning` item plus a `function_call`; the tool result is appended, then
each variant below replays that history with a `medium`-effort continuation. Both models gave
identical qualitative results; token counts differ slightly by model.

**R1 — replay reasoning unchanged: ACCEPTED.**
`gpt_6_astra_r1_replay.json` replays the tool-turn `reasoning` item and result verbatim and
succeeds (`status` implied completed, no `error`), with
`usage: {input_tokens: 112, output_tokens: 21, output_tokens_details.reasoning_tokens: 13}` and the
correct final answer (`"cobalt"`, matching the fixed-up tool result `"A = cobalt"`). Verdict:
**SAFE** (FR-CAP-002 `APPEND`-class edit).

**R2 — drop the reasoning, keep the tool call/result: ACCEPTED, but not verbatim replay.**
`gpt_6_luna_r2_drop.json` / `gpt_6_astra_r2_drop.json` strip every `type: "reasoning"` item from
history while keeping the `function_call`/`function_call_output` pair, and the request still
succeeds: `usage: {input_tokens: 71, output_tokens: 47, reasoning_tokens: 39}`, output types
`["reasoning", "message"]`, correct answer `"cobalt"`. The provider does not require the original
reasoning block to accompany its own tool call — it silently regenerates fresh reasoning
(`reasoning_tokens: 39`, close to the original turn's 39) rather than rejecting the request.
Verdict: **LOSSY**, not REJECTED — this is `DROP_ALL_REASONING`/`DROP_LEADING_REASONING` in
FR-CAP-002 terms, and it is accepted with silent regeneration rather than an error.

**R3 — modify reasoning content/signature: REJECTED.**
`gpt_6_astra_r3_corrupt.json` flips one byte of the tool-turn reasoning item's
`encrypted_content` and gets a 400:
```
"message": "The encrypted content for item <redacted id length=53 sha256=0cf21c80...> could not be verified. Reason: Encrypted content could not be decrypted or parsed.",
"type": "invalid_request_error",
"code": "invalid_encrypted_content"
```
Identical for `gpt_6_luna_r3_corrupt.json`. Verdict: **REJECTED**, confirming the encrypted
reasoning blocks are integrity-checked server-side (a `REWRITE` of the reasoning block itself is
always rejected, never silently dropped).

**R4 — edit an earlier message while replaying later reasoning unchanged: ACCEPTED.**
`gpt_6_astra_r4_edit_earlier.json` rewrites the first user message's `content` (asking for lookup
code B instead of A) while leaving the tool-turn `reasoning`/`function_call`/`function_call_output`
items byte-for-byte unchanged, and still succeeds:
`usage: {input_tokens: 90, output_tokens: 23, reasoning_tokens: 15}`, answer `"cobalt"` (the value
fixed by the unedited tool result, not a value implied by the edited instruction — the model is not
observed to notice the edit; it just reads the tool result back). Verdict: **SAFE** — the API's
reasoning-item integrity check is not bound to the preceding conversation text, only to the
reasoning item's own encrypted content (contrast with R3, where corrupting that same field is
rejected). This means a `REWRITE` of content *before* an open reasoning/tool round is accepted by
this provider as long as the reasoning items themselves are untouched — narrower than FR-CAP-002's
default assumption that any REWRITE drops trailing reasoning; the descriptor should record this as
model-specific rather than assume it generalizes.

**R5 — reasoning from turns before the last user message: replay is optional, not required.**
Both variants append a new final user turn ("What was the lookup word? One word.") after the full
r1 history and succeed:
- `gpt_6_astra_r5_keep_older.json` (reasoning items kept): `usage: {input_tokens: 125,
  output_tokens: 6, reasoning_tokens: 0}`, output types `["message"]` only — no new reasoning was
  generated; the model answered directly from the retained tool result.
- `gpt_6_astra_r5_drop_older.json` (reasoning items stripped): `usage: {input_tokens: 84,
  output_tokens: 22, reasoning_tokens: 14}`, output types `["reasoning", "message"]` — the model
  generated fresh reasoning instead.

Both give the correct answer (`"cobalt"`). Verdict: reasoning predating the last user message is
**not required** to be replayed (SAFE to drop); keeping it does not force the model to reproduce or
re-emit it either. This is consistent with R2's LOSSY-not-REJECTED finding.

**Stateless `previous_response_id` chaining: REJECTED (not a reasoning-binding question per se, but
observed incidentally).** `gpt_6_astra_stateless_previous.json` sends `store: false` on the first
call, then tries `previous_response_id` on the second, and gets:
```
"message": "Previous response with id '<redacted id length=55 sha256=d7df68a9...>' not found.",
"code": "previous_response_not_found"
```
By contrast, `gpt_6_astra_state_first.json`/`gpt_6_astra_state_previous.json` (`store: true`)
chain successfully: the second call's `usage.input_tokens` (25) is far smaller than the full
resend would cost, implying server-side state reuse, and returns the correct answer (`"cobalt"`).
Implication for the descriptor: `previous_response_id` continuation requires `store: true`;
`store: false` responses are not retrievable by ID even moments later.

## Cache reads (C1-C4)

**C1 — minimum cacheable prefix: bracketed between 1023 and 1034 tokens for both models.**
`probes/descriptor/openai/main.go` `cache()` builds a deterministic filler prefix repeated `n`
times and calls `/responses/input_tokens` (the counter) then `/responses` twice. Observed
`input_tokens` (== counter output, see "Counter endpoint" below) and
`usage.input_tokens_details.cached_tokens` for the *first* call at each length:

| n (repeats) | input_tokens | astra cached_tokens (first) | luna cached_tokens (first) |
|---|---|---|---|
| 75 | 914 | 0 | 0 |
| 84 | 1022 | 0 | 0 |
| 85 | 1034 | 1031 | 1031 |
| 100 | 1214 | 1211 | 1211 |

(`gpt_6_{astra,luna}_c1_{75,84,85,100}_{count,first,repeat}.json`.) The jump from 0 to a
near-total cache hit happens between 1022 and 1034 input tokens for both models, so the minimum
cacheable prefix is in that range (consistent with a 1024-token floor, though the fixtures only
bracket it, they do not pin the exact boundary — **NOT DETERMINED** more precisely than
[1023, 1034]).

Note: the *first* call at n=85/100 already shows the same `cached_tokens` as the *repeat* call.
This is because this exact deterministic filler text had been sent in earlier probe runs
(committed history shows this fixture set has been re-run — see the fixture re-run commit
immediately before this doc). It is incidental evidence that the cache persists across separate
process invocations, not just within one run, but the fixtures do not pin how long — **NOT
DETERMINED** beyond "longer than one script invocation."

**C2 — identical prefix twice: cache-read tokens reported in `usage.input_tokens_details`.**
Both `cached_tokens` (read) and `cache_write_tokens` (write) are present on every response's
`usage.input_tokens_details`, populated or zero as appropriate (see C1 table and C3 below). No
separate cache-specific endpoint or header is used; it is inline in the standard `/responses`
usage block.

**C3 — append preserves the cache read; editing early content invalidates it.**
`cache()`'s second probe (`*_c3_seed/_repeat/_append/_edit`) seeds a prefix marked `"The marker is
BLUE."`, repeats it, appends a suffix after it, and separately edits the marker to `"RED"`:

| step | input_tokens | cached_tokens | cache_write_tokens |
|---|---|---|---|
| seed | 1645 | 0 | 1642 |
| repeat (same prefix) | 1645 | 1642 | 0 |
| append (suffix added) | 1648 | 1633 | 12 |
| edit (prefix changed) | 1645 | 0 | 1642 |

(`gpt_6_astra_c3_*.json`; `gpt_6_luna_c3_*.json` matches for repeat/append/edit — `luna`'s `seed`
call hit `max_output_tokens` before emitting a message, `status: "incomplete"`, and reported
`usage: {input_tokens: 0, ...}` for that one call, an anomaly worth flagging rather than treating
as a cache-write measurement; `astra`'s seed and `luna`'s later repeat/append/edit calls agree, so
this doesn't put the C3 conclusion in doubt.) Verdict: append-only continuation preserves nearly
all of the prior cache read (1633/1642 ≈ 99%, the remainder is the small new write for the
appended suffix); editing content *before* the cached prefix's end drops the cache read to 0 and
forces a full re-write. This matches the FR-MAT/FR-CAP assumption that only strictly-appended
history keeps cache reads.

**C4 — TTL options: only `"30m"` accepted by the API for gpt-6-\*; documented pricing multipliers
0.1x read / 1.25x write.**
`gpt_6_{astra,luna}_c4_ttl_30m.json` (`prompt_cache_options: {ttl: "30m"}`) succeeds. `*_ttl_24h.json`
gets a 400:
```
"message": "Invalid value: '24h'. Supported values are: '30m'.",
"code": "invalid_value"
```
identical for both models. This is **OBSERVED** from the API. The write pricing multiplier is not
present in any usage/response field, so it is **DOCUMENTED**, not observed: OpenAI's prompt-caching
guide (fetched 2026-09-26, `https://developers.openai.com/api/docs/guides/prompt-caching`) states
that for GPT-5.6 and later (which includes the gpt-6-\* family probed here) "the only supported
value, `30m`, is also the default," with cache reads charged at "0.1x the uncached input-token
rate" and cache writes at "1.25x the uncached input-token rate." Earlier model families
additionally support `"in_memory"` and `"24h"` retention per that page, but that does not apply to
`gpt-6-astra`/`gpt-6-luna`, matching the rejection observed above.

## Compaction protocol (K1-K3)

OpenAI exposes two distinct compaction mechanisms, tested separately in
`probes/descriptor/openai/main.go` `compaction()`.

**K1a — a standalone `/responses/compact` endpoint exists.**
`gpt_6_astra_k1_compact.json` / `gpt_6_luna_k1_compact.json` post
`{model, input: [user, assistant], instructions: "Preserve the marker word for the next turn."}`
to `/responses/compact` and get back `object: "response.compaction"` with an `output` array
containing (a) the original input messages echoed back verbatim as plain `type: "message"` items,
and (b) one new `type: "compaction"` item carrying only `encrypted_content` (`{length: 1188,
sha256: ...}` for astra) — no plaintext summary. `usage: {input_tokens: 54, output_tokens: 48}`
(astra) is a small, separately-billed operation, not folded into a later inference's usage.
Verdict: **yes**, native, request/response shape as above; it does accept custom instructions
(the `instructions` field); minimum trigger for the *manual* endpoint was not tested (any input
size was accepted here).

**K1b — automatic compaction via `context_management` has a documented minimum trigger and runs
inline with inference, not as a separate pause step.**
`gpt_6_{astra,luna}_k1_auto_threshold.json` sends
`context_management: [{type: "compaction", compact_threshold: 1}]` and gets a 400:
```
"message": "Invalid 'context_management[0].compact_threshold': integer below minimum value. Expected a value >= 1000, but got 1 instead.",
"code": "integer_below_min_value"
```
for both models — **OBSERVED** minimum trigger is 1000 (units not stated by the error, presumably
tokens given the field's role). `gpt_6_astra_k1_auto_inline.json` (`compact_threshold: 1000`, a
~2.8K-token filler prompt) returns output types `["compaction", "message", "compaction"]` in a
*single* `/responses` call: two opaque `type: "compaction"` blocks bookending the assistant's
final `"cobalt"` message, with `usage.input_tokens: 2844` still reflecting the full uncompacted
prefix (compaction did not reduce this call's own billed input; presumably it produces artifacts
for the *next* call). `context_management` is `null` in the sanitized response body (the request
echo isn't returned), and `status: "incomplete"` here is due to `max_output_tokens` (64), unrelated
to compaction. Verdict for K2: automatic/inline compaction happens **inline with inference** in one
round trip — it is not a separate checkpoint/pause operation the runtime must complete before the
next model call, unlike the manual `/responses/compact` endpoint (K1a), which *is* a distinct
operation. The runtime can therefore use the manual endpoint as the FR-MAT-005 checkpoint
primitive, but should not rely on the automatic `context_management` path to provide a
pause-before-continue boundary — it decides and applies inline.

**K2 — mandatory-restoration message after compaction: accepted, restores correctly.**
`gpt_6_astra_k2_restore.json` / `gpt_6_luna_k2_restore.json` take the `k1_compact` output, append a
`{role: "developer", content: "Mandatory restoration: answer with the original marker word."}` plus
a new user turn, and call `/responses` normally (not `/responses/compact`). Both succeed and both
answer `"cobalt"` correctly (`usage.output... text: ["cobalt"]`), confirming the runtime *can*
append a restoration message ahead of the next inference after a manual compaction, per
FR-MAT-005 step 4. This is the intended checkpoint/pause pattern for the manual endpoint; K1b above
shows the automatic path doesn't offer an equivalent seam.

**K3 — compacted artifacts are opaque/encrypted, not inspectable.**
Both the manual endpoint's `compaction` item (`k1_compact`) and the automatic path's `compaction`
items (`k1_auto_inline`) carry only `encrypted_content` (hashed in the sanitized fixture, but the
live response has no plaintext `summary` or `content` field on these items — contrast with the
`response.compaction`'s echoed *original* messages, which are plain text). Verdict: **opaque**.
Coverage of what the compacted block actually represents must be tracked by the runtime from its
own request construction (what it sent into `/responses/compact`), not recovered by inspecting the
returned block, consistent with FR-MAT-005's "persist the canonical returned blocks and their
source coverage."

## Counter endpoint and model list

**Counter endpoint (`/responses/input_tokens`) matches billed input exactly, for every tested
request.** In the C1 table above, the counter's reported value (`gpt_6_{astra,luna}_c1_{n}_count.json`
`.response.input_tokens`) equals the corresponding live call's `usage.input_tokens` at every one
of the four prefix lengths, for both models (914/914, 1022/1022, 1034/1034, 1214/1214). This is
**OBSERVED** for these specific request shapes (plain developer+user text turns); it was not
re-verified for requests carrying tool definitions or reasoning items, since no `_count` probe was
run against those shapes — **NOT DETERMINED** whether the counter also matches billed input once
tools/reasoning are in the request.

**Model list.** `/models` returned 132 IDs (`probes/descriptor/testdata/openai/models.json`),
spanning `gpt-5` through `gpt-6-sol`. The `gpt-6-*` family (`gpt-6-astra`, `gpt-6-luna`,
`gpt-6-sol`) is the newest available as of the run date; `gpt-6-astra` and `gpt-6-luna` were
selected as the probed flagship-reasoning and cheaper-tier models respectively (`gpt-6-sol` was not
probed). The `30m`-only TTL rule documented for "GPT-5.6 and later" (see C4) applies to this whole
probed family.

## Descriptor implications for ADR 12

Both probed models (`gpt-6-astra`, `gpt-6-luna`) showed the **same qualitative profile** in every
question above; only token counts differed. One field (`gpt-6-luna`'s `c3_seed` call) was an
isolated `max_output_tokens`-truncation anomaly, not a profile difference — see C3. The draft
`Capabilities` below is therefore written once and applies to both `Model` values, with per-field
provenance.

| `Capabilities` field | Value (both models) | Provenance |
|---|---|---|
| `Provider` | `openai` | OBSERVED |
| `Model` | `gpt-6-astra` / `gpt-6-luna` | OBSERVED (bare ID from `/models`; no dated snapshot ID exists for these yet) |
| `Version` | bare alias only, no dated snapshot | OBSERVED (models.json has no `gpt-6-astra-YYYY-MM-DD` entries) |
| `ContextWindow` | — | **NOT DETERMINED** — no fixture exercised a context-window limit or reported it; would need a docs fetch or an over-budget probe |
| `MaxOutput` | — | **NOT DETERMINED** — `max_output_tokens` was a request parameter we chose (32-512), never the model's ceiling |
| `Counter` | provider-exact via `/responses/input_tokens` | OBSERVED, but only for plain developer/user text turns (C1 table); NOT DETERMINED for tool- or reasoning-bearing requests |
| `Caching.PrefixSemantics` | sequential prefix cache, no manual breakpoint parameter found in the Responses API surface used here | OBSERVED (implicit from C1/C3 behavior) for "no manual breakpoints"; ASSUMED that none exists elsewhere in the API — not exhaustively searched |
| `Caching.MinimumLength` | between 1023 and 1034 input tokens | OBSERVED, bracketed not pinned |
| `Caching.TTLs` | `"30m"` only (default); `"24h"` rejected | OBSERVED (both models identical 400) |
| `Pricing.CacheRead` / `Pricing.CacheWrite` multiplier | 0.1x / 1.25x of uncached input rate | DOCUMENTED (OpenAI prompt-caching guide, fetched 2026-09-26), not observed in any usage field |
| `Pricing.UncachedInput` / `Pricing.Output` ($ rates) | — | **NOT DETERMINED** — no fixture or fetch captured absolute per-token pricing |
| `Reasoning.ReplayRequired` | false | OBSERVED (R5: dropping pre-last-turn reasoning is accepted and produces a correct answer) |
| `Reasoning.BoundToPriorHistory` | false (bound only to the reasoning item's own encrypted content, not surrounding text) | OBSERVED (R3 corrupts the item itself → REJECTED; R4 edits surrounding text, leaves the item untouched → ACCEPTED) |
| `Edits[APPEND]` | SAFE | OBSERVED (R1) |
| `Edits[APPEND_SYSTEM]` | SAFE | OBSERVED (K2: a `developer`-role message inserted mid-history before the next inference is accepted and effective) |
| `Edits[DROP_LEADING_REASONING]` | LOSSY | OBSERVED (R5-drop: accepted, fresh reasoning generated instead of the original) |
| `Edits[DROP_ALL_REASONING]` | LOSSY | OBSERVED (R2: accepted, fresh reasoning generated instead of the original) |
| `Edits[REWRITE]` | **SAFE when the rewrite doesn't touch the reasoning item itself** (narrower than the general REWRITE-drops-reasoning default) | OBSERVED (R4); flagged for ADR 12 as a provider-specific exception worth encoding explicitly rather than falling back to the framework default, since it is more permissive, not less |
| `Edits[ADD_DEFERRED_TOOL]` | — | **NOT DETERMINED** — no probe added a tool mid-conversation after an initial reasoning turn without one |
| `Edits[MOVE_CACHE_MARKERS]` | — | **NOT DETERMINED / likely N/A** — no manual cache-marker/breakpoint mechanism was found in this API surface to move |
| `NativeCompaction` | true | OBSERVED (both `/responses/compact` and `context_management` compaction) |
| `CompactionInstructions` | true | OBSERVED (`instructions` field on `/responses/compact`, honored in `k2_restore`'s successful marker recall) |
| `CompactionProtocol` | **checkpoint/pause** via `/responses/compact` (separate operation, restoration message before next call demonstrated in K2); **inline** via `context_management` auto-compaction (no separate pause seam) | OBSERVED for both variants' *shape*; the automatic path's classification as a FR-MAT-006 "verified automatic mechanism guaranteeing the full mandatory set remains effective" is **ASSUMED, not verified** — only a single marker word was stress-tested, not a realistic mandatory-context set, so the runtime should not rely on the automatic path for FR-MAT-005/006 without further, harder probes |
| `CompactionMinimumTrigger` | 1000 (units unconfirmed, presumably tokens) | OBSERVED, automatic path only (400 at `compact_threshold: 1`); manual endpoint's minimum untested |
| `MandatoryPreservation` | a manually-inserted restoration message survives one compaction/restore round for a single fact | OBSERVED for that narrow case only; **ASSUMED** to generalize to a full mandatory set (policy/goals/pins/obligations) — untested, matches the caution above |
| `ContextEditing` | — | **NOT DETERMINED / ASSUMED false** — no distinct context-editing primitive (separate from compaction) was found or probed |
| `MidConversationSystem` | true | OBSERVED (K2's `developer`-role insertion mid-history, see `Edits[APPEND_SYSTEM]`) |
| `NativeMemory` | — | **NOT DETERMINED / ASSUMED false** — not probed |

**Recommendation for the adapter/strategy layer:** use `/responses/compact` (not automatic
`context_management`) as the FR-MAT-005 checkpoint primitive for `gpt-6-astra`/`gpt-6-luna` until
the automatic path is verified against a realistic mandatory-context set; treat `REWRITE` of
content preceding an open reasoning/tool round as SAFE specifically for this profile (do not fall
back to a REJECTED/LOSSY default that would force unnecessary resets); and continue to bracket
tighter on the minimum cache prefix length before shipping a hard-coded threshold into
`CachingRules`.

## Commander rulings (2026-09-26)

Rulings on the three open questions above, binding for ADR 12 and this descriptor:

1. **`ContextWindow`/`MaxOutput`.** No limit-exceeding probe will be run — it would cost money and
   settles nothing about reasoning/cache/compaction binding, the actual purpose of this probe.
   ADR 12 (Phase 5 gate) takes these two fields as **DOCUMENTED**, sourced from provider docs and
   cited with a retrieval date, not from a live probe. In this document they are
   **DOCUMENTED-PENDING**: the row above (`— / NOT DETERMINED`) stands until that docs citation is
   added at the ADR 12 draft; no further live probing is planned for them.

2. **`CompactionProtocol` / automatic `context_management` compaction.** Ruled **not trusted** as
   an FR-MAT-005/FR-MAT-006 checkpoint. The descriptor for `gpt-6-astra`/`gpt-6-luna` uses the
   explicit `/responses/compact` checkpoint/pause protocol (K1a/K2 above) as the only sanctioned
   compaction primitive; the automatic path stays unverified and unused by the strategy layer. A
   harder probe — several pins, goals, and an open tool round compacted together, not a single
   marker word — is required before any automatic path is enabled, and is scheduled for Phase 5,
   not this phase.

3. **`Edits[REWRITE]` = SAFE-when-reasoning-untouched.** Ruled a **per-profile descriptor
   override for OpenAI only**, not a change to the FR-CAP-002 framework default. FR-CAP-002's
   conservative default (a REWRITE drops trailing reasoning) stays as-is for every other/unknown
   profile; `gpt-6-astra`/`gpt-6-luna`'s descriptor carries the observed exception explicitly
   (R4 above), and no other provider's descriptor should infer the same behavior from this one.
