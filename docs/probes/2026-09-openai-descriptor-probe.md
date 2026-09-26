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

**R4 — edit an earlier message while replaying later reasoning unchanged: accepted by the
provider, but LOSSY at the runtime boundary (SEC-1.2 correction).**
`gpt_6_astra_r4_edit_earlier.json` rewrites the first user message's `content` (asking for lookup
code B instead of A) while leaving the tool-turn `reasoning`/`function_call`/`function_call_output`
items byte-for-byte unchanged, and still succeeds:
`usage: {input_tokens: 90, output_tokens: 23, reasoning_tokens: 15}`, answer `"cobalt"` (the value
fixed by the unedited tool result, not a value implied by the edited instruction). The API's
reasoning-item integrity check is confirmed to bind only to the reasoning item's own encrypted
content, not to the preceding conversation text (contrast with R3, where corrupting that same
field is rejected) — that half of the observation stands. But accepting the request is not the
same as the edit being safe: the model followed reasoning computed for the *pre-edit* history
(code A) and produced the pre-edit answer, silently ignoring the rewritten instruction (code B)
that the runtime meant to take effect. The reasoning item is opaque/encrypted, so the runtime
cannot itself detect that it now describes stale history. Verdict: **REWRITE = LOSSY, and the
provider offers no safety net for it** — the adapter must strip any reasoning items dated at or
after the rewritten content before dispatch itself, matching the Anthropic doc's identical
treatment of the same observation shape on Sonnet 5 (`BoundToPriorHistory: false` →
"the adapter must strip it itself; the provider offers no safety net", not SAFE). An opaque
reasoning item's effective coverage includes every item before it in history; ADR 6's pre-dispatch
eligibility recheck must treat it as depending on that whole prefix and drop it whenever any item
in that prefix is rewritten or loses eligibility, the same as it would for a REWRITE anywhere else
under FR-CAP-002's default. This is the FR-CAP-002 default, not a provider-specific exception —
the earlier "SAFE, narrower than the framework default" framing is withdrawn.

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

**K1a — a standalone `/responses/compact` endpoint exists (SPEC-1.2 correction: only 1 of the 2
input messages is echoed verbatim, not "the original input messages" plural).**
`gpt_6_astra_k1_compact.json` / `gpt_6_luna_k1_compact.json` post
`{model, input: [user "Remember the marker cobalt. Say ready.", assistant "Ready."],
instructions: "Preserve the marker word for the next turn."}` to `/responses/compact` and get back
`object: "response.compaction"` with an `output` array of length 2, not 4: **one**
`type: "message", role: "user"` item, reproducing only the `user` turn verbatim as plain text, and
**one** new `type: "compaction"` item carrying only `encrypted_content` (`{length: 1188,
sha256: ...}` for astra) — no plaintext summary. The `assistant` "Ready." turn is not echoed
anywhere in the output; it was folded into the opaque `compaction` item along with whatever else
the endpoint chose to compact. So the endpoint's behavior is: echo the still-open/most-recent
message(s) verbatim, fold the rest into one opaque block — not "echo every input message, then
append a compaction block." `usage: {input_tokens: 54, output_tokens: 48}` (astra) is a small,
separately-billed operation, not folded into a later inference's usage. Verdict: **yes**, native,
request/response shape as above (2-item output: 1 echoed message + 1 compaction block for this
2-message input); it does accept custom instructions (the `instructions` field); minimum trigger
for the *manual* endpoint was not tested (any input size was accepted here); which message(s) get
echoed verbatim versus folded was not swept across different input lengths/shapes — **NOT
DETERMINED** beyond this one 2-message case.

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

