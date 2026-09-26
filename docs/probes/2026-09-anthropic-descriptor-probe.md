# Anthropic descriptor probe (2026-09)

Status: observed, 2026-09-26. Throwaway live probe required by SDD section 11 phase 1 so that
ADR 12 (capability profiles, three-valued edit safety, provider blocks, REQUIRE/ALLOW_RESET,
checkpoint compaction) rests on observed behavior. It covers FR-CAP-001/002, FR-MAT-005,
FR-PROV-004/005/006, FR-ASM-011 and traces T08/T12.

## Method

| Item | Value |
|---|---|
| Code | `probes/descriptor/anthropic` (nested module `github.com/tdavison784/context-runtime/probes/descriptor`; root module untouched) |
| SDK | `github.com/anthropics/anthropic-sdk-go v1.75.0`, beta namespace (`client.Beta.Messages.New`, `.CountTokens`, `client.Beta.Models.List`); typed params for every field used, including `thinking.block_binding`, `compaction`, `context_management` |
| API surface | Claude API, `POST /v1/messages` (beta), `POST /v1/messages/count_tokens`, `GET /v1/models`; SDK default `anthropic-version` |
| Betas exercised | `thinking-binding-controls-2026-08-01`, `compact-2026-09-04` (on-demand compaction), `compact-2026-01-12` (threshold compaction), `context-management-2025-06-27` |
| Models | `claude-opus-5-5` (flagship: newest Opus listed, created 2026-09-21) and `claude-sonnet-5` (cheaper tier). Chosen from the live model list below |
| Account profile | An organization created **before 2026-08-31**: the preserved-thinking prefix check is *recorded but not enforced* unless a request opts in (R4 below). Accounts created on/after that date are enforced by default (DOCUMENTED, not observable from this account) |
| Fixtures | `probes/descriptor/testdata/anthropic/*.json`: request and response bodies only, no headers; thinking/compaction signatures reduced to prefix + length + sha256; `request_id` redacted; large filler text digested. `observations.{md,json}` is the raw run log |
| Spend | $0.49 for the committed run, about $0.9 including development runs (usage × list price; see `recorder.go`) |
| Rerun | `cd probes/descriptor && set -a; . ../../.env; set +a; go run ./anthropic -out testdata/anthropic/observations.md` |

Every request in the reasoning group uses the same shape: a fixed system prompt, one client
tool `lookup(key)`, `thinking: {type: "adaptive", display: "summarized"}` (summarized so the
thinking text can be inspected and tampered with) and `output_config.effort: "medium"`. The
first user turn needs reasoning before the tool call, so the assistant turn carries
`[thinking, text, tool_use]`. The histories used below are:

- `H1 = [u0, a1(thinking, text, tool_use), u2(tool_result)]` - an open round being answered.
- `H2 = H1 + a3(final answer)` - closed round.
- `H3 = H2 + u4("now key beta") + a5(thinking?, tool_use) + u6(tool_result)`.

Verdicts use FR-CAP-002: **SAFE** (accepted, reasoning kept), **LOSSY** (accepted, provider
drops or ignores reasoning), **REJECTED** (4xx). "Header" means the
`thinking-binding-controls-2026-08-01` beta; `drop_block` / `error` mean
`thinking.block_binding.prefix_mismatch_behavior`.

## Models available (live `GET /v1/models`, with the `compact-2026-09-04` beta)

Fixture `models_list.json`. All listed models report `max_input_tokens` 1,000,000 except
Opus 4.5 and Haiku 4.5 (200,000).

| Model | Created | Max input | Max output | Thinking types | `compaction.summarize` | `context_management` (compact / clear tool uses / clear thinking) |
|---|---|---|---|---|---|---|
| claude-opus-5-5 | 2026-09-21 | 1,000,000 | 128,000 | adaptive only (`enabled` false) | true | true / true / true |
| claude-sonnet-5 | 2026-06-29 | 1,000,000 | 128,000 | adaptive only (`enabled` false) | true | true / true / true |

