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
| Fixtures | `probes/descriptor/testdata/anthropic/*.json`: request and response bodies only, no headers; thinking/compaction signatures reduced to prefix + length + sha256; `request_id` redacted; large filler text digested. `observations*.{md,json}` are the raw run logs (main run, `-threshold-edit`, `-rewrite`) |
| Spend | $0.49 for the main committed run, $0.04 for the follow-ups (threshold edit, REWRITE), about $0.95 including development runs (usage × list price; see `recorder.go`) |
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

Verdict (OBSERVED): the API accepts it and reports nothing, even inside an open tool round, so
an open round stays structurally valid without its reasoning and the T12 ALLOW_RESET path is
representable. Nothing the runtime sent was dropped by the provider, so in the strict
FR-CAP-002 sense the edit is SAFE. It is, however, a reasoning reset by construction: the
model loses and re-derives its plan. The descriptor should mark DROP_ALL_REASONING as LOSSY
so that strategies only perform it under ALLOW_RESET, and the runtime records the reset
itself (FR-ASM-011) because the provider never will. Removing a leading run of blocks is not a
"mismatch" in the API's sense, which is why `drop_block` reports nothing.

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

Those rows replay reasoning produced *after* the edit point, which SDD REWRITE never does
("a REWRITE always drops the reasoning of the rewritten range and everything after it"). The
REWRITE form itself was probed separately (fixtures `rewrite__<model>__*.json`), every
variant under `prefix_mismatch_behavior: "error"`, so any invalidated block would fail:

| Probe | opus-5-5 | sonnet-5 |
|---|---|---|
| RW1: edit `u0`, strip all reasoning after it | 200, `[]` | 200, `[]` |
| RW2: edit `u4` (in `H3`), replay reasoning *before* it (opus: `a1` and `a3` blocks), strip after it | **200, `[]`** | 200, `[]` (only `a1` had thinking) |
| RW3 control: edit `u4`, replay reasoning after it | not applicable: `a5` had no thinking in either run | same |

Verdict: Edits[REWRITE] with SDD semantics = **SAFE** on both models (OBSERVED). Blocks whose
prefix ends before the edit point remain valid, so reasoning continuity is preserved up to the
edit. Replaying post-edit reasoning is REJECTED (`error`) or LOSSY (`drop_block`) on Opus 5.5
and silently stale on Sonnet 5 and on unenforced legacy defaults. The adapter must strip
post-edit reasoning itself on every profile rather than rely on the provider check.

### Other edit kinds (all under `drop_block` + header, so any invalidation would be reported)

| Edit kind | opus-5-5 | sonnet-5 | Verdict |
|---|---|---|---|
| APPEND (baseline) | 200, `[]` | 200, `[]` | SAFE |
| APPEND_SYSTEM: `{"role":"system"}` message appended after the tool_result turn | 200, `[]`, in=695 | 200, `[]`, in=729 | Reasoning and cache stay valid on both. Sonnet 5 accepted it although the skill docs list it as unsupported there. **System authority is not verified**, so the descriptor never uses it (ruling 4) |
| MOVE_CACHE_MARKERS: `cache_control` added to `u0` | 200, `[]` | 200, `[]` | SAFE |
| ADD_DEFERRED_TOOL: extra `defer_loading: true` tool, no tool search tool | 200, `[]`, in=771 (+93) | 200, `[]`, in=805 | SAFE, but the deferred definition was **billed** (+93 tokens) without a tool search tool |
| DROP_LEADING_REASONING: remove `a1`'s thinking, keep `a3`'s (history `H3`) | 200, `[]` | 200, `[]` (only `a5` had thinking after `a1`; Sonnet 5 runs no check, so uninformative) | SAFE on opus-5-5 (later block still verified). The removed reasoning was visible to the model (R5), so this is a deliberate loss of older reasoning |
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
(OBSERVED, one write/repeat pair). Sonnet 5 never refused.

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
| On-demand: adopt `[block, user(question), {"role":"system"}(restoration)]` | 200, in=762, rule obeyed | Accepted and obeyed, but the user-text restoration above was obeyed equally, so this does **not** show that system authority is carried. System placement is UNVERIFIED and not used (ruling 4) |
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
| Either | - | a request carrying a compaction block with neither the on-demand beta nor a `compact_20260112` edit -> 400 `` `compaction` blocks require a `compact_20260112` strategy in `context_management.edits`. `` | `...T4a-threshold-block-without-strategy` |

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