**K2 — mandatory-restoration message after compaction: accepted; effectiveness not demonstrated
(no control; the marker was already present in the echoed plaintext).**
`gpt_6_astra_k2_restore.json` / `gpt_6_luna_k2_restore.json` take the `k1_compact` output, append a
`{role: "developer", content: "Mandatory restoration: answer with the original marker word."}` plus
a new user turn, and call `/responses` normally (not `/responses/compact`). Both succeed and both
answer `"cobalt"` correctly (`usage.output... text: ["cobalt"]`), confirming the runtime *can*
append a `developer`-role message ahead of the next inference after a manual compaction (accepted,
no error) and get a correct answer, per FR-MAT-005 step 4's structural shape. **But** the
`k1_compact` output already echoes the original `user` message verbatim ("Remember the marker
cobalt. Say ready.") — see K1a — so the model could answer `"cobalt"` correctly from that echoed
plaintext alone, with or without the `developer` restoration message actually carrying any
authority. No run was made without the restoration message (a control), and no run asked the model
to follow a *new* rule not already present in the echoed plaintext (contrast the Anthropic probe's
K2, which restored a rule — hex output — absent from the compacted input and confirmed the model
obeyed it). This fixture cannot distinguish "the restoration message was read and given authority"
from "the model read the echoed user turn." Verdict: **accepted, effectiveness UNVERIFIED**. K1b's
inline-vs-checkpoint framing above still stands (that part concerns request shape, not authority),
but no claim of demonstrated restoration follows from this fixture.

**K3 — compacted artifacts are opaque/encrypted, not inspectable; the response only partially
reveals what was retained (SPEC-1.2: this sharpens rather than changes the verdict).**
Both the manual endpoint's `compaction` item (`k1_compact`) and the automatic path's `compaction`
items (`k1_auto_inline`) carry only `encrypted_content` (hashed in the sanitized fixture, but the
live response has no plaintext `summary` or `content` field on these items). Verdict: **opaque**
for the `compaction` item itself — its content cannot be inspected. But per the corrected K1a
above, the response *is not silent* about which of the input messages were folded into that opaque
block versus echoed verbatim: the `k1_compact` fixture shows 1 of 2 input messages (the `user`
turn) echoed as plain text, and 1 (the `assistant` turn) absent from the plaintext output, so
folded into the `compaction` item. That is a partial, coarse-grained signal (which items are
*not* opaque), not a way to inspect what the opaque item *contains* or confirms was preserved.
The runtime should not rely on this echo behavior to infer coverage — it must still track,
independently, what it sent into `/responses/compact` and treat the returned block as opaque for
every item not itself echoed back verbatim, consistent with FR-MAT-005's "persist the canonical
returned blocks and their source coverage."

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

Uses the shared field vocabulary in `docs/probes/descriptor-vocabulary.md` (SPEC-1.1), binding for
both provider drafts. Both probed models (`gpt-6-astra`, `gpt-6-luna`) showed the **same
qualitative profile** in every question above; only token counts differed (one exception, an
isolated `max_output_tokens`-truncation anomaly on `gpt-6-luna`'s `c3_seed` call, is not a profile
difference — see C3). One literal is given for `gpt-6-astra`; `gpt-6-luna`'s is identical except
`Model` and `Counter`.

```go
Capabilities{
	Provider: "openai", // OBSERVED
	Model:    "gpt-6-astra", // OBSERVED: models.json (bare ID; no dated snapshot exists yet)
	Version:  "responses/v1-responses/default/gpt-6-astra", // ASSUMED: the account-feature-profile
		// and required-headers segments are not observable — the probe brief forbids logging
		// request headers, so this profile string covers only what the sanitized fixtures show
		// (API surface, endpoint, bare model ID). A header or account-flag difference this probe
		// cannot see would silently be treated as the same profile.

	ContextWindow: 0, // DOCUMENTED-PENDING, RULING 1: no limit-exceeding probe was run (cost,
		// and it tests nothing about reasoning/cache/compaction binding); ADR 12 sources this from
		// provider docs with a retrieval date. Not usable until then.
	MaxOutput: 0, // DOCUMENTED-PENDING, RULING 1: same as ContextWindow.

	Counter: "openai./responses/input_tokens.gpt-6-astra", // OBSERVED: matches usage.input_tokens
		// exactly at all 4 tested prefix lengths (C1: 914/914, 1022/1022, 1034/1034, 1214/1214).
		// Scope: plain developer/user text turns only; not re-verified for tool- or
		// reasoning-bearing requests.

	Caching: CachingRules{
		PrefixSemantics: "exact-prefix", // OBSERVED (C3: editing content before the cached
			// prefix's end drops cached_tokens to 0; C1 brackets the floor)
		Breakpoints:     0, // OBSERVED: no caller-placed cache-breakpoint parameter found in the
			// Responses API surface exercised here; not exhaustively searched beyond that surface
		MinimumLength:   [2]int{1023, 1034}, // OBSERVED bracket (C1: 1022 tokens uncached, 1034
			// tokens cached, for both models; not pinned tighter)
		TTLs:            []string{"30m"}, // OBSERVED (C4: "24h" rejected with "Supported values
			// are: '30m'" for both models)
		Automatic:       true, // OBSERVED: cache reuse happens with no caller-placed breakpoint
			// (C1/C3)
	},

	Pricing: Pricing{
		UncachedInput: 0, // DOCUMENTED-PENDING: no fetch captured the absolute per-token rate
		CacheWrite:    map[string]float64{}, // DOCUMENTED-PENDING: absolute $/Mtok not fetched;
			// DOCUMENTED multiplier for context only (not a field value per section 3.2): 1.25x
			// uncached input rate for the "30m" tier (OpenAI prompt-caching guide, fetched
			// 2026-09-26)
		CacheRead:     0, // DOCUMENTED-PENDING: same gap; DOCUMENTED multiplier for context: 0.1x
			// uncached input rate (same source/date)
		Output:        0, // DOCUMENTED-PENDING: no fetch captured the absolute per-token rate
	},

	Reasoning: ReasoningRules{
		ReplayRequired:      false, // OBSERVED (R5: dropping reasoning from before the last user
			// turn is accepted and still produces the correct answer)
		BoundToPriorHistory: false, // OBSERVED (R4: rewriting the first user message while
			// replaying the later reasoning item unchanged is accepted; the provider does not
			// detect or report the mismatch — see PostEditReplay below for what happens next)
		BoundToModel:        false, // ASSUMED (fail-closed default): not probed
		DropsReported:       false, // OBSERVED (R2, R5-drop: the provider silently regenerates
			// fresh reasoning when the original is dropped; no field in usage or output reports
			// a drop)
		PostEditReplay:      "STALE", // OBSERVED (R4, SEC-1.2 finding): the provider accepts
			// reasoning replayed after an edit to earlier content and the model answers from the
			// stale, pre-edit reasoning without any error or reported drop. This is a finding
			// against the provider (section 3.3), not a state the runtime may rely on: the
			// adapter must strip any reasoning item whose coverage reaches into rewritten history
			// before dispatch, since this provider gives no safety net.
	},

	Edits: map[EditKind]EditSafety{
		APPEND:                 "SAFE", // OBSERVED (R1: reasoning replayed verbatim, unedited
			// history, correct answer, reasoning billed and used)
		APPEND_SYSTEM:          "REJECTED", // RULING 4 (SEC-1.1) policy value: the provider
			// accepts a mid-history `developer`-role message (K2, no error), but the strategy
			// treats this edit kind as REJECTED — never performed — until MidConversationSystem
			// is proven true. K2's fixture cannot show the inserted message carries any
			// authority: the marker it "restores" was already present verbatim in the plaintext
			// `user` turn /responses/compact echoes back (K1a), with no no-restoration control
			// and no restored rule absent from that echo. Mirrors the Anthropic draft's identical
			// policy value for the same reason.
		DROP_LEADING_REASONING: "LOSSY", // OBSERVED (R5-drop: accepted, fresh reasoning generated
			// instead of the original)
		DROP_ALL_REASONING:     "LOSSY", // OBSERVED (R2: accepted, fresh reasoning generated
			// instead of the original)
		REWRITE:                "REJECTED", // ASSUMED (fail-closed default): no fixture sends a
			// REWRITE per this vocabulary's definition (edited earlier content with reasoning at
			// and after the edit point already stripped by the adapter, per section 6). R2 tests
			// DROP_ALL_REASONING (reasoning stripped, no text edited); R4 tests PostEditReplay
			// (text edited, reasoning left in place, not stripped) — the combination that would
			// actually exercise REWRITE was not probed. A follow-up probe should send exactly
			// that combined request before this can move off the fail-closed default.
		// ADD_DEFERRED_TOOL and MOVE_CACHE_MARKERS: omitted (key absent = REJECTED). Neither was
		// exercised; MOVE_CACHE_MARKERS has no OpenAI mechanism to test (Caching.Breakpoints == 0).
	},

	NativeCompaction:       true, // OBSERVED (K1a standalone endpoint; K1b automatic path)
	CompactionInstructions: false, // ASSUMED (fail-closed default), not OBSERVED: K1a's
		// `instructions` field is accepted without error, but per this vocabulary's stricter
		// definition ("accepted AND changed the summary in a fixture") that is insufficient — the
		// compaction artifact is opaque (SummaryInspectable: false below), so whether the
		// instruction changed anything is unverifiable from any fixture.
	CompactionProtocol:      []CompactionProtocol{"CHECKPOINT"}, // OBSERVED (K1a: /responses/compact
		// is a standalone operation returning only the compaction output and echoed input, no task
		// actions). VERIFIED_AUTOMATIC (the context_management auto-compaction path, K1b) is
		// deliberately excluded per RULING 2: it runs inline within a single inference with no
		// separate pause seam, and only a single marker word was stress-tested, not a realistic
		// mandatory-context set — not trusted as an FR-MAT-005/006 checkpoint. A harder probe
		// (several pins, goals, an open tool round) is required before it can be added, scheduled
		// for Phase 5.
	CompactionMinimumTrigger: 0, // OBSERVED: the only sanctioned protocol (CHECKPOINT) has no
		// minimum — the caller decides when to call /responses/compact. The excluded automatic
		// path separately enforces a minimum of 1000 (400 at compact_threshold: 1, both models),
		// which does not set this field since that path is not sanctioned (RULING 2).

	MandatoryPreservation: PreservationRules{
		RestorationPlacement: []string{}, // ASSUMED (fail-closed default), RULING 4 (SEC-1.1):
			// no placement is proven effective. K2 accepted a `developer`-role restoration message
			// but cannot separate its effect from the model reading the already-echoed plaintext
			// user turn; no control run, no restored rule absent from that echo.
		ReturnedBlocks:        "echoed input item(s) + one encrypted compaction item", // OBSERVED
			// (K1a)
		RetainedFields:        "1 of 2 input messages echoed verbatim (user turn only; assistant turn folded into the compaction item)", // OBSERVED (K1a, SPEC-1.2 correction of the original "original input messages" overclaim)
		SummaryInspectable:    false, // OBSERVED (K3: `compaction` items carry only
			// `encrypted_content`, no plaintext `summary`/`content` field, in both the manual and
			// automatic paths)
		SummaryIntegrity:      "opaque", // OBSERVED (K3; also R3 shows the analogous reasoning
			// item's encrypted_content is integrity-checked and any edit to it is REJECTED)
		KeptReasoningValid:    false, // ASSUMED (fail-closed default): not probed in combination
			// with compaction
	},

	ContextEditing:        false, // ASSUMED (fail-closed default): no distinct context-editing
		// primitive (separate from compaction) was found or probed
	MidConversationSystem: false, // RULING 4 (SEC-1.1), fail-closed: K2 only shows the request is
		// accepted, not that the inserted message carries authority (see Edits[APPEND_SYSTEM])
	NativeMemory:          false, // ASSUMED (fail-closed default): not probed
}
```

**Recommendation for the adapter/strategy layer:** `CompactionProtocol: CHECKPOINT`
(`/responses/compact`) is the only OpenAI compaction protocol that *may* be sanctioned for
`gpt-6-astra`/`gpt-6-luna` — but with `MandatoryPreservation.RestorationPlacement: []` (no
placement proven), OpenAI native compaction **cannot yet satisfy FR-MAT-005**, and the strategy
must use runtime-side (non-native) compaction for these models until RULING 4's Phase 5 proof
lands (SEC-2.1); never rely on the automatic `context_management` path until it is verified against
a realistic mandatory-context set (RULING 2); treat every `REWRITE` as requiring the adapter to
strip reasoning whose coverage reaches rewritten history itself, since
`Reasoning.PostEditReplay: STALE` means this provider gives no rejection or invalidation signal
when that reasoning goes stale (SEC-1.2); never place mandatory/restoration content in a
mid-conversation `developer` message — `Edits[APPEND_SYSTEM]` is a policy `REJECTED` until
`MidConversationSystem` is proven true (SEC-1.1/RULING 4); and continue to bracket tighter on
`Caching.MinimumLength` before shipping a hard-coded threshold.

## Commander rulings (2026-09-26, amended in PR #4 rounds 1 and 2)

Rulings on the original three open questions, binding for ADR 12 and this descriptor. Ruling 3 is
amended and ruling 4 added following the PR #4 round-1 SEC review (SEC-1.1, SEC-1.2); ruling 2 is
further amended following the round-2 SEC review (SEC-2.1).

1. **`ContextWindow`/`MaxOutput`.** No limit-exceeding probe will be run — it would cost money and
   settles nothing about reasoning/cache/compaction binding, the actual purpose of this probe.
   ADR 12 (Phase 5 gate) takes these two fields as **DOCUMENTED**, sourced from provider docs and
   cited with a retrieval date, not from a live probe. In this document they are
   **DOCUMENTED-PENDING** (see the `Capabilities` literal above): the placeholder `0` value stands
   until that docs citation is added at the ADR 12 draft; no further live probing is planned for
   them.

2. **`CompactionProtocol` / automatic `context_management` compaction (amended, SEC-2.1).** The
   automatic `context_management` path is ruled **not trusted** as an FR-MAT-005/FR-MAT-006
   checkpoint, unchanged from the original ruling. What is amended: `CHECKPOINT`
   (`/responses/compact`, K1a) is the only compaction protocol that *may* be sanctioned for
   `gpt-6-astra`/`gpt-6-luna` — it is **not**, by itself, sufficient evidence that OpenAI native
   compaction satisfies FR-MAT-005. That requires a proven `MandatoryPreservation.RestorationPlacement`,
   and the descriptor currently has none (`[]`, RULING 4): K2 is accepted-but-UNVERIFIED evidence,
   not proof of restoration, and is dropped from this ruling's supporting evidence accordingly (see
   RULING 4). Until RULING 4's Phase 5 test proves a placement — covering both `user` and
   `developer` placement, since only `developer` was tried here — the strategy layer must use
   runtime-side (non-native) compaction for these models; OpenAI native compaction is not an
   available FR-MAT-005 mechanism yet, checkpoint-shaped or not. A harder probe — several pins,
   goals, and an open tool round compacted together, not a single marker word — is required before
   the automatic path specifically can be enabled, and is scheduled for Phase 5, not this phase.