Also listed: claude-fable-5-1, claude-opus-5, claude-fable-5, claude-opus-4-8, claude-opus-4-7,
claude-sonnet-4-6, claude-opus-4-6, claude-opus-4-5-20251101, claude-haiku-4-5-20251001,
claude-sonnet-4-5-20250929. The model object carries no pricing, cache minimum, or
preserved-thinking flag: those must come from docs or probes.

## Reasoning binding (R1-R5)

Fixtures `reasoning__<model>__<probe>.json`. "in" is billed input tokens (no caching in this
group). Baselines: Opus 5.5 replays `H1` at 678 input tokens, Sonnet 5 at 712.

### R1 - replay prior reasoning unchanged

| Model | No header | Header, field unset | Verdict |
|---|---|---|---|
| opus-5-5 | 200, in=678 | 200, in=678, `input_transformations: []` | Accepted. Edits[APPEND] = **SAFE** (OBSERVED) |
| sonnet-5 | 200, in=712 | 200, in=712, `[]` | **SAFE** (OBSERVED) |

`input_transformations` is **absent** without the header and **present (`[]`)** with it, on
every response from both models. A runtime can therefore only distinguish "nothing dropped"
from "not reported" when it sends the header.

### R2 - drop the open round's reasoning, keep `tool_use` / `tool_result`

| Model | No header | `drop_block` | Verdict |
|---|---|---|---|
| opus-5-5 | 200, in=604 (-74) | 200, in=604, `[]` | Accepted; the reasoning is gone and **nothing reports it**. The model re-reasons (new thinking block in the answer) |
| sonnet-5 | 200, in=618 (-94), out=162 vs 63 | 200, in=618, `[]`, out=183 | Same; output grew about 3x as the model re-planned |

Verdict: Edits[DROP_ALL_REASONING] = **LOSSY, silent** (OBSERVED). It is not REJECTED even
inside an open tool round, so an open round stays structurally valid without its reasoning
(the T12 ALLOW_RESET path is representable). Removing a leading run of blocks is not a
"mismatch" in the API's sense, which is why `drop_block` reports nothing; the runtime must
record the reset itself (FR-ASM-011).

### R3 - modify reasoning

| Model | Edit summarized thinking text | Flip one signature character | Flip signature + `drop_block` |
|---|---|---|---|
| opus-5-5 | 200, in=678 (identical to unchanged) | 400 `invalid_request_error`: ``messages.1.content.0: Invalid `signature` in `thinking` block`` | 400, same message |
| sonnet-5 | 200, in=712 (identical) | 400, same | 400, same |

Verdict: signature tampering is **REJECTED** before inference, and `drop_block` does not
cover it (OBSERVED). Editing the visible `thinking` text is accepted with an unchanged token
count: the summarized text is display-only and the model reasons from the signed payload
(inferred from identical billing). Adapters must treat both fields as opaque and replay them
byte-for-byte (FR-PROV-005); never "repair" or redact thinking text.

### R4 - edit an earlier message, then replay later reasoning

Variants of `H1`: `u0` with " Be brief." appended; the top-level `system` with one sentence
appended; the `lookup` tool description with one sentence appended.

| Model | Edit | No header | Header, unset | `error` | `drop_block` |
|---|---|---|---|---|---|
| opus-5-5 | `u0` | 200, in=683 (thinking still sent to model) | 200, `[{"type":"thinking_mismatch_allowed","path":"messages.1.content.0","reason":"prefix_binding_mismatch"}]` | **400**: ``Invalid `signature` in `thinking` block. The block is bound to a different conversation. Remove the block, or set `thinking.block_binding.prefix_mismatch_behavior` to "drop_block". Content before this block differs from when it was created, first at `messages.0.content.0`.`` | 200, in=609, `[{"type":"thinking_dropped","path":"messages.1.content.0","reason":"prefix_binding_mismatch"}]` |
| opus-5-5 | `system` | - | - | - | 200, in=610, `thinking_dropped` / `prefix_binding_mismatch` |
| opus-5-5 | tool description | - | - | - | 200, in=613, `thinking_dropped` / `prefix_binding_mismatch` |
| sonnet-5 | `u0` | 200, in=717 | 200, `[]` | **200**, `[]` | 200, in=717, `[]` |
| sonnet-5 | `system` / tool | - | - | - | 200, `[]`, thinking still billed |