## Descriptor implications for ADR 12

### Profile identity (FR-CAP-001)

Behavior differs by **model**, **account enforcement date** and **request setting**, so the
profile key must include all three:
`anthropic/messages-beta/<model>/binding=<error|drop_block>/account=<enforced|legacy>` (`unset`
is never sent, per ruling 1).
Recommendation: the adapter always sends `thinking-binding-controls-2026-08-01` and an
explicit `prefix_mismatch_behavior`: `error` under REQUIRE, `drop_block` under ALLOW_RESET.
Then the account date no longer changes behavior, and `input_transformations` is always
present, so drops are observable (FR-PROV-006). The two unsafe states (legacy + unset;
Sonnet 5) are never used for replay after an edit.

Legend: **O** = OBSERVED in this probe, **D** = DOCUMENTED (docs read 2026-09-26),
**A** = ASSUMED.

### Draft `Capabilities` - claude-opus-5-5 (profile `binding=error|drop_block`)

```go
Capabilities{
    Provider: "anthropic", Model: "claude-opus-5-5",
    Version: "messages-beta/sdk-go-v1.75.0/probe-2026-09-26",
    ContextWindow: 1_000_000,                      // O (models API)
    MaxOutput:     128_000,                        // O (models API)
    Counter: "anthropic.count_tokens/claude-opus-5-5", // O exact: error 0 on 4 requests; network call, verification only
    Caching: CachingRules{
        PrefixOrder:      "tools>system>messages", // D; O that any earlier byte change -> read 0 (C3)
        MinimumTokens:    512,                     // O bracket 512-517; D for Opus 5 (5.5 not listed)
        MaxBreakpoints:   4,                       // D
        TTLs:             {"5m", "1h"},            // O usage split ephemeral_5m/1h; expiry D
        Automatic:        true,                    // O top-level cache_control + lookback read
        AppendSystemSafe: false,                   // ruling 4: never used; O: read preserved (C3 step 4 read 1049)
        RefusalPersists:  false,                   // O one pair: refused write not readable
    },
    Pricing: Pricing{In: 4.00, Write5m: 5.00, Write1h: 8.00, Read: 0.20, Out: 20.00}, // D, USD/MTok
    Reasoning: ReasoningRules{
        ReplayRequired:      false, // O: stripping accepted, even in an open round (R2)
        PriorTurnsRetained:  true,  // O: earlier-turn thinking billed, not stripped (R5)
        BoundToPriorHistory: true,  // O: messages + system + tools (R4)
        BoundToModel:        true,  // O: model_binding_mismatch on switch to sonnet-5
        OpaqueFields:        "signature authoritative; thinking text display-only", // O (R3)
        DropsReported:       true,  // O only with the binding-controls header
        CanDisableThinking:  false, // D (400 at every effort); not probed
    },
    Edits: map[EditKind]EditSafety{
        APPEND:                 SAFE,     // O (R1)
        APPEND_SYSTEM:          REJECTED, // ruling 4 policy: never performed; O: provider accepts, thinking and cache stay valid, authority UNVERIFIED
        ADD_DEFERRED_TOOL:      SAFE,     // O thinking valid; deferred definition billed without tool search
        MOVE_CACHE_MARKERS:     SAFE,     // O thinking valid; D cache-neutral
        DROP_LEADING_REASONING: SAFE,     // O later blocks still verify; removed reasoning leaves context (R5)
        DROP_ALL_REASONING:     LOSSY,    // O accepted and unreported; a reset by construction (R2)
        REWRITE:                SAFE,     // O with post-edit reasoning stripped (RW1, RW2)
        // replaying post-edit reasoning: REJECTED with binding=error, LOSSY with drop_block (R4)
    },
    NativeCompaction:       true, // O on-demand and threshold
    CompactionInstructions: true, // O (instructions honoured)
    CompactionProtocol:     "checkpoint: on-demand compact-2026-09-04; pause: compact_20260112 + pause_after_compaction", // O
    CompactionMinimumTrigger: 50_000, // O threshold (400 below); on-demand has none (O at 828 tokens)
    MandatoryPreservation: PreservationRules{
        RestoreAfterBlock:     "user text (system placement UNVERIFIED, ruling 4)", // O user text obeyed (K2)
        KeptTurnsThinkingValid: true,  // O on-demand, system/tools unchanged (D condition)
        ThresholdReinsertedThinking: "strip or drop_block", // D
        SummaryIntegrity:      "on-demand signed (compaction_content_mismatch); threshold unsigned+editable", // O
        LosesMedia:            true,  // D images/documents/URLs in the summarized range
        CostInIterations:      true,  // O top-level usage is 0 on a compaction response
    },
    ContextEditing:        false, // ruling 5 (unverified); O: clear_thinking applied without invalidating thinking, clear_tool_uses did not apply
    MidConversationSystem: false, // ruling 4; O: accepted, thinking-safe, cache-safe, authority UNVERIFIED
    NativeMemory:          false, // A: memory_20250818 is a client-executed tool (D), not provider memory; not probed
}
```

