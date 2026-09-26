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