Findings (OBSERVED):

- Opus 5.5 binds each thinking block to the exact prior prefix: messages, `system` and the
  tool definitions. The error names the first changed position, which an adapter can log.
- Enforcement is an **account property plus a request property**. On this pre-2026-08-31
  account the default is "record, don't enforce": the edited prefix goes to the model with
  stale reasoning. Setting the field to either value opts the request into enforcement. On
  new accounts the default is enforcement (`error`) (DOCUMENTED).
- Dropped blocks are not billed (in 683 -> 609).
- Sonnet 5 does not run the conversation check at all: `error` is accepted and the stale
  reasoning is still fed to the model. It still reports model-binding drops (below).

Verdict for REWRITE (FR-CAP-002), per profile:

| Profile | Edits[REWRITE] |
|---|---|
| opus-5-5, request sets `error` (or any account created on/after 2026-08-31 with the field unset) | **REJECTED** |
| opus-5-5, request sets `drop_block` | **LOSSY** (reported, unbilled) |
| opus-5-5, legacy account, field unset | Accepted with stale reasoning, reported only under the header as `thinking_mismatch_allowed` - an unsafe fourth state; never run this profile |
| sonnet-5, any setting | Accepted with stale reasoning, never reported - also unsafe |

SDD FR-CAP-002 already requires a REWRITE to start a fresh epoch without old reasoning, so the
runtime never relies on either unsafe state. The descriptor should still record them, so the
adapter strips reasoning itself on REWRITE for models that don't check.

### Other edit kinds (all under `drop_block` + header, so any invalidation would be reported)

| Edit kind | opus-5-5 | sonnet-5 | Verdict |
|---|---|---|---|
| APPEND (baseline) | 200, `[]` | 200, `[]` | SAFE |
| APPEND_SYSTEM: `{"role":"system"}` message appended after the tool_result turn | 200, `[]`, in=695 | 200, `[]`, in=729 | SAFE on both. Sonnet 5 accepted it although the skill docs list it as unsupported there; that it acts with system authority is not verified |
| MOVE_CACHE_MARKERS: `cache_control` added to `u0` | 200, `[]` | 200, `[]` | SAFE |
| ADD_DEFERRED_TOOL: extra `defer_loading: true` tool, no tool search tool | 200, `[]`, in=771 (+93) | 200, `[]`, in=805 | SAFE, but the deferred definition was **billed** (+93 tokens) without a tool search tool |
| DROP_LEADING_REASONING: remove `a1`'s thinking, keep `a3`'s (history `H3`) | 200, `[]` | 200, `[]` (only `a5` had thinking after `a1`) | SAFE (no drop reported; later block still valid) |
| Remove a non-leading block | not exercised: no run produced three thinking turns | - | DOCUMENTED as invalidating every later block |

### R5 - reasoning from turns before the last user message

`H2 + u4` (new user turn after a closed round), replaying all earlier thinking vs stripping it:

| Model | Replayed | Stripped | Delta | Transformations |
|---|---|---|---|---|
| opus-5-5 | in=793 (2 thinking blocks) | in=685 | +108 | `[]` both |
| sonnet-5 | in=795 (1 block) | in=701 | +94 | `[]` both |

Verdict (OBSERVED): earlier-turn reasoning is **not stripped** by the server. It is billed and
presumably visible to the model, matching the documented "keep all prior thinking" default for
Opus 4.5+/Sonnet 4.6+. It is **not required**: stripping is accepted and not reported, so
the stripped form is DROP_LEADING/ALL_REASONING = LOSSY-silent. Replaying it keeps reasoning
continuity (FR-ASM-011 REQUIRE).

### Model binding

`H1` produced by Opus 5.5, replayed to Sonnet 5 under `drop_block` + header: 200, in=670,
`[{"type":"thinking_dropped","path":"messages.1.content.0","reason":"model_binding_mismatch"}]`.
Blocks are bound to the producing model family: a switch or fallback is **LOSSY** and
reported only under the header (OBSERVED). This is a separate check from the prefix binding
and applies on every account.