### Draft `Capabilities` - claude-sonnet-5

Same as Opus 5.5 except:

```go
    Model: "claude-sonnet-5",
    Counter: "anthropic.count_tokens/claude-sonnet-5",  // O exact (4 requests)
    Caching.MinimumTokens: 1024,                        // O bracket 1023-1029; D 1024
    Pricing: Pricing{In: 2.00, Write5m: 2.50, Write1h: 4.00, Read: 0.20, Out: 10.00}, // D
    Reasoning.BoundToPriorHistory: false, // O: no conversation check even with binding=error (R4)
    Reasoning.BoundToModel:        true,  // A: the model check is documented as universal; only opus->sonnet was probed
    Reasoning.CanDisableThinking:  true,  // D ({type: "disabled"} accepted); not probed
    Edits[REWRITE]: SAFE,                 // O with post-edit reasoning stripped (RW1, RW2)
    // replaying post-edit reasoning is accepted with STALE reasoning and never reported (R4):
    // the adapter must strip it itself; the provider offers no safety net here.
    Edits[APPEND_SYSTEM]: REJECTED,       // ruling 4 policy: never performed; O accepted (200, cache-safe)
    MidConversationSystem: false,         // ruling 4; O accepted although the skill docs say unsupported; authority UNVERIFIED
    CompactionProtocol: "checkpoint (on-demand)",   // O K1 only; adoption/restore not run on this model (A: same as Opus); threshold deferred to Phase 5 (ruling 3)
    ContextEditing: false,                // ruling 5 (unverified); models API lists support, not exercised
```

### Consequences for the runtime

1. **T08 is satisfiable natively on Anthropic**: on-demand compaction is a standalone,
   ledgerable call with no task actions. Its block can be persisted; restoration goes after it
   as user text (system placement is unverified, ruling 4), then a separately prepared inference. Threshold
   compaction is acceptable only with `pause_after_compaction: true`. Without pause it is
   automatic in-request compaction and stays disabled (FR-MAT-005).
2. **T12 is representable**: an open tool round without its reasoning is structurally valid
   (R2), so ALLOW_RESET can rebase inside a round. Nothing reports the drop, so the runtime
   records `discarded block IDs` itself.