3. **`Edits[REWRITE]` / `Reasoning.PostEditReplay` (SEC-1.2 — supersedes the original ruling 3
   below, and is restated once more after the SPEC-1.1 vocabulary conversion).** The original
   ruling 3 read "SAFE when the rewrite doesn't touch the reasoning item itself, a per-profile
   descriptor override for OpenAI only." **That ruling is withdrawn.** PR #4 round-1 SEC review
   (SEC-1.2) found the underlying R4 evidence shows the opposite: the provider *accepts* replaying
   reasoning bound to history that a later `REWRITE` already changed, and the model follows that
   stale reasoning to the pre-edit answer — acceptance is not safety. Under the shared vocabulary
   (`docs/probes/descriptor-vocabulary.md`), this observation is precisely
   `Reasoning.PostEditReplay: STALE` (section 3.3: "accepted and used without report... a finding
   against the provider, not a usable state"), not `Edits[REWRITE]` itself — a true `REWRITE`
   requires the adapter to have already stripped reasoning at/after the edit point before sending,
   which no OpenAI fixture tested. `Edits[REWRITE]` therefore reverts to its fail-closed default
   (`REJECTED`, untested) rather than being asserted `LOSSY`. In practice this makes no difference
   to the adapter's obligation: it must strip any reasoning item whose coverage reaches back across
   rewritten content before dispatch. ADR 6 rule 1 now defines this directly (an opaque reasoning
   block/item's coverage is its entire preceding rendered history, including system/instructions
   and tool definitions — added in parallel by the Anthropic-side PR #4 round-2 fix, SEC-2.2), so
   the adapter's pre-dispatch recheck follows from that ADR 6 rule, not from a per-profile
   allowance to preserve stale reasoning, and `PostEditReplay: STALE` is exactly the fact that makes
   skipping this
   unsafe for this provider.

4. **`MidConversationSystem` / `Edits[APPEND_SYSTEM]` (SEC-1.1).** Ruled **false / `REJECTED`,
   fail-closed**, mirroring the Anthropic probe's ruling 4. K2's fixture accepts a mid-history
   `developer`-role restoration message, but cannot show that message carries any authority: the
   marker it "restores" was already present verbatim in the plaintext `user` turn
   `/responses/compact` echoes back (K1a), so the model's correct answer is equally explained by
   reading that echo, with zero contribution from the inserted message. `MidConversationSystem`
   is `false`; `Edits[APPEND_SYSTEM]` is a **policy** `REJECTED` — the strategy never performs
   this edit — until a Phase 5 test proves otherwise, even though the provider itself returns no
   error for it (acceptance is not the same as the safety/authority classification the strategy
   layer needs). That test must restore a rule *absent* from the compacted input (so obedience can
   only be explained by the restoration message) and include a no-restoration control run,
   matching the Anthropic K2 design. Per SEC-2.1, this Phase 5 test is also what would establish
   `MandatoryPreservation.RestorationPlacement` for RULING 2/FR-MAT-005, so it must cover both
   `user` and `developer` placement — only `developer` was tried in this round's K2 fixture.