## Cache reads (C1-C4)

Fixtures `cache__<model>__*.json`. Prefix = a `system` text block of deterministic
garden-note filler with `cache_control: {type: "ephemeral"}`, followed by a short user turn
(the uncached "tail": 16 tokens on Opus 5.5, 14 on Sonnet 5). Each point uses a fresh nonce so
a *write* decides whether it is cacheable. Usage fields read: `cache_creation_input_tokens`,
`cache_read_input_tokens`, `cache_creation.ephemeral_5m_input_tokens` /
`ephemeral_1h_input_tokens`, `input_tokens` (uncached remainder only).

### C1 - minimum cacheable prefix (token-level bisection)

| Model | Largest uncached | Smallest cached | Bracket | Documented |
|---|---|---|---|---|
| opus-5-5 | billed 527 = 511 prefix + 16 tail, `cache_creation_input_tokens: 0` | write 517 (billed 533) | 512 - 517 | 512 (listed for Opus 5; Opus 5.5 not listed) |
| sonnet-5 | billed 1036 = 1022 prefix + 14 tail | write 1029 (billed 1043) | 1023 - 1029 | 1024 |

Below the minimum the request succeeds with no error and zero cache fields; the only signal
is `cache_creation_input_tokens: 0` (OBSERVED). Both brackets are consistent with the
documented values: 512 for Opus 5.5 and 1024 for Sonnet 5, counted over the prefix up to the
breakpoint.

### C2 - identical prefix twice

| Model | First | Repeat |
|---|---|---|
| opus-5-5 | `cache_creation_input_tokens: 517`, `cache_read_input_tokens: 0`, `input_tokens: 16` | `cache_creation: 0`, `cache_read: 517`, `input_tokens: 16` |
| sonnet-5 | write 1029 | read 1029 |

Billed input = `input_tokens + cache_read_input_tokens + cache_creation_input_tokens`
(OBSERVED; token counting below agrees exactly).

### C3 - append-only continuation vs early edit (top-level automatic `cache_control`)

History: `u0` = about 1,000 (Opus) / 2,000 (Sonnet) tokens of filler; then `a0 "Noted."`,
`u1` question.

| Step | opus-5-5 (write / read) | sonnet-5 (write / read) |
|---|---|---|
| 1. `[u0]` | 1025 / 0 | 2052 / 0 |
| 2. `[u0, a0, u1]` (append) | 24 / **1025** | 24 / **2052** |
| 3. same, but `u0` gets one leading character | 1050 / **0** | 2077 / **0** |
| 4. step 2 + appended `{"role":"system"}` message | 15 / **1049** | 14 / **2076** |

Verdict (OBSERVED): append-only continuation reads the whole earlier prefix; an edit to the
first message drops the read to 0 and rewrites everything; an appended mid-conversation
system message keeps the prefix read (APPEND_SYSTEM is cache-SAFE on both models).
Automatic caching moves the breakpoint to the end and reads the previous entry via the
lookback.

### C4 - TTLs and pricing

`cache_control: {type: "ephemeral", ttl: "1h"}` reports the write under
`cache_creation.ephemeral_1h_input_tokens` (opus-5-5: 539 of 539; sonnet-5: 1051 of 1051),
the default under `ephemeral_5m_input_tokens` (OBSERVED). Expiry was not waited out.

Pricing (DOCUMENTED, https://platform.claude.com/docs/en/about-claude/pricing, read
2026-09-26), USD per million tokens:

| Model | Input | 5m write (1.25x) | 1h write (2x) | Cache read | Output |
|---|---|---|---|---|---|
| opus-5-5 | 4.00 | 5.00 | 8.00 | **0.20 (0.05x)** | 20.00 |
| sonnet-5 | 2.00 | 2.50 | 4.00 | 0.20 (0.1x) | 10.00 |