3. **REQUIRE has a native relief valve** on Opus 5.5: on-demand compaction keeps thinking
   after the block valid (kept-turns probe). Compacting a closed prefix and continuing
   preserves continuity for the turns after it. Threshold compaction does not.
4. **Usage accounting**: sum `usage.iterations` whenever it is present, and the three input
   fields (`input_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`) always.
   Treat `stop_reason: "refusal"` as a cache miss.
5. **Counter**: count_tokens is exact for the probed shapes. Use it to calibrate the local
   estimator (FR-COST-004) and for NFR-003 verification, not on the ingestion path.

## Open questions

1. **Enforced-account behavior** was not observable: this organization predates 2026-08-31.
   The `binding=error` profile emulates it. Confirm on a new organization before claiming the
   default (field unset) is REJECTED.
2. **Non-leading reasoning removal** and the **RW3 control** (edit a message, then replay
   reasoning produced after it) were not exercised: neither model emitted thinking on the
   tool call after the second user message. The docs say both invalidate later blocks, and R4
   already shows the same failure for reasoning after an edited first message.
3. **Threshold compaction without pause**, and threshold compaction on Sonnet 5, were not run
   ($0.22 each). ADR 12 disables the unpaused form anyway.
4. **Sonnet 5 mid-conversation system messages**: accepted with a 200 and cache-safe, but the
   docs list them as unsupported. Whether they carry system authority is unknown; keep
   `MidConversationSystem` UNVERIFIED for Sonnet 5 until a behavioral probe confirms it.
5. **`clear_tool_uses_20250919` did not apply** on a 2-tool-use history (trigger 100, keep 1).
   Cause unknown; not needed for the compaction verdicts.
6. **Cache TTL expiry** (5m/1h) was not waited out. Lifetime rules are DOCUMENTED only.
7. **Classifier refusals** (`cyber`) on benign filler: every ledger-text request in one development
   run (4 of 4), and 1 of 15 garden-note cache requests in the committed run (Opus 5.5 only).
   Refused requests report a cache write that is not readable. ADR 15 forecasts may need a
   refusal allowance, and the adapter may want the documented server-side `fallbacks` (not
   probed, because fallbacks change the model and so drop reasoning).
8. The SDK exposes everything used here as typed beta fields in v1.75.0. No raw-HTTP escape
   was needed; ADR 9's transport choice for Anthropic can rely on the official SDK.

## Commander rulings (2026-09-26)

These rulings resolve the open questions above. The draft `Capabilities` blocks have been
corrected to match them (PR #4 review, SEC-1.4): policy values are fail-closed, and the observed
facts are kept in the comments.

1. **Reasoning-binding control is always explicit.** The descriptor sets the
   `thinking-binding-controls-2026-08-01` beta and `prefix_mismatch_behavior` explicitly on
   every request, so behavior fails closed. The org-default (field unset) behavior does not need
   to be verified (resolves open question 1).
2. **Middle-thinking removal and post-edit replay are DOCUMENTED-only.** Phase 5 adapter
   contract tests must exercise both with forced thinking (open question 2).
3. **Unpaused threshold compaction stays disabled** (FR-MAT-005). Threshold compaction on
   Sonnet 5 is deferred to the Phase 5 contract tests (open question 3).
4. **`MidConversationSystem = false` for all profiles**, including claude-opus-5-5, until a
   Phase 5 test proves that system authority is carried (open question 4).
5. **`ContextEditing = false` (unverified) for all profiles** until the `clear_tool_uses`
   anomaly is explained in Phase 5 (open question 5).
6. **Cache TTLs are DOCUMENTED** (open question 6).
7. **Refusal allowance** is recorded as an input to ADR 15 (Phase 9) (open question 7).
8. **go.mod**: the commander reconciles both probe modules at merge (open question 8).
