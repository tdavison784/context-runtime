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