Other documented cache facts used below: at most 4 breakpoints; `tools` -> `system` ->
`messages` render order; a 20-block lookback for automatic breakpoints; longer TTL entries
must precede shorter ones; caches are per workspace; a read refreshes the TTL; lifetime is
measured from request start.

### Incidental: refusals and cache writes

An earlier filler ("Ledger line N: account N moved N units to bucket X") made Opus 5.5 return
HTTP 200 `stop_reason: "refusal"`, `stop_details.category: "cyber"`, `output_tokens: 0`, and
once, in the committed run, a 295-token garden-note prompt did too. The refused response
**reported a 560-token cache write, but an identical repeat read 0 and wrote again**
(`incidental__opus-5-5__refusal-cache-write.json`, `...-no-read.json`). A refusal is
therefore a cache miss for forecasting purposes, whatever `cache_creation_input_tokens` says
(OBSERVED, n=2). Sonnet 5 never refused.

## Token counting (FR-PROV-004)

`POST /v1/messages/count_tokens` exists for both models, accepts the same body shape
(system, tools, thinking, `output_config`, betas) and returned **exactly** the billed input
total for every comparison made, including thinking replay and cached requests:

| Request | opus-5-5 count / billed | sonnet-5 count / billed |
|---|---|---|
| first tool turn | 498 / 498 | 564 / 564 |
| `H1` replay with thinking | 678 / 678 | 712 / 712 |
| `H2 + u4` with 2 (1) thinking blocks, header set | 793 / 793 | 795 / 795 |
| cached prefix request (read + uncached) | 533 / 533 | 1043 / 1043 |

Verdict: an exact counter backed by the endpoint is available (OBSERVED, error bound 0 on
n=4 per model). It is a network call, so FR-PROV-004 keeps it off the ingestion path. The
docs also say it runs the preserved-thinking check and ignores the `compaction` parameter
(DOCUMENTED, not exercised).

## Compaction protocol (K1-K3)

Anthropic offers three native context features. Docs: `build-with-claude/compaction`,
`compaction-on-demand`, `compaction-threshold` and `context-editing`, read 2026-09-26.

| Feature | Beta | Request | Who decides when |
|---|---|---|---|
| On-demand compaction | `compact-2026-09-04` | top-level `"compaction": {"type": "summarize", "instructions"?}` | the caller; a **standalone** request that returns only the block |
| Threshold compaction | `compact-2026-01-12` | `context_management.edits: [{"type": "compact_20260112", "trigger": {"type": "input_tokens", "value": N}, "pause_after_compaction"?, "instructions"?}]` | the API, inside an ordinary request once input reaches N |
| Context editing | `context-management-2025-06-27` | `clear_tool_uses_20250919`, `clear_thinking_20251015` edits | the API, by rule; clears, does not summarize |

### K1 - exercise and artifact shape

**On-demand** (`H2`, custom `instructions`), fixture `compaction__<model>__K1-on-demand-summarize.json`:

- 200, `stop_reason: "compaction"`, `content` = exactly one block
  `{"type": "compaction", "content": "<readable summary>", "signature": "<opaque>"}`
  (opus-5-5: 647-char summary, 1,736-char signature; sonnet-5: 533 / 636). No thinking, text
  or tool_use blocks.
- Usage: top-level `input_tokens: 0`, `output_tokens: 0`; the cost is only in
  `usage.iterations: [{"type": "compaction", "input_tokens": 828, "output_tokens": 294, ...}]`.
  An adapter that reads only the top-level fields under-reports compaction cost
  (FR-PROV-006).
- Custom instructions: accepted (up to 16,384 chars, replaces the default prompt; DOCUMENTED).
  The summary followed them (kept key, value, result, open request).
- Minimum trigger: none. The caller decides; it worked on an 828-token conversation.
  Constraints (DOCUMENTED): no `stop_sequences`, `output_config.format` or forced
  `tool_choice`; rejected while the last assistant turn has an unanswered `tool_use`; cannot
  be combined with `context_management` in one request.

**Threshold** (fixture `...T1`, `...T2`):

- `trigger.value: 1000` -> 400 `context_management.edits.0.compact_20260112: trigger.value must
  be at least 50000`. **Minimum trigger = 50,000 input tokens** (OBSERVED; docs default 150,000).
- A 51,924-token request with `trigger: 50000`, `pause_after_compaction: true` and custom
  instructions -> 200, `stop_reason: "compaction"`, one block `{"type": "compaction",
  "content": "<942-char summary>"}` with **no signature and no `encrypted_content`**;
  `iterations: [{"type": "compaction", "input_tokens": 51924, "output_tokens": 602}]`, top-level
  zeros. Cost $0.22.

### K2 - can the runtime restore mandatory context before the next inference?

| Path | Observed | FR-MAT-005 fit |
|---|---|---|
| On-demand: adopt `[assistant{compaction block}, user(restoration + question)]` | 200, in=762, answer obeys the restored rule (values also in hex) | **Checkpoint protocol.** The compaction call generates no task actions, and the runtime chooses what goes after the block |
| On-demand: adopt `[block, user(question), {"role":"system"}(restoration)]` | 200, in=762, rule obeyed | Restoration can use system authority (APPEND_SYSTEM) |
| Threshold with `pause_after_compaction: true`, then `[assistant{block}, user(restoration + question)]` with the edit still configured | 200, in=919, rule obeyed | **Pause protocol.** The pausing response held only the summary (no task action), and restoration happened before the next inference |
| Threshold without pause | not exercised (another $0.22) | Summary and continuation in one response: the runtime cannot restore in between, so under FR-MAT-005 this is "automatic in-request compaction" and stays disabled |

Kept turns and thinking: after on-demand compaction of `H2`, the turns taken since
(`u4, a5, u6` from `H3`) were sent after the block under `drop_block` + header: 200,
`input_transformations: []` (fixture `...K-kept-turns-thinking-dropblock`). Reasoning in turns
after the summarized range **stays valid** (OBSERVED on opus-5-5), provided `system` and
`tools` do not change (DOCUMENTED). For threshold compaction the docs say the opposite: turns
re-inserted after the block must have their thinking removed or dropped.

### K3 - opaque or inspectable?

| Artifact | Readable? | Integrity | Evidence |
|---|---|---|---|
| On-demand block | yes, `content` is plain text | **signed**: appending text to `content` -> 400 `` `compaction` block `content` does not match its `signature` ``, `error.details.error_code: "compaction_content_mismatch"` | `...K3-tamper-summary` |
| Threshold block | yes | **unsigned and editable**: an edited summary (added "answer in words") was accepted and obeyed ("Sixty-two") | `...T4-threshold-block-edited` |
| Either | - | a request carrying a compaction block with neither the on-demand beta nor a `compact_20260112` edit -> 400 ``compaction` blocks require a `compact_20260112` strategy in `context_management.edits`.`` | `...T4a-threshold-block-without-strategy` |

Coverage: both blocks are readable summaries, so the runtime can store the text and
record its source coverage (the message range sent). What the summary actually retained is
not checkable: coverage is "declared by range, content unverified". Images, documents and
fetched URLs in the summarized range are lost (DOCUMENTED). On-demand blocks are
provider-bound opaque state for replay (FR-PROV-005): store `content` + `signature` verbatim.

### Context editing (exercised once each, opus-5-5, on `H3`, `drop_block` + header)

- `clear_thinking_20251015`, `keep: {type: "thinking_turns", value: 1}` -> `applied_edits:
  [{"type": "clear_thinking_20251015", "cleared_thinking_turns": 2, "cleared_input_tokens": 44}]`,
  `input_transformations: []`. Server-side clearing does **not** count as a history edit
  (OBSERVED; DOCUMENTED for Opus 5.5 / Fable 5.1).
- `clear_tool_uses_20250919`, `trigger: {input_tokens: 100}`, `keep: {tool_uses: 1}` ->
  `applied_edits: []` on a 2-tool-use history. Not applied; the cause was not determined.
- Docs: clearing invalidates the prompt cache from the cleared point; `clear_thinking` must be
  first when edits are combined; tool-use trigger default 100,000 input tokens, keep 3.
